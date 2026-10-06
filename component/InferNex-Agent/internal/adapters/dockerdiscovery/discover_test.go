package dockerdiscovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domain"
)

const (
	testEnvironmentID = "11111111-1111-4111-8111-111111111111"
	testSnapshotID    = "22222222-2222-4222-8222-222222222222"
	testHostID        = "host-synthetic"
	testScope         = "scope-synthetic"
)

var fixedDiscoveryTime = time.Date(2026, 10, 2, 1, 2, 3, 4, time.UTC)

func TestDiscoverDockerProducesFinalizedAllowlistedSnapshot(t *testing.T) {
	const marker = "DO_NOT_LEAK_TOKEN_DISCOVERY_8E12"
	socket := discoveryFixtureServer(t, testDaemonID, []string{testIDOne}, func(w http.ResponseWriter, _ *http.Request, id string) {
		writeJSON(t, w, inspectFixture(id, marker))
	})
	discoverer := fixedDiscoverer(t, socket, testDaemonID, 0)
	environment := testDockerEnvironment(t, testDaemonID)

	snapshot, err := discoverer.Discover(context.Background(), environment, DiscoverRequest{PrincipalScope: testScope})
	if err != nil {
		t.Fatal(err)
	}
	if err := domain.VerifyRecord(snapshot); err != nil {
		t.Fatalf("verify snapshot: %v", err)
	}
	if snapshot.Spec.Completeness != domain.CompletenessComplete {
		t.Fatalf("completeness = %s, want complete", snapshot.Spec.Completeness)
	}
	if len(snapshot.Spec.Coverage) != 1 || snapshot.Spec.Coverage[0].State != domain.CoverageComplete || snapshot.Spec.Coverage[0].Count != 1 {
		t.Fatalf("coverage = %#v", snapshot.Spec.Coverage)
	}
	if len(snapshot.Spec.Entities) != 1 {
		t.Fatalf("entities = %d, want 1", len(snapshot.Spec.Entities))
	}
	entity := snapshot.Spec.Entities[0]
	if entity.EntityKind != domain.EntityContainer || entity.Identity.NativeID != testIDOne || entity.Identity.HostID != testHostID || entity.Identity.DaemonID != testDaemonID {
		t.Fatalf("identity = %#v", entity.Identity)
	}
	if len(snapshot.Spec.Relations) != 0 || len(snapshot.Spec.Issues) != 0 {
		t.Fatalf("unexpected inferred relations or issues: %#v %#v", snapshot.Spec.Relations, snapshot.Spec.Issues)
	}
	wantFields := map[string]bool{
		"name": true, "imageRef": true, "imageDigest": true, "phase": true,
		"ports": true, "mountRefs": true, "deviceRequests": true,
	}
	for _, fact := range entity.Facts {
		if !wantFields[fact.Field] {
			t.Errorf("unexpected fact field %q", fact.Field)
		}
		delete(wantFields, fact.Field)
		if fact.Status != domain.StatusObserved {
			t.Errorf("fact %s status = %s, want observed", fact.Field, fact.Status)
		}
		if fact.Source.ObjectRef.SnapshotRef == nil || fact.Source.ObjectRef.SnapshotRef.ID != snapshot.ID || fact.Source.ObjectRef.EntityID != entity.EntityID {
			t.Errorf("fact %s has imprecise source %#v", fact.Field, fact.Source)
		}
	}
	if len(wantFields) != 0 {
		t.Fatalf("missing facts: %v", wantFields)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(marker)) {
		t.Fatal("snapshot retained Env, label, auth, command, mount source, binding, or device option secret")
	}
	for _, prohibited := range []string{"cpuMilli", "memoryBytes", "topology-declaration", "component", "ownership"} {
		if bytes.Contains(raw, []byte(prohibited)) {
			t.Fatalf("snapshot fabricated unsupported %q evidence", prohibited)
		}
	}
}

