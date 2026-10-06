package inventoryview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domain"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domainstore"
)

const (
	testScope = "local-operator"
	testToken = "0123456789abcdef0123456789abcdef"
	envID     = "11111111-1111-4111-8111-111111111111"
	beforeID  = "22222222-2222-4222-8222-222222222222"
	afterID   = "33333333-3333-4333-8333-333333333333"
	testTime  = "2026-10-07T00:00:00.000000000Z"
)

type memoryReader struct {
	mu      sync.Mutex
	records map[domain.RecordRef]domain.Record
	entries []domainstore.ListEntry
	calls   int
	scopes  []string
}

func (m *memoryReader) Get(_ context.Context, scope string, ref domain.RecordRef) (domain.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.scopes = append(m.scopes, scope)
	record := m.records[ref]
	if record == nil {
		return nil, &domainstore.Error{Code: domainstore.CodeNotFound, Op: "test", Err: errors.New("secret backend path")}
	}
	return record, nil
}

func (m *memoryReader) ListPage(_ context.Context, scope string, options domainstore.ListOptions) (domainstore.ListPage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.scopes = append(m.scopes, scope)
	filtered := make([]domainstore.ListEntry, 0, len(m.entries))
	for _, entry := range m.entries {
		if options.Kind == "" || entry.Reference.Kind == options.Kind {
			filtered = append(filtered, entry)
		}
	}
	start := 0
	if options.After != nil {
		start = -1
		for index, entry := range filtered {
			if entry.Reference == *options.After {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return domainstore.ListPage{}, &domainstore.Error{Code: domainstore.CodeInvalidArgument, Op: "test", Err: errors.New("secret cursor detail")}
		}
	}
	end := start + options.Limit
	if end > len(filtered) {
		end = len(filtered)
	}
	page := domainstore.ListPage{Entries: append([]domainstore.ListEntry(nil), filtered[start:end]...), HasMore: end < len(filtered)}
	if page.HasMore {
		ref := page.Entries[len(page.Entries)-1].Reference
		page.NextRef = &ref
	}
	return page, nil
}

func request(handler http.Handler, method, target, authorization string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestAuthenticationOriginMethodAndStrictScopeGate(t *testing.T) {
	store := &memoryReader{records: map[domain.RecordRef]domain.Record{}}
	handler := New(store, testScope, testToken)
	target := "/api/v1/inventory/records?kind=Environment&limit=1"
	for _, item := range []struct {
		name, auth string
		status     int
	}{
		{"missing", "", http.StatusUnauthorized},
		{"wrong", "Bearer 0123456789abcdef0123456789abcdeg", http.StatusUnauthorized},
		{"wrong scheme", "Basic " + testToken, http.StatusUnauthorized},
	} {
		t.Run(item.name, func(t *testing.T) {
			response := request(handler, http.MethodGet, target, item.auth)
			if response.Code != item.status || response.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatalf("status=%d CORS=%q", response.Code, response.Header().Get("Access-Control-Allow-Origin"))
			}
		})
	}
	if store.calls != 0 {
		t.Fatalf("unauthorized requests reached store %d times", store.calls)
	}

	originRequest := httptest.NewRequest(http.MethodGet, target, nil)
	originRequest.Header.Set("Authorization", "Bearer "+testToken)
	originRequest.Header.Set("Origin", "https://attacker.invalid")
	originResponse := httptest.NewRecorder()
	handler.ServeHTTP(originResponse, originRequest)
	if originResponse.Code != http.StatusForbidden || store.calls != 0 {
		t.Fatalf("cross-origin status=%d calls=%d", originResponse.Code, store.calls)
	}
	if response := request(handler, http.MethodPost, target, "Bearer "+testToken); response.Code != http.StatusMethodNotAllowed || store.calls != 0 {
		t.Fatalf("POST status=%d calls=%d", response.Code, store.calls)
	}

	// Scope is never accepted from a client, and rejection happens before a
	// data read. The store sees only the constructor-bound scope.
	badScope := "/api/v1/inventory/record?kind=Environment&id=" + envID + "&revision=1&tenantScope=other"
	if response := request(handler, http.MethodGet, badScope, "Bearer "+testToken); response.Code != http.StatusBadRequest || store.calls != 0 {
		t.Fatalf("client scope status=%d calls=%d", response.Code, store.calls)
	}
	for _, target := range []string{"/api/v1/inventory/records?kind=", "/api/v1/inventory/records?limit="} {
		if response := request(handler, http.MethodGet, target, "Bearer "+testToken); response.Code != http.StatusBadRequest || store.calls != 0 {
			t.Fatalf("explicit empty filter %q status=%d calls=%d", target, response.Code, store.calls)
		}
	}
	if response := request(handler, http.MethodGet, "/api/v1/inventory/records", "Bearer "+testToken); response.Code != http.StatusOK {
		t.Fatalf("authorized list status=%d body=%s", response.Code, response.Body.String())
	}
	if len(store.scopes) != 1 || store.scopes[0] != testScope {
		t.Fatalf("store scopes = %#v", store.scopes)
	}
}

func TestListCursorIsBoundOpaqueAndTamperEvident(t *testing.T) {
	first := environmentFixture(t, 1, "host-a")
	second := environmentFixtureWithID(t, "44444444-4444-4444-8444-444444444444", 1, "host-b")
	store := &memoryReader{records: map[domain.RecordRef]domain.Record{}, entries: []domainstore.ListEntry{
		{Reference: first.Reference(), Digest: first.Digest, CreatedAt: first.CreatedAt, Status: domainstore.StatusOK},
		{Reference: second.Reference(), Digest: second.Digest, CreatedAt: second.CreatedAt, Status: domainstore.StatusOK},
	}}
	handler := newWithEntropy(store, testScope, testToken, bytes.NewReader(bytes.Repeat([]byte{0x11}, 32)))
	firstPage := request(handler, http.MethodGet, "/api/v1/inventory/records?kind=Environment&limit=1", "Bearer "+testToken)
	if firstPage.Code != http.StatusOK || strings.Contains(firstPage.Body.String(), testScope) {
		t.Fatalf("first page status=%d body=%s", firstPage.Code, firstPage.Body.String())
	}
	var page listResponse
	if err := json.Unmarshal(firstPage.Body.Bytes(), &page); err != nil || page.Next == "" || len(page.Records) != 1 {
		t.Fatalf("first page=%+v err=%v", page, err)
	}
	continued := request(handler, http.MethodGet, "/api/v1/inventory/records?after="+page.Next, "Bearer "+testToken)
	if continued.Code != http.StatusOK {
		t.Fatalf("continued status=%d body=%s", continued.Code, continued.Body.String())
	}
	// Cursor signing state is private to one handler lifetime. A restart with
	// the same bearer token cannot replay the old cursor.
	restarted := newWithEntropy(store, testScope, testToken, bytes.NewReader(bytes.Repeat([]byte{0x22}, 32)))
	if response := request(restarted, http.MethodGet, "/api/v1/inventory/records?after="+page.Next, "Bearer "+testToken); response.Code != http.StatusBadRequest {
		t.Fatalf("cursor survived handler restart: status=%d", response.Code)
	}
	// The fixed scope is part of the MAC domain even if signing key material is
	// held constant, so a cursor cannot cross a server scope boundary.
	otherScope := newWithEntropy(store, "other-scope", testToken, bytes.NewReader(bytes.Repeat([]byte{0x11}, 32)))
	if response := request(otherScope, http.MethodGet, "/api/v1/inventory/records?after="+page.Next, "Bearer "+testToken); response.Code != http.StatusBadRequest {
		t.Fatalf("cursor crossed scope: status=%d", response.Code)
	}
	tampered := page.Next[:len(page.Next)-1] + "A"
	if response := request(handler, http.MethodGet, "/api/v1/inventory/records?after="+tampered, "Bearer "+testToken); response.Code != http.StatusBadRequest {
		t.Fatalf("tampered cursor status=%d", response.Code)
	}
	if response := request(handler, http.MethodGet, "/api/v1/inventory/records?after="+page.Next+"&limit=2", "Bearer "+testToken); response.Code != http.StatusBadRequest {
		t.Fatalf("cursor plus filter status=%d", response.Code)
	}
}

func TestCursorEntropyFailureFailsClosed(t *testing.T) {
	store := &memoryReader{records: map[domain.RecordRef]domain.Record{}}
	handler := newWithEntropy(store, testScope, testToken, strings.NewReader("short"))
	response := request(handler, http.MethodGet, "/api/v1/inventory/records", "Bearer "+testToken)
	if response.Code != http.StatusInternalServerError || store.calls != 0 || strings.Contains(response.Body.String(), "EOF") {
		t.Fatalf("entropy failure status=%d calls=%d body=%s", response.Code, store.calls, response.Body.String())
	}
}

func TestEnvironmentViewOmitsPrivateBindingsAndEscapesHTML(t *testing.T) {
	environment := environmentFixture(t, 1, `<script>alert("inventory")</script>`)
	store := &memoryReader{records: map[domain.RecordRef]domain.Record{environment.Reference(): environment}}
	handler := New(store, testScope, testToken)
	target := "/api/v1/inventory/record?kind=Environment&id=" + envID + "&revision=1"
	response := request(handler, http.MethodGet, target, "Bearer "+testToken)
	body := response.Body.String()
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
	for _, forbidden := range []string{"tenantScope", "endpointRef", "credentialRef", "identityFingerprint", "connectionRef", "<script>"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response contains %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, `\u003cscript\u003e`) {
		t.Fatalf("HTML-sensitive text was not JSON escaped: %s", body)
	}
}

func TestDiffSurfacesRevisionFactsRelationsAndConfirmedPresenceChanges(t *testing.T) {
	env1 := environmentFixture(t, 1, "host-a")
	env2 := environmentFixture(t, 2, "host-a")
	before := snapshotFixture(t, beforeID, env1.Reference(), domain.CompletenessComplete, domain.CoverageComplete,
		[]entityInput{{"common", "old"}, {"peer", "same"}, {"removed", "gone"}}, domain.StatusObserved)
	after := snapshotFixture(t, afterID, env2.Reference(), domain.CompletenessComplete, domain.CoverageComplete,
		[]entityInput{{"common", "new"}, {"peer", "same"}, {"added", "here"}}, domain.StatusInferred)
	store := fixtureReader(env1, env2, before, after)
	response := request(New(store, testScope, testToken), http.MethodGet,
		"/api/v1/inventory/diff?before="+beforeID+"&after="+afterID, "Bearer "+testToken)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var diff diffResponse
	if err := json.Unmarshal(response.Body.Bytes(), &diff); err != nil {
		t.Fatal(err)
	}
	if diff.Direction != "before-to-after" || !diff.Environment.Revision.Changed || diff.Environment.Revision.Before != 1 || diff.Environment.Revision.After != 2 {
		t.Fatalf("environment diff = %+v direction=%q", diff.Environment, diff.Direction)
	}
	if !diff.Coverage.Comparable || len(diff.Entities.Added) != 1 || len(diff.Entities.Removed) != 1 || len(diff.Entities.Changed) != 1 {
		t.Fatalf("entity diff = %+v coverage=%+v", diff.Entities, diff.Coverage)
	}
	if len(diff.Entities.Changed[0].Facts.Changed) != 1 || len(diff.Relations.Changed) != 1 || len(diff.Relations.Added) != 0 || len(diff.Relations.Removed) != 0 {
		t.Fatalf("fact/relation diff = entities %+v relations %+v", diff.Entities.Changed, diff.Relations)
	}
	if strings.Contains(response.Body.String(), testScope) {
		t.Fatal("diff leaked fixed tenant scope")
	}
}

func TestDiffTreatsIncompleteCoverageAsObservationUncertainty(t *testing.T) {
	environment := environmentFixture(t, 1, "host-a")
	partialBefore := snapshotFixture(t, beforeID, environment.Reference(), domain.CompletenessPartial, domain.CoveragePartial,
		[]entityInput{{"common", "same"}}, domain.StatusObserved)
	completeAfter := snapshotFixture(t, afterID, environment.Reference(), domain.CompletenessComplete, domain.CoverageComplete,
		[]entityInput{{"common", "same"}, {"appeared", "value"}}, domain.StatusObserved)
	store := fixtureReader(environment, partialBefore, completeAfter)
	handler := New(store, testScope, testToken)
	forward := request(handler, http.MethodGet, "/api/v1/inventory/diff?before="+beforeID+"&after="+afterID, "Bearer "+testToken)
	var added diffResponse
	if forward.Code != http.StatusOK || json.Unmarshal(forward.Body.Bytes(), &added) != nil {
		t.Fatalf("forward status=%d body=%s", forward.Code, forward.Body.String())
	}
	if added.Coverage.Comparable || len(added.Entities.Added) != 0 || len(added.Entities.NewlyObserved) != 1 {
		t.Fatalf("partial-before entity classification = %+v coverage=%+v", added.Entities, added.Coverage)
	}
	reverse := request(handler, http.MethodGet, "/api/v1/inventory/diff?before="+afterID+"&after="+beforeID, "Bearer "+testToken)
	var missing diffResponse
	if reverse.Code != http.StatusOK || json.Unmarshal(reverse.Body.Bytes(), &missing) != nil {
		t.Fatalf("reverse status=%d body=%s", reverse.Code, reverse.Body.String())
	}
	if missing.Direction != "before-to-after" || len(missing.Entities.Removed) != 0 || len(missing.Entities.NotObserved) != 1 {
		t.Fatalf("partial-after entity classification = %+v", missing.Entities)
	}
}

func TestDiffRejectsChangedPhysicalIdentity(t *testing.T) {
	env1 := environmentFixture(t, 1, "host-a")
	env2 := environmentFixture(t, 2, "host-b")
	env2.Spec.IdentityFingerprint = "sha256:" + strings.Repeat("b", 64)
	env2.Digest = ""
	if err := domain.FinalizeRecord(env2); err != nil {
		t.Fatal(err)
	}
	before := snapshotFixture(t, beforeID, env1.Reference(), domain.CompletenessComplete, domain.CoverageComplete, []entityInput{}, domain.StatusObserved)
	after := snapshotFixture(t, afterID, env2.Reference(), domain.CompletenessComplete, domain.CoverageComplete, []entityInput{}, domain.StatusObserved)
	response := request(New(fixtureReader(env1, env2, before, after), testScope, testToken), http.MethodGet,
		"/api/v1/inventory/diff?before="+beforeID+"&after="+afterID, "Bearer "+testToken)
	if response.Code != http.StatusConflict || strings.Contains(response.Body.String(), env2.Spec.IdentityFingerprint) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func environmentFixture(t *testing.T, revision int64, host string) *domain.Environment {
	t.Helper()
	return environmentFixtureWithID(t, envID, revision, host)
}

func environmentFixtureWithID(t *testing.T, id string, revision int64, host string) *domain.Environment {
	t.Helper()
	environment := &domain.Environment{
		SchemaVersion: domain.SchemaVersion, Kind: domain.KindEnvironment, TenantScope: testScope,
		ID: id, Revision: revision, CreatedAt: testTime,
		Spec: domain.EnvironmentSpec{
			Runtime: domain.RuntimeDocker, EndpointRef: domain.LocalRef{ID: id},
			IdentityFingerprint: "sha256:" + strings.Repeat("a", 64), HostID: host,
			AllowedNamespaces: []string{}, NetworkPolicy: domain.NetworkOffline, Enabled: true,
		},
	}
	if err := domain.FinalizeRecord(environment); err != nil {
		t.Fatal(err)
	}
	return environment
}

type entityInput struct{ nativeID, name string }

func snapshotFixture(t *testing.T, id string, environment domain.RecordRef, completeness domain.Completeness, coverageState domain.CoverageState, inputs []entityInput, relationStatus domain.FactStatus) *domain.InventorySnapshot {
	t.Helper()
	snapshot := &domain.InventorySnapshot{
		SchemaVersion: domain.SchemaVersion, Kind: domain.KindInventorySnapshot, TenantScope: testScope,
		ID: id, Revision: 1, CreatedAt: testTime,
		Spec: domain.SnapshotSpec{
			EnvironmentRef: environment, CollectorVersion: "test", RulesetVersion: "test",
			StartedAt: testTime, FinishedAt: testTime,
			Coverage: []domain.Coverage{{ResourceKind: "containers", State: coverageState}},
			Entities: []domain.Entity{}, Relations: []domain.Relation{}, Issues: []domain.Issue{}, Completeness: completeness,
		},
	}
	if coverageState != domain.CoverageComplete {
		snapshot.Spec.Coverage[0].Reason = domain.IssueForbidden
	}
	for _, input := range inputs {
		identity := domain.Identity{
			Runtime: domain.RuntimeDocker, EnvironmentID: environment.ID, EntityKind: domain.EntityComponent,
			NativeID: input.nativeID, HostID: "host-a", DaemonID: "daemon-a",
		}
		entityID, err := domain.EntityID(identity)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Spec.Entities = append(snapshot.Spec.Entities, domain.Entity{
			EntityID: entityID, EntityKind: domain.EntityComponent, Identity: identity,
			Facts: []domain.Fact{{
				Field: "name", Value: domain.StringValue(input.name), Status: domain.StatusObserved, ObservedAt: testTime,
				Source: domain.Source{Collector: "test", ObjectRef: domain.RecordObjectRef(environment), FieldPath: "name"},
			}},
		})
	}
	if len(snapshot.Spec.Entities) >= 2 {
		self := snapshot.Reference()
		snapshot.Spec.Relations = append(snapshot.Spec.Relations, domain.Relation{
			Type: domain.RelationDependsOn, FromEntityID: snapshot.Spec.Entities[0].EntityID, ToEntityID: snapshot.Spec.Entities[1].EntityID,
			Status: relationStatus, EvidenceRefs: []domain.EntityRef{{SnapshotRef: self, EntityID: snapshot.Spec.Entities[0].EntityID}},
		})
	}
	if err := domain.FinalizeRecord(snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func fixtureReader(records ...domain.Record) *memoryReader {
	reader := &memoryReader{records: make(map[domain.RecordRef]domain.Record)}
	for _, record := range records {
		reader.records[record.Reference()] = record
	}
	return reader
}
