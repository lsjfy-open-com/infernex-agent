package privateinventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domain"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domainstore"
)

type fakeStore struct {
	mu          sync.Mutex
	scope       string
	environment *domain.Environment
	connection  domainstore.Connection
	saved       []*domain.InventorySnapshot
	listEntries []domainstore.ListEntry
	listStarted chan struct{}
	listRelease chan struct{}
}

func (s *fakeStore) Scope() string { return s.scope }
func (s *fakeStore) Get(context.Context, string, domain.RecordRef) (domain.Record, error) {
	return nil, errors.New("not implemented")
}
func (s *fakeStore) Verify(context.Context, string, domain.RecordRef) (domain.Record, error) {
	return nil, errors.New("not implemented")
}
func (s *fakeStore) List(context.Context, string, domainstore.ListOptions) ([]domainstore.ListEntry, error) {
	return nil, nil
}
func (s *fakeStore) ListPage(_ context.Context, scope string, options domainstore.ListOptions) (domainstore.ListPage, error) {
	if scope != s.scope {
		return domainstore.ListPage{}, errors.New("list scope mismatch")
	}
	start := 0
	if options.After != nil {
		if s.listStarted != nil {
			select {
			case s.listStarted <- struct{}{}:
			default:
			}
			<-s.listRelease
		}
		start = -1
		for index := range s.listEntries {
			if s.listEntries[index].Reference == *options.After {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return domainstore.ListPage{}, errors.New("after not found")
		}
	}
	end := start + options.Limit
	if end > len(s.listEntries) {
		end = len(s.listEntries)
	}
	page := domainstore.ListPage{Entries: append([]domainstore.ListEntry(nil), s.listEntries[start:end]...), HasMore: end < len(s.listEntries)}
	if page.HasMore && end > start {
		ref := s.listEntries[end-1].Reference
		page.NextRef = &ref
	}
	return page, nil
}
func (s *fakeStore) Connection(_ context.Context, scope string, ref domain.RecordRef) (domainstore.Connection, error) {
	if scope != s.scope || s.environment == nil || ref != s.environment.Reference() {
		return domainstore.Connection{}, errors.New("connection scope mismatch")
	}
	return s.connection, nil
}
func (s *fakeStore) CurrentEnvironment(_ context.Context, scope, id string) (*domain.Environment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if scope != s.scope || s.environment == nil || id != s.environment.ID {
		return nil, errors.New("environment not found")
	}
	copy := *s.environment
	return &copy, nil
}
func (s *fakeStore) SaveSnapshot(_ context.Context, scope string, snapshot *domain.InventorySnapshot) (domain.RecordRef, error) {
	if scope != s.scope {
		return domain.RecordRef{}, errors.New("save scope mismatch")
	}
	if err := domain.VerifyRecord(snapshot); err != nil {
		return domain.RecordRef{}, err
	}
	copy, err := cloneSnapshot(snapshot)
	if err != nil {
		return domain.RecordRef{}, err
	}
	s.mu.Lock()
	s.saved = append(s.saved, copy)
	s.mu.Unlock()
	return snapshot.Reference(), nil
}

func TestPreviewHandleSaveBindingTTLRevisionAndClone(t *testing.T) {
	environment := fixtureEnvironment(t)
	store := &fakeStore{
		scope: environment.TenantScope, environment: environment,
		connection: domainstore.Connection{Runtime: domain.RuntimeKubernetes},
	}
	now := time.Date(2026, 10, 6, 1, 2, 3, 0, time.UTC)
	discoverCalls := 0
	service, err := New(store, WithKubernetesDiscover(func(context.Context, domain.Environment, domainstore.Connection, DiscoverRequest) (*domain.InventorySnapshot, string, error) {
		discoverCalls++
		return fixtureSnapshot(t), "next-page", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }

	preview, err := service.Discover(context.Background(), DiscoverRequest{
		Principal: "uid:1000", EnvironmentID: environment.ID, Revision: environment.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if discoverCalls != 1 || preview.Preview.CursorHandle != "next-page" || len(service.previews) != 1 {
		t.Fatalf("unexpected preview: calls=%d preview=%+v cache=%d", discoverCalls, preview.Preview, len(service.previews))
	}
	preview.Snapshot.Digest = "sha256:" + string(make([]byte, 64))
	if _, err := service.Record(context.Background(), "uid:1001", preview.Preview.Handle, preview.Preview.Digest); err == nil {
		t.Fatal("different principal saved preview")
	}
	if _, err := service.Record(context.Background(), "uid:1000", preview.Preview.Handle, "sha256:wrong"); err == nil {
		t.Fatal("different digest saved preview")
	}
	firstRef, err := service.Record(context.Background(), "uid:1000", preview.Preview.Handle, preview.Preview.Digest)
	if err != nil {
		t.Fatalf("record cloned preview: %v", err)
	}
	secondRef, err := service.Record(context.Background(), "uid:1000", preview.Preview.Handle, preview.Preview.Digest)
	if err != nil || secondRef != firstRef {
		t.Fatalf("idempotent replay = %v, %v; want %v", secondRef, err, firstRef)
	}

	other, err := service.Discover(context.Background(), DiscoverRequest{Principal: "uid:1000", EnvironmentID: environment.ID, Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.environment.Revision = 2
	store.mu.Unlock()
	if _, err := service.Record(context.Background(), "uid:1000", other.Preview.Handle, other.Preview.Digest); err == nil {
		t.Fatal("changed Environment revision saved preview")
	}
	store.mu.Lock()
	store.environment.Revision = 1
	store.mu.Unlock()

	expired, err := service.Discover(context.Background(), DiscoverRequest{Principal: "uid:1002", EnvironmentID: environment.ID, Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(domain.PreviewTTL)
	if _, err := service.Record(context.Background(), "uid:1002", expired.Preview.Handle, expired.Preview.Digest); err == nil {
		t.Fatal("expired preview saved")
	}
}

func TestDiscoverRejectsStaleRevisionBeforeRuntime(t *testing.T) {
	environment := fixtureEnvironment(t)
	store := &fakeStore{scope: environment.TenantScope, environment: environment, connection: domainstore.Connection{Runtime: domain.RuntimeKubernetes}}
	calls := 0
	service, err := New(store, WithKubernetesDiscover(func(context.Context, domain.Environment, domainstore.Connection, DiscoverRequest) (*domain.InventorySnapshot, string, error) {
		calls++
		return fixtureSnapshot(t), "", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Discover(context.Background(), DiscoverRequest{Principal: "uid:1000", EnvironmentID: environment.ID, Revision: 2})
	if err == nil || calls != 0 {
		t.Fatalf("stale discover err=%v calls=%d", err, calls)
	}
}

func TestDiscoverRejectsDisabledAndOutOfScopeBeforeRuntime(t *testing.T) {
	environment := fixtureEnvironment(t)
	store := &fakeStore{scope: environment.TenantScope, environment: environment, connection: domainstore.Connection{Runtime: domain.RuntimeKubernetes}}
	calls := 0
	service, err := New(store, WithKubernetesDiscover(func(context.Context, domain.Environment, domainstore.Connection, DiscoverRequest) (*domain.InventorySnapshot, string, error) {
		calls++
		return fixtureSnapshot(t), "", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	request := DiscoverRequest{Principal: "uid:1000", EnvironmentID: environment.ID, Revision: 1, Namespaces: []string{"outside"}}
	if _, err := service.Discover(context.Background(), request); err == nil || calls != 0 {
		t.Fatalf("out-of-scope discover err=%v calls=%d", err, calls)
	}
	store.mu.Lock()
	store.environment.Spec.Enabled = false
	store.mu.Unlock()
	request.Namespaces = nil
	if _, err := service.Discover(context.Background(), request); err == nil || calls != 0 {
		t.Fatalf("disabled discover err=%v calls=%d", err, calls)
	}
}

func TestPreviewCacheLimitsAndNoUnsafeEviction(t *testing.T) {
	store := &fakeStore{scope: "synthetic-scope"}
	service, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 6, 1, 2, 3, 0, time.UTC)
	service.now = func() time.Time { return now }
	snapshot := fixtureSnapshot(t)
	environmentRef := snapshot.Spec.EnvironmentRef

	for i := 0; i < domain.MaxPreviewsPerSubject; i++ {
		if _, _, err := service.putPreview("principal-a", environmentRef, snapshot); err != nil {
			t.Fatalf("subject preview %d: %v", i+1, err)
		}
	}
	if _, _, err := service.putPreview("principal-a", environmentRef, snapshot); err == nil {
		t.Fatal("fifth subject preview accepted")
	}
	for i := domain.MaxPreviewsPerSubject; i < domain.MaxPreviewsPerService; i++ {
		if _, _, err := service.putPreview("principal-"+time.Duration(i).String(), environmentRef, snapshot); err != nil {
			t.Fatalf("service preview %d: %v", i+1, err)
		}
	}
	if _, _, err := service.putPreview("overflow", environmentRef, snapshot); err == nil {
		t.Fatal("seventeenth service preview accepted")
	}

	var savingHandle string
	service.mu.Lock()
	for handle, entry := range service.previews {
		entry.saving = true
		savingHandle = handle
		break
	}
	service.mu.Unlock()
	now = now.Add(domain.PreviewTTL)
	if _, _, err := service.putPreview("after-expiry", environmentRef, snapshot); err != nil {
		t.Fatalf("expired slots were not reclaimed: %v", err)
	}
	service.mu.Lock()
	_, retained := service.previews[savingHandle]
	service.mu.Unlock()
	if !retained {
		t.Fatal("preview being saved was evicted")
	}
}

func TestRecordListCursorIsOpaqueBoundAndExpires(t *testing.T) {
	entries := make([]domainstore.ListEntry, 3)
	for index := range entries {
		entries[index] = domainstore.ListEntry{Reference: domain.RecordRef{
			TenantScope: "synthetic-scope", Kind: domain.KindEnvironment,
			ID: "11111111-1111-4111-8111-11111111111" + string(rune('1'+index)), Revision: 1,
		}, Status: domainstore.StatusOK}
	}
	store := &fakeStore{scope: "synthetic-scope", listEntries: entries}
	service, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 6, 1, 2, 3, 0, time.UTC)
	service.now = func() time.Time { return now }
	first, err := service.ListPage(context.Background(), ListRequest{Principal: "uid:1000", Kind: domain.KindEnvironment, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Entries) != 2 || len(first.CursorHandle) != 64 || first.CursorHandle == entries[1].Reference.ID {
		t.Fatalf("first page = %+v", first)
	}
	if _, err := service.ListPage(context.Background(), ListRequest{Principal: "uid:other", CursorHandle: first.CursorHandle}); err == nil {
		t.Fatal("cursor accepted for another principal")
	}
	if _, err := service.ListPage(context.Background(), ListRequest{Principal: "uid:1000", Kind: domain.KindEnvironment, CursorHandle: first.CursorHandle}); err == nil {
		t.Fatal("cursor accepted with replacement filters")
	}
	second, err := service.ListPage(context.Background(), ListRequest{Principal: "uid:1000", CursorHandle: first.CursorHandle})
	if err != nil || len(second.Entries) != 1 || second.CursorHandle != "" {
		t.Fatalf("second page = %+v, %v", second, err)
	}
	if _, err := service.ListPage(context.Background(), ListRequest{Principal: "uid:1000", CursorHandle: first.CursorHandle}); err == nil {
		t.Fatal("consumed cursor was reusable")
	}

	expiring, err := service.ListPage(context.Background(), ListRequest{Principal: "uid:1000", Kind: domain.KindEnvironment, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(domain.PreviewTTL)
	if _, err := service.ListPage(context.Background(), ListRequest{Principal: "uid:1000", CursorHandle: expiring.CursorHandle}); err == nil {
		t.Fatal("expired cursor was accepted")
	}
}

func TestRecordListCursorRejectsConcurrentUse(t *testing.T) {
	entries := []domainstore.ListEntry{
		{Reference: domain.RecordRef{TenantScope: "synthetic-scope", Kind: domain.KindEnvironment, ID: "11111111-1111-4111-8111-111111111111", Revision: 1}, Status: domainstore.StatusOK},
		{Reference: domain.RecordRef{TenantScope: "synthetic-scope", Kind: domain.KindEnvironment, ID: "11111111-1111-4111-8111-111111111112", Revision: 1}, Status: domainstore.StatusOK},
	}
	store := &fakeStore{scope: "synthetic-scope", listEntries: entries}
	service, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.ListPage(context.Background(), ListRequest{Principal: "uid:1000", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	store.listStarted = make(chan struct{}, 1)
	store.listRelease = make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		_, continueErr := service.ListPage(context.Background(), ListRequest{Principal: "uid:1000", CursorHandle: first.CursorHandle})
		finished <- continueErr
	}()
	<-store.listStarted
	if _, err := service.ListPage(context.Background(), ListRequest{Principal: "uid:1000", CursorHandle: first.CursorHandle}); err == nil {
		t.Fatal("cursor accepted concurrently")
	}
	close(store.listRelease)
	if err := <-finished; err != nil {
		t.Fatalf("claimed cursor failed: %v", err)
	}
}

func TestRecordListCursorLimitsAndResponseBudget(t *testing.T) {
	entries := make([]domainstore.ListEntry, domain.MaxPageSize*3)
	for index := range entries {
		entries[index] = domainstore.ListEntry{
			Reference: domain.RecordRef{TenantScope: "synthetic-scope", Kind: domain.KindEnvironment, ID: fmt.Sprintf("11111111-1111-4111-8111-%012d", index), Revision: 1},
			Status:    domainstore.StatusCorrupt, Problem: strings.Repeat("x", domain.MaxStringBytes),
		}
	}
	store := &fakeStore{scope: "synthetic-scope", listEntries: entries}
	service, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	handles := make([]string, 0, domain.MaxPreviewsPerSubject)
	for index := 0; index < domain.MaxPreviewsPerSubject; index++ {
		page, err := service.ListPage(context.Background(), ListRequest{Principal: "principal-a", Limit: domain.MaxPageSize})
		if err != nil {
			t.Fatalf("subject cursor %d: %v", index+1, err)
		}
		handles = append(handles, page.CursorHandle)
	}
	replacement, err := service.ListPage(context.Background(), ListRequest{Principal: "principal-a", CursorHandle: handles[0]})
	if err != nil || replacement.CursorHandle == "" {
		t.Fatalf("cursor replacement at capacity = %+v, %v", replacement, err)
	}
	if _, err := service.ListPage(context.Background(), ListRequest{Principal: "principal-a", Limit: 1}); err == nil {
		t.Fatal("fifth subject cursor accepted")
	}
	for index := domain.MaxPreviewsPerSubject; index < domain.MaxPreviewsPerService; index++ {
		if _, err := service.ListPage(context.Background(), ListRequest{Principal: fmt.Sprintf("principal-%d", index), Limit: 1}); err != nil {
			t.Fatalf("service cursor %d: %v", index+1, err)
		}
	}
	if _, err := service.ListPage(context.Background(), ListRequest{Principal: "overflow", Limit: 1}); err == nil {
		t.Fatal("seventeenth service cursor accepted")
	}

	boundedService, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	page, err := boundedService.ListPage(context.Background(), ListRequest{Principal: "bounded", Limit: domain.MaxPageSize})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(struct {
		Records      []domainstore.ListEntry `json:"records"`
		Returned     int                     `json:"returned"`
		Truncated    bool                    `json:"truncated"`
		CursorHandle string                  `json:"cursorHandle,omitempty"`
	}{page.Entries, len(page.Entries), page.Truncated, page.CursorHandle})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > domain.MaxToolResponseBytes || len(page.Entries) >= domain.MaxPageSize || page.CursorHandle == "" {
		t.Fatalf("bounded page bytes=%d entries=%d cursor=%q", len(raw), len(page.Entries), page.CursorHandle)
	}
}

func fixtureEnvironment(t *testing.T) *domain.Environment {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "domain", "testdata", "schema-v1-environment-k8s.json"))
	if err != nil {
		t.Fatal(err)
	}
	environment, err := domain.DecodeEnvironment(raw)
	if err != nil {
		t.Fatal(err)
	}
	return environment
}

func fixtureSnapshot(t *testing.T) *domain.InventorySnapshot {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "domain", "testdata", "schema-v1-snapshot-k8s-aggregate.json"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := domain.DecodeInventorySnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
