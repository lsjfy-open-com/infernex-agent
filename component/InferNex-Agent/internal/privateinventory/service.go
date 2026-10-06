// Package privateinventory coordinates registered connections, read-only
// discovery previews, and explicit snapshot recording. It never accepts an
// endpoint, credential, tenant scope, or snapshot body from a discovery/save
// tool call.
package privateinventory

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/adapters/dockerdiscovery"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domain"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domainstore"
)

type Store interface {
	Scope() string
	Get(context.Context, string, domain.RecordRef) (domain.Record, error)
	Verify(context.Context, string, domain.RecordRef) (domain.Record, error)
	List(context.Context, string, domainstore.ListOptions) ([]domainstore.ListEntry, error)
	ListPage(context.Context, string, domainstore.ListOptions) (domainstore.ListPage, error)
	Connection(context.Context, string, domain.RecordRef) (domainstore.Connection, error)
	CurrentEnvironment(context.Context, string, string) (*domain.Environment, error)
	SaveSnapshot(context.Context, string, *domain.InventorySnapshot) (domain.RecordRef, error)
}

type KubernetesDiscoverFunc func(context.Context, domain.Environment, domainstore.Connection, DiscoverRequest) (*domain.InventorySnapshot, string, error)

type Option func(*Service)

func WithKubernetesDiscover(discover KubernetesDiscoverFunc) Option {
	return func(service *Service) { service.kubernetesDiscover = discover }
}

type Service struct {
	store              Store
	kubernetesDiscover KubernetesDiscoverFunc
	now                func() time.Time
	random             io.Reader

	mu          sync.Mutex
	previews    map[string]*previewEntry
	listCursors map[string]*listCursorEntry
}

type DiscoverRequest struct {
	Principal     string
	EnvironmentID string
	Revision      int64
	ResourceKinds []string
	Namespaces    []string
	IncludeNodes  bool
	CursorHandle  string
}

type Preview struct {
	Handle         string              `json:"previewHandle"`
	Digest         string              `json:"digest"`
	TenantScope    string              `json:"tenantScope"`
	EnvironmentRef domain.RecordRef    `json:"environmentRef"`
	SnapshotRef    domain.RecordRef    `json:"snapshotRef"`
	Completeness   domain.Completeness `json:"completeness"`
	EntityCount    int                 `json:"entityCount"`
	IssueCount     int                 `json:"issueCount"`
	ExpiresAt      string              `json:"expiresAt"`
	CursorHandle   string              `json:"cursorHandle,omitempty"`
}

type LocalPreview struct {
	Preview  Preview
	Snapshot *domain.InventorySnapshot
}

type previewEntry struct {
	principal      string
	scope          string
	environmentRef domain.RecordRef
	digest         string
	snapshot       *domain.InventorySnapshot
	expiresAt      time.Time
	saving         bool
}

type listCursorEntry struct {
	principal string
	scope     string
	kind      domain.Kind
	limit     int
	after     domain.RecordRef
	expiresAt time.Time
	inUse     bool
}

type ListRequest struct {
	Principal    string
	Kind         domain.Kind
	Limit        int
	CursorHandle string
}

type ListPage struct {
	Entries      []domainstore.ListEntry
	CursorHandle string
	Truncated    bool
}

func New(store Store, options ...Option) (*Service, error) {
	if store == nil || store.Scope() == "" {
		return nil, errors.New("private inventory store is required")
	}
	service := &Service{
		store: store, now: time.Now, random: rand.Reader,
		previews: make(map[string]*previewEntry), listCursors: make(map[string]*listCursorEntry),
	}
	for _, option := range options {
		option(service)
	}
	return service, nil
}

func (s *Service) Scope() string { return s.store.Scope() }

