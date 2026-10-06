package domainstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domain"
)

// simulatedOps exists only to exercise transaction outcomes on non-Linux test
// hosts. Its rename implementation is deliberately not used by production.
type simulatedOps struct {
	lock            chan struct{}
	renameErr       error
	failParentSync  bool
	failPendingSync bool
	failWriteBase   string
}

func newSimulatedOps() *simulatedOps {
	lock := make(chan struct{}, 1)
	lock <- struct{}{}
	return &simulatedOps{lock: lock}
}

func (*simulatedOps) CheckFilesystem(string) error { return nil }
func (o *simulatedOps) Lock(ctx context.Context, _ *os.File, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-o.lock:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errLockBusy
	}
}
func (o *simulatedOps) Unlock(*os.File) error {
	o.lock <- struct{}{}
	return nil
}
func (o *simulatedOps) WriteFile(path string, contents []byte, ownerUID int) error {
	if filepath.Base(path) == o.failWriteBase {
		return errors.New("injected file write failure")
	}
	return writeNewFile(path, contents, ownerUID)
}
func (o *simulatedOps) RenameNoReplace(oldPath, newPath string) error {
	if o.renameErr != nil {
		return o.renameErr
	}
	if _, err := os.Lstat(newPath); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(oldPath, newPath)
}
func (o *simulatedOps) SyncDir(path string) error {
	if o.failPendingSync && strings.Contains(filepath.Base(path), ".domain-pending-") {
		return errors.New("injected pending sync failure")
	}
	if o.failParentSync && validUUID(filepath.Base(path)) {
		return errors.New("injected parent sync failure")
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	err = dir.Sync()
	closeErr := dir.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func TestDockerFingerprintVector(t *testing.T) {
	got, err := connectionFingerprint(dockerConnection())
	if err != nil {
		t.Fatal(err)
	}
	const want = "sha256:eb8e33b33469d9bb84e77aef599648ee9ed2e78f362c761272a86fac7fb0f5f9"
	if got != want {
		t.Fatalf("Docker identity fingerprint = %s, want %s", got, want)
	}
}

func newTestStore(t *testing.T, ops *simulatedOps) (*Store, string) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "state")
	store, err := initWithOps(root, "tenant-a", os.Geteuid(), ops)
	if err != nil {
		t.Fatal(err)
	}
	return store, root
}

func dockerConnection() Connection {
	return Connection{
		Runtime: domain.RuntimeDocker, Endpoint: "/run/infernex-test/docker.sock",
		HostID: "host-a", ExpectedDaemonID: "daemon-a", NetworkPolicy: domain.NetworkOffline,
	}
}

func testSnapshot(env *domain.Environment, id string) *domain.InventorySnapshot {
	timestamp := "2026-10-02T01:02:03.000000000Z"
	return &domain.InventorySnapshot{
		SchemaVersion: domain.SchemaVersion, Kind: domain.KindInventorySnapshot,
		TenantScope: env.TenantScope, ID: id, Revision: 1, CreatedAt: timestamp,
		Spec: domain.SnapshotSpec{
			EnvironmentRef: env.Reference(), CollectorVersion: "test-collector/v1", RulesetVersion: "test-rules/v1",
			StartedAt: timestamp, FinishedAt: timestamp,
			Coverage: []domain.Coverage{{ResourceKind: "containers", Count: 0, State: domain.CoverageComplete}},
			Entities: []domain.Entity{}, Relations: []domain.Relation{}, Issues: []domain.Issue{},
			Completeness: domain.CompletenessComplete,
		},
	}
}