func TestDiscoverAuthorizationPrecedesNetworkIO(t *testing.T) {
	var requests atomic.Int32
	socket := newUnixHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "must not be called", http.StatusInternalServerError)
	}))
	discoverer := fixedDiscoverer(t, socket, testDaemonID, 0)

	tests := []struct {
		name    string
		env     domain.Environment
		request DiscoverRequest
		code    ErrorCode
	}{
		{name: "wrong scope", env: testDockerEnvironment(t, testDaemonID), request: DiscoverRequest{PrincipalScope: "other"}, code: CodeForbidden},
		{name: "unknown resource", env: testDockerEnvironment(t, testDaemonID), request: DiscoverRequest{PrincipalScope: testScope, ResourceKinds: []string{"images"}}, code: CodeInvalidArgument},
		{name: "duplicate resource", env: testDockerEnvironment(t, testDaemonID), request: DiscoverRequest{PrincipalScope: testScope, ResourceKinds: []string{ResourceContainers, ResourceContainers}}, code: CodeInvalidArgument},
	}
	disabled := testDockerEnvironment(t, testDaemonID)
	disabled.Spec.Enabled = false
	finalizeEnvironment(t, &disabled)
	tests = append(tests, struct {
		name    string
		env     domain.Environment
		request DiscoverRequest
		code    ErrorCode
	}{name: "disabled", env: disabled, request: DiscoverRequest{PrincipalScope: testScope}, code: CodeForbidden})
	tampered := testDockerEnvironment(t, testDaemonID)
	tampered.Spec.HostID = "tampered-without-resigning"
	tests = append(tests, struct {
		name    string
		env     domain.Environment
		request DiscoverRequest
		code    ErrorCode
	}{name: "invalid record", env: tampered, request: DiscoverRequest{PrincipalScope: testScope}, code: CodeInvalidArgument})

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := requests.Load()
			snapshot, err := discoverer.Discover(context.Background(), test.env, test.request)
			if snapshot != nil {
				t.Fatal("preflight failure returned a snapshot")
			}
			assertCode(t, err, test.code)
			if after := requests.Load(); after != before {
				t.Fatalf("network calls changed from %d to %d", before, after)
			}
		})
	}
}

func TestDiscoverRequiresRegisteredIdentityFingerprint(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	socket := newUnixHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.RequestURI())
		mu.Unlock()
		switch r.URL.RequestURI() {
		case "/version":
			writeJSON(t, w, map[string]string{"ApiVersion": "1.44", "MinAPIVersion": "1.24"})
		case "/v1.44/info":
			writeJSON(t, w, map[string]string{"ID": testDaemonID})
		default:
			t.Fatal("list must not run after fingerprint mismatch")
		}
	}))
	discoverer := fixedDiscoverer(t, socket, testDaemonID, 0)
	environment := testDockerEnvironment(t, "another-registered-daemon")
	snapshot, err := discoverer.Discover(context.Background(), environment, DiscoverRequest{PrincipalScope: testScope})
	if err != nil {
		t.Fatal(err)
	}
	assertFailedSnapshot(t, snapshot, domain.CoverageFailed, domain.IssueIdentityChanged)
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(paths) != fmt.Sprint([]string{"/version", "/v1.44/info"}) {
		t.Fatalf("paths = %v", paths)
	}
}

func TestDiscoverFailureClassesRemainDistinct(t *testing.T) {
	tests := []struct {
		name       string
		server     func(t *testing.T) string
		expectedID string
		state      domain.CoverageState
		issue      domain.IssueCode
	}{
		{
			name: "unavailable",
			server: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "missing.sock")
			},
			expectedID: testDaemonID, state: domain.CoverageFailed, issue: domain.IssueUnavailable,
		},
		{
			name: "forbidden",
			server: func(t *testing.T) string {
				return newUnixHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte("DO_NOT_LEAK daemon denial"))
				}))
			},
			expectedID: testDaemonID, state: domain.CoverageForbidden, issue: domain.IssueForbidden,
		},
		{
			name: "unsupported",
			server: func(t *testing.T) string {
				return newUnixHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					writeJSON(t, w, map[string]string{"ApiVersion": "1.43", "MinAPIVersion": "1.20"})
				}))
			},
			expectedID: testDaemonID, state: domain.CoverageUnsupported, issue: domain.IssueUnsupported,
		},
		{
			name: "daemon identity changed",
			server: func(t *testing.T) string {
				return newUnixHTTPServer(t, standardHandler(t, "replacement-daemon", nil, nil))
			},
			expectedID: testDaemonID, state: domain.CoverageFailed, issue: domain.IssueIdentityChanged,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			discoverer := fixedDiscoverer(t, test.server(t), test.expectedID, 0)
			snapshot, err := discoverer.Discover(context.Background(), testDockerEnvironment(t, test.expectedID), DiscoverRequest{PrincipalScope: testScope})
			if err != nil {
				t.Fatal(err)
			}
			assertFailedSnapshot(t, snapshot, test.state, test.issue)
			raw, _ := json.Marshal(snapshot)
			if bytes.Contains(raw, []byte("DO_NOT_LEAK")) {
				t.Fatal("failed snapshot leaked daemon response")
			}
		})
	}
}

