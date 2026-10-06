// Package domainstore persists the immutable records used by private deployment
// inventory. The production filesystem implementation is intentionally Linux-only.
package domainstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domain"
)

const (
	storeSchema        = "private-deployment-store/v1"
	lockWait           = 5 * time.Second
	maxConnectionBytes = 64 << 10
	maxManifestBytes   = 16 << 10
)

type Code string

const (
	CodeInvalidArgument Code = "invalid_argument"
	CodeUnauthorized    Code = "unauthorized"
	CodeNotFound        Code = "not_found"
	CodeConflict        Code = "conflict"
	CodeBusy            Code = "busy"
	CodeStorage         Code = "storage_error"
	CodeCanceled        Code = "canceled"
	CodeOutcomeUnknown  Code = "outcome_unknown"
	CodeUnsupported     Code = "unsupported"
	CodeCorrupt         Code = "corrupt"
)

type Error struct {
	Code      Code
	Op        string
	Err       error
	Committed bool
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return fmt.Sprintf("%s: %s", e.Op, e.Code)
	}
	return fmt.Sprintf("%s: %s: %v", e.Op, e.Code, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

func IsCode(err error, code Code) bool {
	var target *Error
	return errors.As(err, &target) && target.Code == code
}

func storeError(code Code, op string, err error) error {
	if err == nil {
		err = errors.New(string(code))
	}
	return &Error{Code: code, Op: op, Err: err}
}

// Connection is the private, typed registration input. It is stored only in
// an Environment bundle and is never returned by record-oriented APIs.
type Connection struct {
	Runtime                    domain.Runtime       `json:"runtime"`
	Endpoint                   string               `json:"endpoint"`
	HostID                     string               `json:"hostID,omitempty"`
	ClusterID                  string               `json:"clusterID,omitempty"`
	ExpectedDaemonID           string               `json:"expectedDaemonID,omitempty"`
	ExpectedClusterFingerprint string               `json:"expectedClusterFingerprint,omitempty"`
	Namespaces                 []string             `json:"namespaces,omitempty"`
	NetworkPolicy              domain.NetworkPolicy `json:"networkPolicy"`
}

// DecodeConnection strictly decodes one registration input. Field names are
// case-sensitive and duplicate keys, invalid UTF-8, nulls, unknown fields,
// trailing values, and runtime-incompatible shapes are rejected.
func DecodeConnection(data []byte) (Connection, error) {
	if len(data) > maxConnectionBytes {
		return Connection{}, errors.New("connection size limit exceeded")
	}
	var connection Connection
	if err := decodeStrict(data, &connection); err != nil {
		return Connection{}, err
	}
	if err := validateConnection(connection); err != nil {
		return Connection{}, err
	}
	return connection, nil
}

type RecordStatus string

const (
	StatusOK      RecordStatus = "ok"
	StatusCorrupt RecordStatus = "corrupt"
)

type ListOptions struct {
	Kind  domain.Kind
	Limit int
	After *domain.RecordRef
}

type ListPage struct {
	Entries []ListEntry       `json:"records"`
	HasMore bool              `json:"hasMore"`
	NextRef *domain.RecordRef `json:"-"`
}

type ListEntry struct {
	Reference domain.RecordRef `json:"reference"`
	Digest    string           `json:"digest,omitempty"`
	CreatedAt string           `json:"createdAt,omitempty"`
	Status    RecordStatus     `json:"status"`
	Problem   string           `json:"problem,omitempty"`
}

type scopeFile struct {
	SchemaVersion string `json:"schemaVersion"`
	Scope         string `json:"scope"`
	OwnerUID      int    `json:"ownerUID"`
}

type manifest struct {
	SchemaVersion string         `json:"schemaVersion"`
	Files         []manifestFile `json:"files"`
}

type manifestFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

type Store struct {
	root     string
	scope    string
	ownerUID int
	ops      platformOps
	now      func() time.Time
	random   io.Reader
}

func Init(stateDir, scope string, ownerUID int) (*Store, error) {
	if ownerUID != os.Geteuid() {
		return nil, storeError(CodeUnauthorized, "init", errors.New("owner UID must be the effective process UID"))
	}
	return initWithOps(stateDir, scope, ownerUID, productionOps())
}

func Open(stateDir string, ownerUID int) (*Store, error) {
	if ownerUID != os.Geteuid() {
		return nil, storeError(CodeUnauthorized, "open", errors.New("owner UID must be the effective process UID"))
	}
	return openWithOps(stateDir, ownerUID, productionOps())
}

func initWithOps(stateDir, scope string, ownerUID int, ops platformOps) (*Store, error) {
	if strings.TrimSpace(scope) == "" || len(scope) > domain.MaxStringBytes {
		return nil, storeError(CodeInvalidArgument, "init", errors.New("scope is required"))
	}
	root, err := cleanAbsolute(stateDir)
	if err != nil {
		return nil, storeError(CodeInvalidArgument, "init", err)
	}
	if ownerUID < 0 {
		return nil, storeError(CodeInvalidArgument, "init", errors.New("owner UID is invalid"))
	}
	if err = ops.CheckFilesystem(filepath.Dir(root)); err != nil {
		return nil, classifyPlatform("init", err)
	}
	if err = createStateRoot(root, ownerUID); err != nil {
		return nil, err
	}
	if err = ops.CheckFilesystem(root); err != nil {
		return nil, classifyPlatform("init", err)
	}
	s := &Store{root: root, scope: scope, ownerUID: ownerUID, ops: ops, now: time.Now, random: rand.Reader}
	if err = s.initializeScope(); err != nil {
		return nil, err
	}
	if err = s.ensureLayout(); err != nil {
		return nil, err
	}
	if err = verifyTree(root, ownerUID); err != nil {
		return nil, storeError(CodeStorage, "init", err)
	}
	return s, nil
}

func openWithOps(stateDir string, ownerUID int, ops platformOps) (*Store, error) {
	root, err := cleanAbsolute(stateDir)
	if err != nil {
		return nil, storeError(CodeInvalidArgument, "open", err)
	}
	if ownerUID < 0 {
		return nil, storeError(CodeInvalidArgument, "open", errors.New("owner UID is invalid"))
	}
	if err = ops.CheckFilesystem(root); err != nil {
		return nil, classifyPlatform("open", err)
	}
	if err = verifyTree(root, ownerUID); err != nil {
		return nil, storeError(CodeStorage, "open", err)
	}
	for _, path := range []string{
		root,
		filepath.Join(root, "records"),
		filepath.Join(root, "records", string(domain.KindEnvironment)),
		filepath.Join(root, "records", string(domain.KindInventorySnapshot)),
	} {
		if err = verifyPrivateDir(path, ownerUID); err != nil {
			return nil, storeError(CodeCorrupt, "open", err)
		}
	}
	if _, err = readRegular(filepath.Join(root, ".writer.lock"), ownerUID, 0); err != nil {
		return nil, storeError(CodeCorrupt, "open", err)
	}
	b, err := readRegular(filepath.Join(root, "scope.json"), ownerUID, maxManifestBytes)
	if err != nil {
		return nil, storeError(CodeStorage, "open", err)
	}
	var sf scopeFile
	if err = decodeStrict(b, &sf); err != nil || sf.SchemaVersion != storeSchema || strings.TrimSpace(sf.Scope) == "" || sf.OwnerUID != ownerUID {
		if err == nil {
			err = errors.New("scope binding is invalid")
		}
		return nil, storeError(CodeCorrupt, "open", err)
	}
	return &Store{root: root, scope: sf.Scope, ownerUID: ownerUID, ops: ops, now: time.Now, random: rand.Reader}, nil
}

func (s *Store) Scope() string { return s.scope }

func (s *Store) Register(ctx context.Context, connection Connection) (*domain.Environment, error) {
	if err := s.authorize(s.scope); err != nil {
		return nil, err
	}
	if err := validateConnection(connection); err != nil {
		return nil, storeError(CodeInvalidArgument, "register", err)
	}
	id, err := newUUID(s.random)
	if err != nil {
		return nil, storeError(CodeStorage, "register", err)
	}
	env, err := s.environment(id, 1, connection)
	if err != nil {
		return nil, storeError(CodeInvalidArgument, "register", err)
	}
	if _, err = s.publish(ctx, env, &connection, 0); err != nil {
		return nil, err
	}
	return env, nil
}

func (s *Store) Update(ctx context.Context, id string, expectedRevision int64, connection Connection) (*domain.Environment, error) {
	if err := s.authorize(s.scope); err != nil {
		return nil, err
	}
	if !validUUID(id) || expectedRevision <= 0 || expectedRevision >= domain.MaxSafeInteger {
		return nil, storeError(CodeInvalidArgument, "update", errors.New("id or expected revision is invalid"))
	}
	if err := validateConnection(connection); err != nil {
		return nil, storeError(CodeInvalidArgument, "update", err)
	}
	env, err := s.environment(id, expectedRevision+1, connection)
	if err != nil {
		return nil, storeError(CodeInvalidArgument, "update", err)
	}
	if _, err = s.publish(ctx, env, &connection, expectedRevision); err != nil {
		return nil, err
	}
	return env, nil
}

func (s *Store) SaveSnapshot(ctx context.Context, principalScope string, snapshot *domain.InventorySnapshot) (domain.RecordRef, error) {
	if err := s.authorize(principalScope); err != nil {
		return domain.RecordRef{}, err
	}
	if snapshot == nil {
		return domain.RecordRef{}, storeError(CodeInvalidArgument, "save snapshot", errors.New("snapshot is nil"))
	}
	if err := domain.AuthorizeScope(principalScope, snapshot.TenantScope); err != nil {
		return domain.RecordRef{}, storeError(CodeUnauthorized, "save snapshot", err)
	}
	if snapshot.Digest == "" {
		if err := domain.NormalizeSnapshot(snapshot); err != nil {
			return domain.RecordRef{}, storeError(CodeInvalidArgument, "save snapshot", err)
		}
		digest, err := domain.ComputeDigest(snapshot)
		if err != nil {
			return domain.RecordRef{}, storeError(CodeInvalidArgument, "save snapshot", err)
		}
		snapshot.Digest = digest
	}
	if err := domain.VerifyRecord(snapshot); err != nil {
		return domain.RecordRef{}, storeError(CodeInvalidArgument, "save snapshot", err)
	}
	return s.publish(ctx, snapshot, nil, 0)
}

func (s *Store) Get(ctx context.Context, principalScope string, ref domain.RecordRef) (domain.Record, error) {
	if err := s.authorize(principalScope); err != nil {
		return nil, err
	}
	if err := domain.AuthorizeScope(principalScope, ref.TenantScope); err != nil {
		return nil, storeError(CodeUnauthorized, "get", err)
	}
	if err := validateRef(ref); err != nil {
		return nil, storeError(CodeInvalidArgument, "get", err)
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	record, _, err := s.readBundle(ref)
	return record, err
}

func (s *Store) Verify(ctx context.Context, principalScope string, ref domain.RecordRef) (domain.Record, error) {
	return s.Get(ctx, principalScope, ref)
}

// CurrentEnvironment resolves and verifies the latest immutable revision for
// an Environment ID. The caller's scope is checked before the filesystem is
// consulted, so it is safe for revision gates in discovery and preview flows.
func (s *Store) CurrentEnvironment(ctx context.Context, principalScope, id string) (*domain.Environment, error) {
	if err := s.authorize(principalScope); err != nil {
		return nil, err
	}
	if !validUUID(id) {
		return nil, storeError(CodeInvalidArgument, "current environment", errors.New("environment ID is invalid"))
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	ref, found, err := s.latestEnvironment(id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, storeError(CodeNotFound, "current environment", errors.New("Environment was not found"))
	}
	record, _, err := s.readBundle(ref)
	if err != nil {
		return nil, err
	}
	environment, ok := record.(*domain.Environment)
	if !ok {
		return nil, storeError(CodeCorrupt, "current environment", errors.New("latest Environment path contains another kind"))
	}
	return environment, nil
}

func (s *Store) Connection(ctx context.Context, principalScope string, environmentRef domain.RecordRef) (Connection, error) {
	if err := s.authorize(principalScope); err != nil {
		return Connection{}, err
	}
	if err := domain.AuthorizeScope(principalScope, environmentRef.TenantScope); err != nil {
		return Connection{}, storeError(CodeUnauthorized, "connection", err)
	}
	if environmentRef.Kind != domain.KindEnvironment {
		return Connection{}, storeError(CodeInvalidArgument, "connection", errors.New("an Environment reference is required"))
	}
	if err := validateRef(environmentRef); err != nil {
		return Connection{}, storeError(CodeInvalidArgument, "connection", err)
	}
	if err := contextErr(ctx); err != nil {
		return Connection{}, err
	}
	_, connection, err := s.readBundle(environmentRef)
	if err != nil {
		return Connection{}, err
	}
	if connection == nil {
		return Connection{}, storeError(CodeCorrupt, "connection", errors.New("connection is missing"))
	}
	return *connection, nil
}

func (s *Store) List(ctx context.Context, principalScope string, options ListOptions) ([]ListEntry, error) {
	page, err := s.ListPage(ctx, principalScope, options)
	return page.Entries, err
}

// ListPage returns one stable lexical page over the records visible during
// this call. NextRef is intended for a server-side opaque cursor and must not
// be exposed as a client-selected authorization or integrity reference.
func (s *Store) ListPage(ctx context.Context, principalScope string, options ListOptions) (ListPage, error) {
	if err := s.authorize(principalScope); err != nil {
		return ListPage{}, err
	}
	var after domain.RecordRef
	hasAfter := options.After != nil
	if hasAfter {
		after = *options.After
	}
	if options.Kind != "" && options.Kind != domain.KindEnvironment && options.Kind != domain.KindInventorySnapshot {
		return ListPage{}, storeError(CodeInvalidArgument, "list", errors.New("record kind is invalid"))
	}
	if options.Limit == 0 {
		options.Limit = domain.DefaultPageSize
	}
	if options.Limit < 1 || options.Limit > domain.MaxPageSize {
		return ListPage{}, storeError(CodeInvalidArgument, "list", errors.New("limit is outside 1..100"))
	}
	if hasAfter {
		if err := domain.AuthorizeScope(principalScope, after.TenantScope); err != nil {
			return ListPage{}, storeError(CodeUnauthorized, "list", err)
		}
		if err := validateRef(after); err != nil {
			return ListPage{}, storeError(CodeInvalidArgument, "list", errors.New("after reference is invalid"))
		}
		if options.Kind != "" && after.Kind != options.Kind {
			return ListPage{}, storeError(CodeInvalidArgument, "list", errors.New("after reference does not match the kind filter"))
		}
	}
	if err := contextErr(ctx); err != nil {
		return ListPage{}, err
	}
	refs, err := s.scanRefs(options.Kind)
	if err != nil {
		return ListPage{}, err
	}
	start := 0
	if hasAfter {
		found := false
		for i := range refs {
			if refs[i] == after {
				start, found = i+1, true
				break
			}
		}
		if !found {
			return ListPage{}, storeError(CodeInvalidArgument, "list", errors.New("after reference is not present"))
		}
	}
	end := min(start+options.Limit, len(refs))
	page := ListPage{Entries: make([]ListEntry, 0, end-start), HasMore: end < len(refs)}
	for _, ref := range refs[start:end] {
		if err = contextErr(ctx); err != nil {
			return ListPage{}, err
		}
		record, _, readErr := s.readBundle(ref)
		if readErr != nil {
			page.Entries = append(page.Entries, ListEntry{Reference: ref, Status: StatusCorrupt, Problem: "bundle integrity verification failed"})
			continue
		}
		entry := ListEntry{Reference: record.Reference(), Status: StatusOK}
		switch value := record.(type) {
		case *domain.Environment:
			entry.Digest, entry.CreatedAt = value.Digest, value.CreatedAt
		case *domain.InventorySnapshot:
			entry.Digest, entry.CreatedAt = value.Digest, value.CreatedAt
		}
		page.Entries = append(page.Entries, entry)
	}
	if page.HasMore && end > start {
		next := refs[end-1]
		page.NextRef = &next
	}
	return page, nil
}

func (s *Store) authorize(principalScope string) error {
	if err := domain.AuthorizeScope(principalScope, s.scope); err != nil {
		return storeError(CodeUnauthorized, "authorize", err)
	}
	return nil
}

func (s *Store) environment(id string, revision int64, connection Connection) (*domain.Environment, error) {
	fingerprint, err := connectionFingerprint(connection)
	if err != nil {
		return nil, err
	}
	namespaces := append([]string(nil), connection.Namespaces...)
	if namespaces == nil {
		namespaces = []string{}
	}
	env := &domain.Environment{
		SchemaVersion: domain.SchemaVersion,
		Kind:          domain.KindEnvironment,
		TenantScope:   s.scope,
		ID:            id,
		Revision:      revision,
		CreatedAt:     domain.FormatTimestamp(s.now()),
		Spec: domain.EnvironmentSpec{
			Runtime: connection.Runtime, EndpointRef: domain.LocalRef{ID: id},
			IdentityFingerprint: fingerprint, HostID: connection.HostID, ClusterID: connection.ClusterID,
			AllowedNamespaces: namespaces, NetworkPolicy: connection.NetworkPolicy, Enabled: true,
		},
	}
	if connection.Runtime == domain.RuntimeKubernetes {
		env.Spec.CredentialRef = &domain.LocalRef{ID: id}
	}
	if err = domain.FinalizeRecord(env); err != nil {
		return nil, err
	}
	return env, nil
}

func validateConnection(c Connection) error {
	if c.NetworkPolicy != domain.NetworkOffline && c.NetworkPolicy != domain.NetworkApprovedOnline {
		return errors.New("networkPolicy is invalid")
	}
	if c.Endpoint == "" || domain.ContainsSecretMaterial(c.Endpoint) {
		return errors.New("endpoint is missing or contains secret material")
	}
	switch c.Runtime {
	case domain.RuntimeDocker:
		if !filepath.IsAbs(c.Endpoint) || filepath.Clean(c.Endpoint) != c.Endpoint || strings.Contains(c.Endpoint, "://") {
			return errors.New("Docker endpoint must be an absolute Unix socket path")
		}
		if c.HostID == "" || c.ExpectedDaemonID == "" || c.ClusterID != "" || c.ExpectedClusterFingerprint != "" || len(c.Namespaces) != 0 {
			return errors.New("Docker connection fields are invalid")
		}
	case domain.RuntimeKubernetes:
		if c.Endpoint != "existing-default" && (!filepath.IsAbs(c.Endpoint) || filepath.Clean(c.Endpoint) != c.Endpoint) {
			return errors.New("Kubernetes endpoint must be an absolute kubeconfig path or existing-default")
		}
		if c.ClusterID == "" || !digestPattern.MatchString(c.ExpectedClusterFingerprint) || c.HostID != "" || c.ExpectedDaemonID != "" || len(c.Namespaces) == 0 {
			return errors.New("Kubernetes connection fields are invalid")
		}
		seen := map[string]bool{}
		for _, namespace := range c.Namespaces {
			if namespace == "" || len(namespace) > domain.MaxStringBytes || domain.ContainsSecretMaterial(namespace) || seen[namespace] {
				return errors.New("namespaces contain an empty or duplicate value")
			}
			seen[namespace] = true
		}
	default:
		return errors.New("runtime is invalid")
	}
	for _, value := range []string{c.Endpoint, c.HostID, c.ClusterID, c.ExpectedDaemonID, c.ExpectedClusterFingerprint} {
		if len(value) > domain.MaxStringBytes || domain.ContainsSecretMaterial(value) {
			return errors.New("connection string limit exceeded")
		}
	}
	return nil
}

func connectionFingerprint(c Connection) (string, error) {
	var value any
	if c.Runtime == domain.RuntimeDocker {
		value = struct {
			Runtime  domain.Runtime `json:"runtime"`
			HostID   string         `json:"hostID"`
			DaemonID string         `json:"daemonID"`
		}{c.Runtime, c.HostID, c.ExpectedDaemonID}
	} else {
		value = struct {
			Runtime            domain.Runtime `json:"runtime"`
			ClusterID          string         `json:"clusterID"`
			ClusterFingerprint string         `json:"clusterFingerprint"`
		}{c.Runtime, c.ClusterID, c.ExpectedClusterFingerprint}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	canonical, err := domain.CanonicalizeJSON(raw, domain.CanonicalOptions{})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return domain.DigestPrefix + hex.EncodeToString(sum[:]), nil
}

func decodeStrict(data []byte, target any) error {
	value := reflect.ValueOf(target)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return errors.New("strict JSON target must be a non-nil pointer")
	}
	canonical, err := domain.CanonicalizeJSON(data, domain.CanonicalOptions{})
	if err != nil {
		return err
	}
	if err = validateExactJSONShape(canonical, value.Type().Elem(), "value"); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

func validateExactJSONShape(raw json.RawMessage, target reflect.Type, path string) error {
	if bytes.Equal(raw, []byte("null")) {
		return fmt.Errorf("%s must not be null", path)
	}
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	switch target.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil || object == nil {
			if err == nil {
				err = errors.New("expected JSON object")
			}
			return fmt.Errorf("%s: %w", path, err)
		}
		type fieldShape struct {
			typeOf   reflect.Type
			optional bool
		}
		fields := make(map[string]fieldShape, target.NumField())
		for i := 0; i < target.NumField(); i++ {
			field := target.Field(i)
			if !field.IsExported() {
				continue
			}
			tag := strings.Split(field.Tag.Get("json"), ",")
			if len(tag) > 0 && tag[0] == "-" {
				continue
			}
			name := field.Name
			if len(tag) > 0 && tag[0] != "" {
				name = tag[0]
			}
			optional := false
			for _, option := range tag[1:] {
				optional = optional || option == "omitempty"
			}
			fields[name] = fieldShape{typeOf: field.Type, optional: optional}
		}
		for name, child := range object {
			shape, ok := fields[name]
			if !ok {
				return fmt.Errorf("%s contains unknown field %q", path, name)
			}
			if err := validateExactJSONShape(child, shape.typeOf, path+"."+name); err != nil {
				return err
			}
		}
		for name, shape := range fields {
			if _, ok := object[name]; !ok && !shape.optional {
				return fmt.Errorf("%s.%s is required", path, name)
			}
		}
		return nil
	case reflect.Slice, reflect.Array:
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil || items == nil {
			if err == nil {
				err = errors.New("expected JSON array")
			}
			return fmt.Errorf("%s: %w", path, err)
		}
		for i, item := range items {
			if err := validateExactJSONShape(item, target.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		return nil
	default:
		decoded := reflect.New(target).Interface()
		if err := json.Unmarshal(raw, decoded); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		return nil
	}
}

func newUUID(source io.Reader) (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(source, b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func contextErr(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return storeError(CodeCanceled, "context", err)
	}
	return nil
}

func classifyPlatform(op string, err error) error {
	if errors.Is(err, errUnsupported) {
		return storeError(CodeUnsupported, op, err)
	}
	return storeError(CodeStorage, op, err)
}

func sortRefs(refs []domain.RecordRef) {
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Kind != refs[j].Kind {
			return refs[i].Kind < refs[j].Kind
		}
		if refs[i].ID != refs[j].ID {
			return refs[i].ID < refs[j].ID
		}
		return refs[i].Revision > refs[j].Revision
	})
}