func (s *Service) Discover(ctx context.Context, request DiscoverRequest) (LocalPreview, error) {
	if request.Principal == "" || request.EnvironmentID == "" || request.Revision <= 0 {
		return LocalPreview{}, errors.New("principal and environment reference are required")
	}
	ref := domain.RecordRef{TenantScope: s.Scope(), Kind: domain.KindEnvironment, ID: request.EnvironmentID, Revision: request.Revision}
	environment, err := s.store.CurrentEnvironment(ctx, s.Scope(), request.EnvironmentID)
	if err != nil {
		return LocalPreview{}, safeStoreError(err)
	}
	if environment.Reference() != ref {
		return LocalPreview{}, errors.New("requested Environment revision is stale")
	}
	if !environment.Spec.Enabled {
		return LocalPreview{}, errors.New("registered Environment is disabled")
	}
	connection, err := s.store.Connection(ctx, s.Scope(), ref)
	if err != nil {
		return LocalPreview{}, safeStoreError(err)
	}
	if connection.Runtime != environment.Spec.Runtime {
		return LocalPreview{}, errors.New("registered runtime binding is inconsistent")
	}

	var snapshot *domain.InventorySnapshot
	var cursor string
	switch environment.Spec.Runtime {
	case domain.RuntimeDocker:
		if len(request.Namespaces) != 0 || request.IncludeNodes || request.CursorHandle != "" {
			return LocalPreview{}, errors.New("Docker discovery does not accept Kubernetes scope or cursors")
		}
		client, clientErr := dockerdiscovery.NewClient(connection.Endpoint, connection.ExpectedDaemonID)
		if clientErr != nil {
			return LocalPreview{}, clientErr
		}
		defer client.CloseIdleConnections()
		discoverer, clientErr := dockerdiscovery.NewDiscoverer(client)
		if clientErr != nil {
			return LocalPreview{}, clientErr
		}
		snapshot, err = discoverer.Discover(ctx, *environment, dockerdiscovery.DiscoverRequest{
			PrincipalScope: s.Scope(), ResourceKinds: request.ResourceKinds,
		})
	case domain.RuntimeKubernetes:
		if s.kubernetesDiscover == nil {
			return LocalPreview{}, errors.New("Kubernetes private discovery is not configured")
		}
		allowed := make(map[string]bool, len(environment.Spec.AllowedNamespaces))
		for _, namespace := range environment.Spec.AllowedNamespaces {
			allowed[namespace] = true
		}
		for _, namespace := range request.Namespaces {
			if !allowed[namespace] {
				return LocalPreview{}, errors.New("requested namespace is outside the registered scope")
			}
		}
		snapshot, cursor, err = s.kubernetesDiscover(ctx, *environment, connection, request)
	default:
		return LocalPreview{}, errors.New("registered runtime is unsupported")
	}
	if err != nil {
		return LocalPreview{}, err
	}
	if snapshot == nil || snapshot.Digest == "" {
		return LocalPreview{}, errors.New("discovery returned an invalid preview")
	}

	cacheSnapshot, err := cloneSnapshot(snapshot)
	if err != nil {
		return LocalPreview{}, err
	}
	handle, expiresAt, err := s.putPreview(request.Principal, environment.Reference(), cacheSnapshot)
	if err != nil {
		return LocalPreview{}, err
	}
	preview := Preview{
		Handle: handle, Digest: snapshot.Digest, TenantScope: snapshot.TenantScope,
		EnvironmentRef: snapshot.Spec.EnvironmentRef, SnapshotRef: snapshot.Reference(),
		Completeness: snapshot.Spec.Completeness, EntityCount: len(snapshot.Spec.Entities),
		IssueCount: len(snapshot.Spec.Issues), ExpiresAt: expiresAt.UTC().Format(time.RFC3339Nano), CursorHandle: cursor,
	}
	return LocalPreview{Preview: preview, Snapshot: snapshot}, nil
}

func (s *Service) Record(ctx context.Context, principal, handle, digest string) (domain.RecordRef, error) {
	if principal == "" || handle == "" || digest == "" {
		return domain.RecordRef{}, errors.New("previewHandle and digest are required")
	}
	now := s.now()
	s.mu.Lock()
	s.expireLocked(now)
	entry := s.previews[handle]
	if entry == nil || entry.principal != principal || entry.scope != s.Scope() || subtle.ConstantTimeCompare([]byte(entry.digest), []byte(digest)) != 1 || entry.saving {
		s.mu.Unlock()
		return domain.RecordRef{}, errors.New("preview handle binding is invalid or expired")
	}
	entry.saving = true
	if entry.snapshot == nil || subtle.ConstantTimeCompare([]byte(entry.snapshot.Digest), []byte(entry.digest)) != 1 || domain.VerifyRecord(entry.snapshot) != nil {
		entry.saving = false
		s.mu.Unlock()
		return domain.RecordRef{}, errors.New("preview integrity check failed")
	}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		if current := s.previews[handle]; current == entry {
			current.saving = false
		}
		s.mu.Unlock()
	}()
	environment, err := s.store.CurrentEnvironment(ctx, s.Scope(), entry.environmentRef.ID)
	if err != nil {
		return domain.RecordRef{}, safeStoreError(err)
	}
	if environment.Reference() != entry.environmentRef || !environment.Spec.Enabled {
		return domain.RecordRef{}, errors.New("registered environment changed")
	}
	if previous := entry.snapshot.Spec.PreviousSnapshotRef; previous != nil {
		if _, err := s.store.Get(ctx, s.Scope(), *previous); err != nil {
			return domain.RecordRef{}, errors.New("previous continuation snapshot must be recorded first")
		}
	}
	ref, err := s.store.SaveSnapshot(ctx, s.Scope(), entry.snapshot)
	if err != nil {
		return domain.RecordRef{}, safeStoreError(err)
	}
	return ref, nil
}