func TestDiscoverInspectFailureReturnsPartialFacts(t *testing.T) {
	socket := discoveryFixtureServer(t, testDaemonID, []string{testIDOne, testIDTwo, testIDThreeForDiscovery}, func(w http.ResponseWriter, _ *http.Request, id string) {
		if id == testIDTwo {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("DO_NOT_LEAK inspect body"))
			return
		}
		writeJSON(t, w, inspectFixture(id, ""))
	})
	discoverer := fixedDiscoverer(t, socket, testDaemonID, 0)
	snapshot, err := discoverer.Discover(context.Background(), testDockerEnvironment(t, testDaemonID), DiscoverRequest{PrincipalScope: testScope, ResourceKinds: []string{ResourceContainers}})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Spec.Completeness != domain.CompletenessPartial || len(snapshot.Spec.Entities) != 2 {
		t.Fatalf("partial snapshot = %#v", snapshot.Spec)
	}
	if snapshot.Spec.Coverage[0].State != domain.CoveragePartial || snapshot.Spec.Coverage[0].Reason != domain.IssueNotFound {
		t.Fatalf("coverage = %#v", snapshot.Spec.Coverage[0])
	}
	if !hasIssue(snapshot, domain.IssueNotFound) {
		t.Fatalf("issues = %#v", snapshot.Spec.Issues)
	}
}

func TestDiscoverTimeoutKeepsCompletedEntitiesButCancelReturnsNone(t *testing.T) {
	for _, test := range []struct {
		name      string
		cancel    bool
		timeout   time.Duration
		wantError error
	}{
		{name: "timeout partial", timeout: 35 * time.Millisecond},
		{name: "caller cancellation", cancel: true, timeout: time.Second, wantError: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			blocked := make(chan struct{})
			var once sync.Once
			socket := discoveryFixtureServer(t, testDaemonID, []string{testIDOne, testIDTwo}, func(w http.ResponseWriter, r *http.Request, id string) {
				if id == testIDOne {
					writeJSON(t, w, inspectFixture(id, ""))
					return
				}
				once.Do(func() { close(blocked) })
				w.WriteHeader(http.StatusOK)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				<-r.Context().Done()
			})
			client, err := newClient(socket, testDaemonID, test.timeout)
			if err != nil {
				t.Fatal(err)
			}
			discoverer, err := newDiscoverer(client, func() time.Time { return fixedDiscoveryTime }, func() (string, error) { return testSnapshotID, nil })
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.cancel {
				go func() {
					<-blocked
					cancel()
				}()
			}
			snapshot, discoverErr := discoverer.Discover(ctx, testDockerEnvironment(t, testDaemonID), DiscoverRequest{PrincipalScope: testScope})
			if test.wantError != nil {
				if snapshot != nil || !errors.Is(discoverErr, test.wantError) {
					t.Fatalf("snapshot=%#v error=%v", snapshot, discoverErr)
				}
				return
			}
			if discoverErr != nil {
				t.Fatal(discoverErr)
			}
			if snapshot.Spec.Completeness != domain.CompletenessPartial || len(snapshot.Spec.Entities) != 1 || snapshot.Spec.Coverage[0].State != domain.CoverageTimeout || !hasIssue(snapshot, domain.IssueTimeout) {
				t.Fatalf("timeout snapshot = %#v", snapshot.Spec)
			}
		})
	}
}