func TestStorePublishVerifyRestartAndPrivateConnection(t *testing.T) {
	ops := newSimulatedOps()
	store, root := newTestStore(t, ops)
	env, err := store.Register(context.Background(), dockerConnection())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := testSnapshot(env, "22222222-2222-4222-8222-222222222222")
	ref, err := store.SaveSnapshot(context.Background(), "tenant-a", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if ref != snapshot.Reference() || snapshot.Digest == "" {
		t.Fatalf("unexpected saved snapshot: %+v", ref)
	}

	restarted, err := openWithOps(root, os.Geteuid(), ops)
	if err != nil {
		t.Fatal(err)
	}
	record, err := restarted.Get(context.Background(), "tenant-a", ref)
	if err != nil {
		t.Fatal(err)
	}
	got := record.(*domain.InventorySnapshot)
	if got.Digest != snapshot.Digest {
		t.Fatalf("digest changed across restart: %s != %s", got.Digest, snapshot.Digest)
	}
	connection, err := restarted.Connection(context.Background(), "tenant-a", env.Reference())
	if err != nil || connection.Endpoint != dockerConnection().Endpoint {
		t.Fatalf("private connection lookup failed: %+v, %v", connection, err)
	}
	entries, err := restarted.List(context.Background(), "tenant-a", ListOptions{Limit: 100})
	if err != nil || len(entries) != 2 {
		t.Fatalf("list: %+v, %v", entries, err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Problem, connection.Endpoint) {
			t.Fatal("list leaked private endpoint")
		}
	}
	for _, item := range []struct {
		path string
		mode os.FileMode
	}{
		{root, 0700},
		{filepath.Join(root, "scope.json"), 0600},
		{filepath.Join(root, ".writer.lock"), 0600},
		{filepath.Join(root, "records", string(domain.KindEnvironment), env.ID, "1", "connection.json"), 0600},
	} {
		info, statErr := os.Stat(item.path)
		if statErr != nil || info.Mode().Perm() != item.mode {
			t.Fatalf("mode %s: got %v, err %v", item.path, info.Mode().Perm(), statErr)
		}
	}
}

func TestStoreCASSnapshotImmutabilityAndCurrentEnvironment(t *testing.T) {
	store, _ := newTestStore(t, newSimulatedOps())
	env1, err := store.Register(context.Background(), dockerConnection())
	if err != nil {
		t.Fatal(err)
	}
	updatedConnection := dockerConnection()
	updatedConnection.ExpectedDaemonID = "daemon-b"
	env2, err := store.Update(context.Background(), env1.ID, 1, updatedConnection)
	if err != nil {
		t.Fatal(err)
	}
	if env2.Revision != 2 {
		t.Fatalf("revision = %d", env2.Revision)
	}
	current, err := store.CurrentEnvironment(context.Background(), "tenant-a", env1.ID)
	if err != nil || current.Reference() != env2.Reference() || current.Digest != env2.Digest {
		t.Fatalf("current Environment = %+v, %v", current, err)
	}
	if _, err = store.CurrentEnvironment(context.Background(), "tenant-b", env1.ID); !IsCode(err, CodeUnauthorized) {
		t.Fatalf("foreign scope resolved current Environment: %v", err)
	}
	if _, err = store.Update(context.Background(), env1.ID, 1, dockerConnection()); !IsCode(err, CodeConflict) {
		t.Fatalf("stale CAS accepted: %v", err)
	}
	stale := testSnapshot(env1, "33333333-3333-4333-8333-333333333333")
	if _, err = store.SaveSnapshot(context.Background(), "tenant-a", stale); !IsCode(err, CodeConflict) {
		t.Fatalf("snapshot bound to stale Environment accepted: %v", err)
	}

	snapshot := testSnapshot(env2, "44444444-4444-4444-8444-444444444444")
	ref, err := store.SaveSnapshot(context.Background(), "tenant-a", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if again, retryErr := store.SaveSnapshot(context.Background(), "tenant-a", snapshot); retryErr != nil || again != ref {
		t.Fatalf("idempotent retry failed: %+v, %v", again, retryErr)
	}
	different := testSnapshot(env2, snapshot.ID)
	different.Spec.CollectorVersion = "different/v1"
	if _, err = store.SaveSnapshot(context.Background(), "tenant-a", different); !IsCode(err, CodeConflict) {
		t.Fatalf("different snapshot content reused ID: %v", err)
	}
	newerConnection := updatedConnection
	newerConnection.ExpectedDaemonID = "daemon-c"
	if _, err = store.Update(context.Background(), env2.ID, env2.Revision, newerConnection); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SaveSnapshot(context.Background(), "tenant-a", snapshot); !IsCode(err, CodeConflict) {
		t.Fatalf("idempotent retry bypassed current Environment gate: %v", err)
	}
}

func TestStoreRejectsForgedIdentityAndCrossScopeBeforeRead(t *testing.T) {
	store, _ := newTestStore(t, newSimulatedOps())
	env, err := store.Register(context.Background(), dockerConnection())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := testSnapshot(env, "55555555-5555-4555-8555-555555555555")
	identity := domain.Identity{
		Runtime: domain.RuntimeDocker, EnvironmentID: env.ID, EntityKind: domain.EntityHost,
		NativeID: "forged-host", HostID: "other-host", DaemonID: "daemon-a",
	}
	entityID, err := domain.EntityID(identity)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Spec.Entities = []domain.Entity{{EntityID: entityID, EntityKind: domain.EntityHost, Identity: identity, Facts: []domain.Fact{}}}
	snapshot.Spec.Coverage[0].Count = 1
	if _, err = store.SaveSnapshot(context.Background(), "tenant-a", snapshot); !IsCode(err, CodeConflict) {
		t.Fatalf("forged identity accepted: %v", err)
	}
	foreign := env.Reference()
	foreign.TenantScope = "tenant-b"
	if _, err = store.Get(context.Background(), "tenant-a", foreign); !IsCode(err, CodeUnauthorized) {
		t.Fatalf("cross-scope reference reached storage: %v", err)
	}
	if _, err = store.Get(context.Background(), "tenant-b", env.Reference()); !IsCode(err, CodeUnauthorized) {
		t.Fatalf("foreign principal reached storage: %v", err)
	}
}

func TestStoreCorruptionIsReportedWithoutTrustedRecord(t *testing.T) {
	store, root := newTestStore(t, newSimulatedOps())
	env, err := store.Register(context.Background(), dockerConnection())
	if err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(root, "records", string(domain.KindEnvironment), env.ID, "1", "record.json")
	original, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(recordPath, append(original, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Verify(context.Background(), "tenant-a", env.Reference()); !IsCode(err, CodeCorrupt) {
		t.Fatalf("tampered record was trusted: %v", err)
	}
	entries, err := store.List(context.Background(), "tenant-a", ListOptions{})
	if err != nil || len(entries) != 1 || entries[0].Status != StatusCorrupt || strings.Contains(entries[0].Problem, string(original)) {
		t.Fatalf("corrupt list result: %+v, %v", entries, err)
	}
}

func TestListPageResumesAfterOpaqueServerReference(t *testing.T) {
	store, _ := newTestStore(t, newSimulatedOps())
	refs := make([]domain.RecordRef, 0, 3)
	for range 3 {
		environment, err := store.Register(context.Background(), dockerConnection())
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, environment.Reference())
	}
	sortRefs(refs)

	first, err := store.ListPage(context.Background(), "tenant-a", ListOptions{
		Kind: domain.KindEnvironment, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Entries) != 2 || !first.HasMore || first.NextRef == nil || *first.NextRef != refs[1] {
		t.Fatalf("first page = %+v, ordered refs = %+v", first, refs)
	}
	if first.Entries[0].Reference != refs[0] || first.Entries[1].Reference != refs[1] {
		t.Fatalf("first page ordering = %+v, want %+v", first.Entries, refs[:2])
	}

	second, err := store.ListPage(context.Background(), "tenant-a", ListOptions{
		Kind: domain.KindEnvironment, Limit: 2, After: first.NextRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Entries) != 1 || second.HasMore || second.NextRef != nil || second.Entries[0].Reference != refs[2] {
		t.Fatalf("second page = %+v, want only %+v", second, refs[2])
	}
}

func TestListPageValidatesResumeReferenceBeforeScanning(t *testing.T) {
	store, _ := newTestStore(t, newSimulatedOps())
	environment, err := store.Register(context.Background(), dockerConnection())
	if err != nil {
		t.Fatal(err)
	}

	foreign := environment.Reference()
	foreign.TenantScope = "tenant-b"
	if _, err = store.ListPage(context.Background(), "tenant-a", ListOptions{After: &foreign}); !IsCode(err, CodeUnauthorized) {
		t.Fatalf("foreign resume reference accepted: %v", err)
	}
	mismatched := environment.Reference()
	if _, err = store.ListPage(context.Background(), "tenant-a", ListOptions{
		Kind: domain.KindInventorySnapshot, After: &mismatched,
	}); !IsCode(err, CodeInvalidArgument) {
		t.Fatalf("kind-mismatched resume reference accepted: %v", err)
	}
	missing := environment.Reference()
	missing.ID = "99999999-9999-4999-8999-999999999999"
	if _, err = store.ListPage(context.Background(), "tenant-a", ListOptions{After: &missing}); !IsCode(err, CodeInvalidArgument) {
		t.Fatalf("missing resume reference accepted: %v", err)
	}
}

func TestListPageCorruptEntryConsumesPageSlot(t *testing.T) {
	store, root := newTestStore(t, newSimulatedOps())
	refs := make([]domain.RecordRef, 0, 2)
	for range 2 {
		environment, err := store.Register(context.Background(), dockerConnection())
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, environment.Reference())
	}
	sortRefs(refs)
	recordPath := filepath.Join(root, "records", string(refs[0].Kind), refs[0].ID, "1", "record.json")
	if err := os.WriteFile(recordPath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	first, err := store.ListPage(context.Background(), "tenant-a", ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Entries) != 1 || first.Entries[0].Reference != refs[0] || first.Entries[0].Status != StatusCorrupt || !first.HasMore || first.NextRef == nil {
		t.Fatalf("corrupt first page = %+v", first)
	}
	second, err := store.ListPage(context.Background(), "tenant-a", ListOptions{Limit: 1, After: first.NextRef})
	if err != nil || len(second.Entries) != 1 || second.Entries[0].Reference != refs[1] || second.HasMore {
		t.Fatalf("page after corrupt entry = %+v, %v", second, err)
	}
}

func TestStoreRejectsSymlinkedRecordDirectoryAfterOpen(t *testing.T) {
	store, root := newTestStore(t, newSimulatedOps())
	env, err := store.Register(context.Background(), dockerConnection())
	if err != nil {
		t.Fatal(err)
	}
	revision := store.recordDir(env.Reference())
	moved := revision + "-moved"
	if err = os.Rename(revision, moved); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(moved, revision); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(context.Background(), "tenant-a", env.Reference()); !IsCode(err, CodeCorrupt) {
		t.Fatalf("Get followed a symlinked revision directory: %v", err)
	}
	entries, err := store.List(context.Background(), "tenant-a", ListOptions{})
	if err != nil || len(entries) != 1 || entries[0].Reference != env.Reference() || entries[0].Status != StatusCorrupt {
		t.Fatalf("List did not report symlinked revision in %s: %+v, %v", root, entries, err)
	}
}

func TestStorePendingCancellationAndPublicationOutcome(t *testing.T) {
	ops := newSimulatedOps()
	store, root := newTestStore(t, ops)
	env, err := store.Register(context.Background(), dockerConnection())
	if err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(root, "records", string(domain.KindInventorySnapshot), "66666666-6666-4666-8666-666666666666")
	if err = os.Mkdir(pending, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(pending, ".domain-pending-crash"), 0700); err != nil {
		t.Fatal(err)
	}
	entries, err := store.List(context.Background(), "tenant-a", ListOptions{})
	if err != nil || len(entries) != 1 {
		t.Fatalf("pending became visible: %+v, %v", entries, err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	before := testSnapshot(env, "77777777-7777-4777-8777-777777777777")
	if _, err = store.SaveSnapshot(canceled, "tenant-a", before); !IsCode(err, CodeCanceled) {
		t.Fatalf("pre-publication cancellation: %v", err)
	}
	if _, statErr := os.Stat(store.recordDir(before.Reference())); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("canceled snapshot was published: %v", statErr)
	}

	ops.failParentSync = true
	after := testSnapshot(env, "88888888-8888-4888-8888-888888888888")
	ref, err := store.SaveSnapshot(context.Background(), "tenant-a", after)
	if !IsCode(err, CodeOutcomeUnknown) || ref != after.Reference() {
		t.Fatalf("post-publish failure was not outcome_unknown: %+v, %v", ref, err)
	}
	ops.failParentSync = false
	if _, err = store.Get(context.Background(), "tenant-a", ref); err != nil {
		t.Fatalf("published record cannot be reconciled: %v", err)
	}
}

func TestStoreRejectsUnsafeStateAndUnsupportedRename(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "state")
	if err := os.Mkdir(root, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := initWithOps(root, "tenant-a", os.Geteuid(), newSimulatedOps()); !IsCode(err, CodeStorage) {
		t.Fatalf("unsafe mode accepted: %v", err)
	}

	ops := newSimulatedOps()
	store, _ := newTestStore(t, ops)
	ops.renameErr = errUnsupported
	if _, err := store.Register(context.Background(), dockerConnection()); !IsCode(err, CodeUnsupported) {
		t.Fatalf("unsupported no-replace fell back: %v", err)
	}
	entries, err := store.List(context.Background(), "tenant-a", ListOptions{})
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed rename exposed record: %+v, %v", entries, err)
	}
}

func TestEnvironmentCASIsSerialized(t *testing.T) {
	store, _ := newTestStore(t, newSimulatedOps())
	env, err := store.Register(context.Background(), dockerConnection())
	if err != nil {
		t.Fatal(err)
	}
	connections := []Connection{dockerConnection(), dockerConnection()}
	connections[0].ExpectedDaemonID = "concurrent-a"
	connections[1].ExpectedDaemonID = "concurrent-b"
	results := make(chan error, 2)
	for i := range connections {
		go func(connection Connection) {
			_, updateErr := store.Update(context.Background(), env.ID, 1, connection)
			results <- updateErr
		}(connections[i])
	}
	wins, conflicts := 0, 0
	for range connections {
		err = <-results
		switch {
		case err == nil:
			wins++
		case IsCode(err, CodeConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent CAS result: %v", err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
}

func TestStoreFailureInjectionLeavesNoFinalBundle(t *testing.T) {
	for _, filename := range []string{"record.json", "connection.json", "manifest.json"} {
		t.Run(filename, func(t *testing.T) {
			ops := newSimulatedOps()
			store, _ := newTestStore(t, ops)
			ops.failWriteBase = filename
			if _, err := store.Register(context.Background(), dockerConnection()); !IsCode(err, CodeStorage) {
				t.Fatalf("write failure classification: %v", err)
			}
			entries, err := store.List(context.Background(), "tenant-a", ListOptions{})
			if err != nil || len(entries) != 0 {
				t.Fatalf("partial bundle visible: %+v, %v", entries, err)
			}
		})
	}
	t.Run("pending directory sync", func(t *testing.T) {
		ops := newSimulatedOps()
		store, _ := newTestStore(t, ops)
		ops.failPendingSync = true
		if _, err := store.Register(context.Background(), dockerConnection()); !IsCode(err, CodeStorage) {
			t.Fatalf("sync failure classification: %v", err)
		}
		entries, err := store.List(context.Background(), "tenant-a", ListOptions{})
		if err != nil || len(entries) != 0 {
			t.Fatalf("unsynced bundle visible: %+v, %v", entries, err)
		}
	})
}

func TestWriterLockWaitIsCancelable(t *testing.T) {
	ops := newSimulatedOps()
	store, _ := newTestStore(t, ops)
	<-ops.lock
	defer func() { ops.lock <- struct{}{} }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := store.Register(ctx, dockerConnection()); !IsCode(err, CodeCanceled) {
		t.Fatalf("lock wait did not honor cancellation: %v", err)
	}
}

func TestSnapshotLineageCannotCrossEnvironment(t *testing.T) {
	store, _ := newTestStore(t, newSimulatedOps())
	firstEnv, err := store.Register(context.Background(), dockerConnection())
	if err != nil {
		t.Fatal(err)
	}
	secondEnv, err := store.Register(context.Background(), dockerConnection())
	if err != nil {
		t.Fatal(err)
	}
	first := testSnapshot(firstEnv, "99999999-9999-4999-8999-999999999999")
	if _, err = store.SaveSnapshot(context.Background(), "tenant-a", first); err != nil {
		t.Fatal(err)
	}
	second := testSnapshot(secondEnv, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	previous := first.Reference()
	second.Spec.PreviousSnapshotRef = &previous
	if _, err = store.SaveSnapshot(context.Background(), "tenant-a", second); !IsCode(err, CodeConflict) {
		t.Fatalf("cross-Environment lineage accepted: %v", err)
	}
}

func TestProductionOwnerUIDCannotBeImpersonated(t *testing.T) {
	if _, err := Init(filepath.Join(t.TempDir(), "state"), "tenant-a", os.Geteuid()+1); !IsCode(err, CodeUnauthorized) {
		t.Fatalf("Init accepted a caller-selected owner UID: %v", err)
	}
	if _, err := Open(filepath.Join(t.TempDir(), "state"), os.Geteuid()+1); !IsCode(err, CodeUnauthorized) {
		t.Fatalf("Open accepted a caller-selected owner UID: %v", err)
	}
}

func TestOpenRejectsMissingFixedLayout(t *testing.T) {
	ops := newSimulatedOps()
	_, root := newTestStore(t, ops)
	if err := os.Remove(filepath.Join(root, "records", string(domain.KindInventorySnapshot))); err != nil {
		t.Fatal(err)
	}
	if _, err := openWithOps(root, os.Geteuid(), ops); !IsCode(err, CodeCorrupt) {
		t.Fatalf("Open accepted missing fixed record directory: %v", err)
	}
}

func TestConnectionPathShapeRejectsTraversalAndURLs(t *testing.T) {
	for _, endpoint := range []string{"relative.sock", "/run/../tmp/docker.sock", "https://example.test/docker"} {
		connection := dockerConnection()
		connection.Endpoint = endpoint
		if err := validateConnection(connection); err == nil {
			t.Fatalf("accepted unsafe Docker endpoint %q", endpoint)
		}
	}
	kube := Connection{
		Runtime: domain.RuntimeKubernetes, Endpoint: "/etc/../tmp/kubeconfig", ClusterID: "cluster-a",
		ExpectedClusterFingerprint: "sha256:registered", Namespaces: []string{"models"}, NetworkPolicy: domain.NetworkOffline,
	}
	if err := validateConnection(kube); err == nil {
		t.Fatal("accepted traversing kubeconfig path")
	}
}

func TestDecodeStrictRejectsAliasesDuplicatesInvalidUTF8AndNestedUnknown(t *testing.T) {
	valid := []byte(`{"schemaVersion":"private-deployment-store/v1","files":[{"name":"record.json","sha256":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`)
	var decoded manifest
	if err := decodeStrict(valid, &decoded); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	cases := map[string][]byte{
		"case alias":      []byte(`{"SchemaVersion":"private-deployment-store/v1","files":[]}`),
		"duplicate":       []byte(`{"schemaVersion":"private-deployment-store/v1","schemaVersion":"private-deployment-store/v1","files":[]}`),
		"nested alias":    []byte(`{"schemaVersion":"private-deployment-store/v1","files":[{"Name":"record.json","sha256":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`),
		"nested unknown":  []byte(`{"schemaVersion":"private-deployment-store/v1","files":[{"name":"record.json","sha256":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":1}]}`),
		"null collection": []byte(`{"schemaVersion":"private-deployment-store/v1","files":null}`),
		"trailing value":  append(append([]byte(nil), valid...), []byte(` {}`)...),
		"invalid unicode": append([]byte(`{"schemaVersion":"`), append([]byte{0xff}, []byte(`","files":[]}`)...)...),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			var target manifest
			if err := decodeStrict(input, &target); err == nil {
				t.Fatalf("accepted non-strict JSON: %q", input)
			}
		})
	}
}

func TestDecodeConnectionIsExactAndRuntimeTyped(t *testing.T) {
	valid, err := json.Marshal(dockerConnection())
	if err != nil {
		t.Fatal(err)
	}
	connection, err := DecodeConnection(valid)
	if err != nil || connection.ExpectedDaemonID != dockerConnection().ExpectedDaemonID {
		t.Fatalf("valid connection decode: %+v, %v", connection, err)
	}
	for name, input := range map[string][]byte{
		"case alias": []byte(`{"Runtime":"docker","endpoint":"/run/docker.sock","hostID":"host-a","expectedDaemonID":"daemon-a","networkPolicy":"offline"}`),
		"duplicate":  []byte(`{"runtime":"docker","runtime":"kubernetes","endpoint":"/run/docker.sock","hostID":"host-a","expectedDaemonID":"daemon-a","networkPolicy":"offline"}`),
		"unknown":    append(valid[:len(valid)-1], []byte(`,"credential":"inline"}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeConnection(input); err == nil {
				t.Fatalf("accepted non-strict connection: %s", input)
			}
		})
	}
}

func TestStoreRejectsRehashedConnectionWithUnknownField(t *testing.T) {
	store, root := newTestStore(t, newSimulatedOps())
	env, err := store.Register(context.Background(), dockerConnection())
	if err != nil {
		t.Fatal(err)
	}
	dir := store.recordDir(env.Reference())
	recordBytes, err := os.ReadFile(filepath.Join(dir, "record.json"))
	if err != nil {
		t.Fatal(err)
	}
	connectionBytes, err := os.ReadFile(filepath.Join(dir, "connection.json"))
	if err != nil {
		t.Fatal(err)
	}
	connectionBytes = append(connectionBytes[:len(connectionBytes)-1], []byte(`,"unknown":"value"}`)...)
	manifestBytes, err := makeManifest(map[string][]byte{"record.json": recordBytes, "connection.json": connectionBytes})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "connection.json"), connectionBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "manifest.json"), manifestBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(context.Background(), "tenant-a", env.Reference()); !IsCode(err, CodeCorrupt) {
		t.Fatalf("rehashed connection with unknown field was trusted under %s: %v", root, err)
	}
}