func cloneSnapshot(snapshot *domain.InventorySnapshot) (*domain.InventorySnapshot, error) {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, errors.New("encode private inventory preview")
	}
	cloned, err := domain.DecodeInventorySnapshot(raw)
	if err != nil {
		return nil, errors.New("clone private inventory preview")
	}
	return cloned, nil
}

func (s *Service) List(ctx context.Context, kind domain.Kind, limit int) ([]domainstore.ListEntry, error) {
	if limit == 0 {
		limit = domain.DefaultPageSize
	}
	if limit < 1 || limit > domain.MaxPageSize {
		return nil, fmt.Errorf("list limit must be between 1 and %d", domain.MaxPageSize)
	}
	if kind != "" && kind != domain.KindEnvironment && kind != domain.KindInventorySnapshot {
		return nil, errors.New("record kind is invalid")
	}
	entries, err := s.store.List(ctx, s.Scope(), domainstore.ListOptions{Kind: kind, Limit: limit})
	if err != nil {
		return nil, safeStoreError(err)
	}
	return entries, nil
}

func (s *Service) ListPage(ctx context.Context, request ListRequest) (ListPage, error) {
	if request.Principal == "" {
		return ListPage{}, errors.New("principal is required")
	}
	var after *domain.RecordRef
	var claimedCursor *listCursorEntry
	defer func() {
		if claimedCursor == nil {
			return
		}
		s.mu.Lock()
		if current := s.listCursors[request.CursorHandle]; current == claimedCursor {
			current.inUse = false
		}
		s.mu.Unlock()
	}()
	kind, limit := request.Kind, request.Limit
	if request.CursorHandle != "" {
		if kind != "" || limit != 0 {
			return ListPage{}, errors.New("cursor continuation accepts no new list filters")
		}
		now := s.now()
		s.mu.Lock()
		s.expireLocked(now)
		cursor := s.listCursors[request.CursorHandle]
		if cursor == nil || cursor.principal != request.Principal || cursor.scope != s.Scope() || cursor.inUse {
			s.mu.Unlock()
			return ListPage{}, errors.New("list cursor binding is invalid or expired")
		}
		cursor.inUse = true
		claimedCursor = cursor
		kind, limit = cursor.kind, cursor.limit
		value := cursor.after
		after = &value
		s.mu.Unlock()
	}
	if limit == 0 {
		limit = domain.DefaultPageSize
	}
	if limit < 1 || limit > domain.MaxPageSize {
		return ListPage{}, fmt.Errorf("list limit must be between 1 and %d", domain.MaxPageSize)
	}
	if kind != "" && kind != domain.KindEnvironment && kind != domain.KindInventorySnapshot {
		return ListPage{}, errors.New("record kind is invalid")
	}

	requestedLimit := limit
	var page domainstore.ListPage
	for {
		var err error
		page, err = s.store.ListPage(ctx, s.Scope(), domainstore.ListOptions{Kind: kind, Limit: limit, After: after})
		if err != nil {
			return ListPage{}, safeStoreError(err)
		}
		probe := struct {
			Records      []domainstore.ListEntry `json:"records"`
			Returned     int                     `json:"returned"`
			Truncated    bool                    `json:"truncated"`
			CursorHandle string                  `json:"cursorHandle,omitempty"`
		}{Records: page.Entries, Returned: len(page.Entries), Truncated: page.HasMore || limit < requestedLimit}
		if page.HasMore {
			probe.CursorHandle = strings.Repeat("0", 64)
		}
		raw, err := json.Marshal(probe)
		if err != nil {
			return ListPage{}, errors.New("encode record list page")
		}
		if len(raw) <= domain.MaxToolResponseBytes || limit == 1 {
			if len(raw) > domain.MaxToolResponseBytes {
				return ListPage{}, errors.New("one record summary exceeds the tool response budget")
			}
			break
		}
		limit--
	}
	result := ListPage{Entries: page.Entries, Truncated: page.HasMore || limit < requestedLimit}
	if request.CursorHandle != "" {
		s.mu.Lock()
		if current := s.listCursors[request.CursorHandle]; current == claimedCursor {
			delete(s.listCursors, request.CursorHandle)
		}
		s.mu.Unlock()
		claimedCursor = nil
	}
	if page.HasMore {
		if page.NextRef == nil {
			return ListPage{}, errors.New("record list page is missing its continuation reference")
		}
		handle, err := s.putListCursor(request.Principal, kind, limit, *page.NextRef)
		if err != nil {
			return ListPage{}, err
		}
		result.CursorHandle = handle
	}
	return result, nil
}