func TestDiscoverCapsEntitiesAndReportsLimitWithoutCursor(t *testing.T) {
	ids := make([]string, domain.MaxEntities+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("%064x", i+1)
	}
	socket := discoveryFixtureServer(t, testDaemonID, ids, func(w http.ResponseWriter, _ *http.Request, id string) {
		writeJSON(t, w, inspectFixture(id, ""))
	})
	discoverer := fixedDiscoverer(t, socket, testDaemonID, 0)
	snapshot, err := discoverer.Discover(context.Background(), testDockerEnvironment(t, testDaemonID), DiscoverRequest{PrincipalScope: testScope})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Spec.Entities) != domain.MaxEntities || snapshot.Spec.Completeness != domain.CompletenessPartial || snapshot.Spec.Coverage[0].Reason != domain.IssueLimitExceeded {
		t.Fatalf("limited snapshot = entities:%d coverage:%#v completeness:%s", len(snapshot.Spec.Entities), snapshot.Spec.Coverage, snapshot.Spec.Completeness)
	}
	raw, _ := json.Marshal(snapshot)
	if bytes.Contains(bytes.ToLower(raw), []byte("cursor")) || bytes.Contains(bytes.ToLower(raw), []byte("continue")) {
		t.Fatal("Docker limit fabricated a pagination cursor")
	}
}

func TestDiscoverDoesNotInferPDRolesFromNames(t *testing.T) {
	ids := []string{testIDOne, testIDTwo}
	socket := discoveryFixtureServer(t, testDaemonID, ids, func(w http.ResponseWriter, _ *http.Request, id string) {
		fixture := inspectFixture(id, "")
		if id == testIDOne {
			fixture["Name"] = "/prefill"
		} else {
			fixture["Name"] = "/decode"
		}
		fixture["Config"].(map[string]any)["Labels"] = map[string]string{"role": strings.TrimPrefix(fixture["Name"].(string), "/")}
		writeJSON(t, w, fixture)
	})
	discoverer := fixedDiscoverer(t, socket, testDaemonID, 0)
	snapshot, err := discoverer.Discover(context.Background(), testDockerEnvironment(t, testDaemonID), DiscoverRequest{PrincipalScope: testScope})
	if err != nil {
		t.Fatal(err)
	}
	for _, entity := range snapshot.Spec.Entities {
		if entity.EntityKind != domain.EntityContainer {
			t.Fatalf("inferred entity from name/label: %#v", entity)
		}
	}
	if len(snapshot.Spec.Relations) != 0 {
		t.Fatalf("inferred PD relations: %#v", snapshot.Spec.Relations)
	}
}

func TestNormalizeRedactsProjectedSecretsAndDropsOversizeEntity(t *testing.T) {
	environment := testDockerEnvironment(t, testDaemonID)
	snapshot := newSnapshot(environment, testSnapshotID, fixedDiscoveryTime)
	secretInspect := containerInspect{
		id:             testIDOne,
		name:           "/safe",
		imageReference: "https://user:password@example.invalid/image",
		imageID:        "sha256:safe",
		state:          "running",
		ports:          []string{"80/tcp"},
		mounts:         []mountProjection{{name: "safe"}},
	}
	entity, issues, ok := normalizeContainer(snapshot, environment, testDaemonID, secretInspect, formatTimestamp(fixedDiscoveryTime))
	if !ok || !containsIssue(issues, domain.IssueRedacted) {
		t.Fatalf("entity=%#v issues=%#v ok=%v", entity, issues, ok)
	}
	raw, _ := json.Marshal(entity)
	if bytes.Contains(raw, []byte("password")) || !bytes.Contains(raw, []byte(domain.Redacted)) {
		t.Fatalf("projected credential handling = %s", raw)
	}

	oversize := secretInspect
	oversize.imageReference = "safe"
	oversize.mounts = make([]mountProjection, 200)
	for i := range oversize.mounts {
		oversize.mounts[i].name = fmt.Sprintf("%04d-%s", i, strings.Repeat("x", 400))
	}
	_, issues, ok = normalizeContainer(snapshot, environment, testDaemonID, oversize, formatTimestamp(fixedDiscoveryTime))
	if ok || !containsIssue(issues, domain.IssueLimitExceeded) {
		t.Fatalf("oversize entity accepted: issues=%#v", issues)
	}
}