func (s *Service) Get(ctx context.Context, ref domain.RecordRef) (domain.Record, error) {
	ref.TenantScope = s.Scope()
	record, err := s.store.Get(ctx, s.Scope(), ref)
	if err != nil {
		return nil, safeStoreError(err)
	}
	return record, nil
}

func (s *Service) Verify(ctx context.Context, ref domain.RecordRef) (domain.Record, error) {
	ref.TenantScope = s.Scope()
	record, err := s.store.Verify(ctx, s.Scope(), ref)
	if err != nil {
		return nil, safeStoreError(err)
	}
	return record, nil
}

func safeStoreError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var typed *domainstore.Error
	if errors.As(err, &typed) {
		return fmt.Errorf("private inventory store: %s", typed.Code)
	}
	return errors.New("private inventory store operation failed")
}

func (s *Service) putPreview(principal string, environmentRef domain.RecordRef, snapshot *domain.InventorySnapshot) (string, time.Time, error) {
	if principal == "" {
		return "", time.Time{}, errors.New("principal is required")
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(now)
	principalCount := 0
	for _, entry := range s.previews {
		if entry.principal == principal {
			principalCount++
		}
	}
	if principalCount >= domain.MaxPreviewsPerSubject || len(s.previews) >= domain.MaxPreviewsPerService {
		return "", time.Time{}, errors.New("preview cache capacity reached")
	}
	var raw [32]byte
	if _, err := io.ReadFull(s.random, raw[:]); err != nil {
		return "", time.Time{}, fmt.Errorf("generate preview handle: %w", err)
	}
	handle := hex.EncodeToString(raw[:])
	if _, exists := s.previews[handle]; exists {
		return "", time.Time{}, errors.New("preview handle collision")
	}
	expiresAt := now.Add(domain.PreviewTTL)
	s.previews[handle] = &previewEntry{
		principal: principal, scope: s.Scope(), environmentRef: environmentRef,
		digest: snapshot.Digest, snapshot: snapshot, expiresAt: expiresAt,
	}
	return handle, expiresAt, nil
}

func (s *Service) putListCursor(principal string, kind domain.Kind, limit int, after domain.RecordRef) (string, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(now)
	principalCount := 0
	for _, cursor := range s.listCursors {
		if cursor.principal == principal {
			principalCount++
		}
	}
	if principalCount >= domain.MaxPreviewsPerSubject || len(s.listCursors) >= domain.MaxPreviewsPerService {
		return "", errors.New("list cursor capacity reached")
	}
	var raw [32]byte
	if _, err := io.ReadFull(s.random, raw[:]); err != nil {
		return "", fmt.Errorf("generate list cursor: %w", err)
	}
	handle := hex.EncodeToString(raw[:])
	if _, exists := s.listCursors[handle]; exists {
		return "", errors.New("list cursor collision")
	}
	s.listCursors[handle] = &listCursorEntry{
		principal: principal, scope: s.Scope(), kind: kind, limit: limit,
		after: after, expiresAt: now.Add(domain.PreviewTTL),
	}
	return handle, nil
}

func (s *Service) expireLocked(now time.Time) {
	for handle, entry := range s.previews {
		if !entry.saving && !now.Before(entry.expiresAt) {
			delete(s.previews, handle)
		}
	}
	for handle, cursor := range s.listCursors {
		if !cursor.inUse && !now.Before(cursor.expiresAt) {
			delete(s.listCursors, handle)
		}
	}
}