func TestDockerIdentityFingerprintUsesOnlyCanonicalRegisteredIdentity(t *testing.T) {
	one, err := DockerIdentityFingerprint(testHostID, testDaemonID)
	if err != nil {
		t.Fatal(err)
	}
	two, err := DockerIdentityFingerprint(testHostID, testDaemonID)
	if err != nil {
		t.Fatal(err)
	}
	if one != two || !strings.HasPrefix(one, domain.DigestPrefix) || len(one) != len(domain.DigestPrefix)+64 {
		t.Fatalf("fingerprints = %q %q", one, two)
	}
	other, _ := DockerIdentityFingerprint(testHostID, "replacement")
	if other == one {
		t.Fatal("daemon identity change did not change fingerprint")
	}
}

const testIDThreeForDiscovery = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

func discoveryFixtureServer(t *testing.T, daemonID string, ids []string, inspect func(http.ResponseWriter, *http.Request, string)) string {
	t.Helper()
	return newUnixHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.RequestURI() {
		case "/version":
			writeJSON(t, w, map[string]string{"ApiVersion": "1.44", "MinAPIVersion": "1.24"})
		case "/v1.44/info":
			writeJSON(t, w, map[string]string{"ID": daemonID})
		case "/v1.44/containers/json?all=1":
			containers := make([]map[string]string, len(ids))
			for i, id := range ids {
				containers[i] = map[string]string{"Id": id}
			}
			writeJSON(t, w, containers)
		default:
			prefix := "/v1.44/containers/"
			suffix := "/json"
			if strings.HasPrefix(r.URL.Path, prefix) && strings.HasSuffix(r.URL.Path, suffix) {
				id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), suffix)
				inspect(w, r, id)
				return
			}
			http.NotFound(w, r)
		}
	}))
}

func fixedDiscoverer(t *testing.T, socket, expectedDaemonID string, timeout time.Duration) *Discoverer {
	t.Helper()
	var client *Client
	var err error
	if timeout == 0 {
		client, err = NewClient(socket, expectedDaemonID)
	} else {
		client, err = newClient(socket, expectedDaemonID, timeout)
	}
	if err != nil {
		t.Fatal(err)
	}
	discoverer, err := newDiscoverer(client, func() time.Time { return fixedDiscoveryTime }, func() (string, error) { return testSnapshotID, nil })
	if err != nil {
		t.Fatal(err)
	}
	return discoverer
}

func testDockerEnvironment(t *testing.T, daemonID string) domain.Environment {
	t.Helper()
	fingerprint, err := DockerIdentityFingerprint(testHostID, daemonID)
	if err != nil {
		t.Fatal(err)
	}
	environment := domain.Environment{
		SchemaVersion: domain.SchemaVersion,
		Kind:          domain.KindEnvironment,
		TenantScope:   testScope,
		ID:            testEnvironmentID,
		Revision:      1,
		CreatedAt:     formatTimestamp(fixedDiscoveryTime),
		Spec: domain.EnvironmentSpec{
			Runtime:             domain.RuntimeDocker,
			EndpointRef:         domain.LocalRef{ID: testEnvironmentID},
			IdentityFingerprint: fingerprint,
			HostID:              testHostID,
			AllowedNamespaces:   []string{},
			NetworkPolicy:       domain.NetworkOffline,
			Enabled:             true,
		},
	}
	finalizeEnvironment(t, &environment)
	return environment
}

func finalizeEnvironment(t *testing.T, environment *domain.Environment) {
	t.Helper()
	environment.Digest = ""
	if err := domain.FinalizeRecord(environment); err != nil {
		t.Fatal(err)
	}
}

func assertFailedSnapshot(t *testing.T, snapshot *domain.InventorySnapshot, state domain.CoverageState, issue domain.IssueCode) {
	t.Helper()
	if snapshot == nil {
		t.Fatal("snapshot is nil")
	}
	if err := domain.VerifyRecord(snapshot); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if snapshot.Spec.Completeness != domain.CompletenessFailed || len(snapshot.Spec.Entities) != 0 || len(snapshot.Spec.Coverage) != 1 || snapshot.Spec.Coverage[0].State != state || !hasIssue(snapshot, issue) {
		t.Fatalf("failed snapshot = %#v", snapshot.Spec)
	}
}

func hasIssue(snapshot *domain.InventorySnapshot, code domain.IssueCode) bool {
	return containsIssue(snapshot.Spec.Issues, code)
}

func containsIssue(issues []domain.Issue, code domain.IssueCode) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
