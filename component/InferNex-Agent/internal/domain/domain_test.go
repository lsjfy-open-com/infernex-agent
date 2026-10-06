package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeValidDomainRecords(t *testing.T) {
	files := []string{"schema-v1-environment.json", "schema-v1-environment-k8s.json", "schema-v1-snapshot-docker-aggregate.json"}
	for _, name := range files {
		t.Run(name, func(t *testing.T) {
			raw := fixture(t, name)
			record, err := DecodeRecord(raw)
			if err != nil {
				t.Fatal(err)
			}
			first, err := CanonicalRecord(record)
			if err != nil {
				t.Fatal(err)
			}
			second, err := CanonicalRecord(record)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(first, second) {
				t.Fatal("canonical record bytes changed")
			}
			if err := VerifyRecord(record); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDecodeRejectsUnknownFieldAndTrailingValue(t *testing.T) {
	raw := fixture(t, "schema-v1-environment.json")
	cases := map[string][]byte{
		"unknown":              bytes.Replace(raw, []byte(`"enabled": true`), []byte(`"enabled": true, "extra": 1`), 1),
		"duplicate":            bytes.Replace(raw, []byte(`"enabled": true`), []byte(`"enabled": true, "enabled": true`), 1),
		"null":                 bytes.Replace(raw, []byte(`"enabled": true`), []byte(`"enabled": null`), 1),
		"missing":              bytes.Replace(raw, []byte(`"enabled": true`), []byte(`"notEnabled": true`), 1),
		"trailing":             append(append([]byte{}, raw...), []byte(` {}`)...),
		"top-level case alias": bytes.Replace(raw, []byte(`"kind": "Environment"`), []byte(`"kind": "Environment", "Kind": "Environment"`), 1),
		"nested case alias":    bytes.Replace(raw, []byte(`"enabled": true`), []byte(`"enabled": true, "Enabled": false`), 1),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeRecord(input); err == nil {
				t.Fatal("expected strict decode failure")
			}
		})
	}
}

func TestNegativeFixturesAreRejected(t *testing.T) {
	for _, name := range []string{"environment-unknown-field.json", "environment-case-alias-top.json", "environment-case-alias-nested.json", "record-wrong-schema.json", "record-tampered-digest.json", "duplicate-key.json", "refs-cross-tenant.json", "refs-missing-revision.json", "raw-secret.json"} {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeRecord(fixture(t, name))
			if err == nil {
				t.Fatal("accepted negative fixture")
			}
			if strings.Contains(err.Error(), "DO_NOT_LEAK_TOKEN_7E31") {
				t.Fatal("secret marker leaked through error")
			}
		})
	}
	if _, err := DecodeRecord(fixture(t, "refs-same-scope.json")); err != nil {
		t.Fatalf("same-scope reference fixture failed: %v", err)
	}
}

func TestCanonicalizationGoldenVectorsAndStrictNumbers(t *testing.T) {
	input := []byte("{\"中文\":\"值\",\"z\":\"line\\n<>&\\u2028\\u2029\",\"digest\":\"drop\",\"nested\":{\"x\":9007199254740991,\"digest\":\"keep\"},\"a\":null,\"arr\":[3,2,1]}")
	want := []byte("{\"a\":null,\"arr\":[3,2,1],\"nested\":{\"digest\":\"keep\",\"x\":9007199254740991},\"z\":\"line\\u000a<>&  \",\"中文\":\"值\"}")
	got, err := CanonicalizeJSON(input, CanonicalOptions{ExcludeRootDigest: true})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("canonical mismatch\n got %q\nwant %q", got, want)
	}
	sum := sha256.Sum256(got)
	if actual := hex.EncodeToString(sum[:]); actual != "0719ac53c007909a1ef1a83292a61cf780a9dafc6b565c1247c6706df4dc8394" {
		t.Fatalf("golden digest changed: %s", actual)
	}
	valid := []string{"-9007199254740991", "0", "9007199254740991"}
	for _, s := range valid {
		if _, err := CanonicalizeJSON([]byte(s), CanonicalOptions{}); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	invalid := [][]byte{[]byte("-9007199254740992"), []byte("9007199254740992"), []byte("1.0"), []byte("1e3"), []byte(`{"a":1,"\u0061":2}`), []byte(`"\ud800"`), {0x22, 0xff, 0x22}}
	for _, raw := range invalid {
		if _, err := CanonicalizeJSON(raw, CanonicalOptions{}); err == nil {
			t.Errorf("accepted invalid input %q", raw)
		}
	}
	maliciousKey := []byte(`{"DO_NOT_LEAK_TOKEN_7E31":1,"DO_NOT_LEAK_TOKEN_7E31":2}`)
	if _, err := CanonicalizeJSON(maliciousKey, CanonicalOptions{}); err == nil || strings.Contains(err.Error(), "DO_NOT_LEAK") {
		t.Fatalf("duplicate-key error was absent or leaked key: %v", err)
	}
}

func TestVerifyRejectsSchemaAndDigestTamper(t *testing.T) {
	env := mustEnvironment(t)
	env.Spec.HostID = "changed"
	if err := VerifyRecord(env); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("expected digest mismatch, got %v", err)
	}
	env = mustEnvironment(t)
	env.SchemaVersion = "private-deployment/v2"
	if err := VerifyRecord(env); err == nil {
		t.Fatal("accepted unknown schema")
	}
	s := mustSnapshot(t, "schema-v1-snapshot-docker-aggregate.json")
	s.Revision = 2
	if err := VerifyRecord(s); err == nil {
		t.Fatal("accepted mutable snapshot revision")
	}
	s = mustSnapshot(t, "schema-v1-snapshot-docker-aggregate.json")
	s.Spec.Entities[0].Identity.NativeID = "tampered"
	if err := VerifyRecord(s); err == nil || !strings.Contains(err.Error(), "entityId") {
		t.Fatalf("expected entity identity rejection, got %v", err)
	}
}

func TestReferencesStayInsideTenantScope(t *testing.T) {
	s := mustSnapshot(t, "schema-v1-snapshot-docker-aggregate.json")
	s.Spec.EnvironmentRef.TenantScope = "other"
	resign(t, s)
	if err := VerifyRecord(s); err == nil {
		t.Fatal("accepted cross-scope environmentRef")
	}
	s = mustSnapshot(t, "schema-v1-snapshot-docker-aggregate.json")
	s.Spec.Relations[0].EvidenceRefs[0].SnapshotRef.ID = "33333333-3333-4333-8333-333333333333"
	resign(t, s)
	if err := VerifyRecord(s); err == nil {
		t.Fatal("accepted cross-snapshot entity reference")
	}
	s = mustSnapshot(t, "schema-v1-snapshot-docker-aggregate.json")
	src := &s.Spec.Entities[0].Facts[0].Source.ObjectRef
	ref := s.Reference()
	src.SnapshotRef = &ref
	src.EntityID = s.Spec.Entities[0].EntityID
	resign(t, s)
	if err := VerifyRecord(s); err == nil {
		t.Fatal("accepted objectRef with both union arms")
	}
	if err := AuthorizeScope("synthetic-scope", "other"); err == nil {
		t.Fatal("accepted unauthorized scope")
	}
}

func TestEnvironmentAuthorizationScope(t *testing.T) {
	if err := AuthorizeScope("synthetic-scope", "synthetic-scope"); err != nil {
		t.Fatal(err)
	}
	for _, scopes := range [][2]string{{"", "synthetic-scope"}, {"synthetic-scope", ""}, {"synthetic-scope", "other"}} {
		if err := AuthorizeScope(scopes[0], scopes[1]); err == nil {
			t.Fatalf("authorized scopes %#v", scopes)
		}
	}
}

func TestEmbeddedEntityAndRelationIntegrity(t *testing.T) {
	tests := map[string]func(*InventorySnapshot){
		"duplicate entity":   func(s *InventorySnapshot) { s.Spec.Entities = append(s.Spec.Entities, s.Spec.Entities[0]) },
		"dangling relation":  func(s *InventorySnapshot) { s.Spec.Relations[0].ToEntityID = "eid:sha256:" + strings.Repeat("0", 64) },
		"duplicate relation": func(s *InventorySnapshot) { s.Spec.Relations = append(s.Spec.Relations, s.Spec.Relations[0]) },
		"duplicate coverage": func(s *InventorySnapshot) { s.Spec.Coverage = append(s.Spec.Coverage, s.Spec.Coverage[0]) },
		"complete forbidden": func(s *InventorySnapshot) {
			s.Spec.Coverage[0].State = CoverageForbidden
			s.Spec.Coverage[0].Reason = IssueForbidden
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			s := mustSnapshot(t, "schema-v1-snapshot-docker-aggregate.json")
			mutate(s)
			resign(t, s)
			if err := VerifyRecord(s); err == nil {
				t.Fatal("accepted invalid snapshot")
			}
		})
	}
}

func TestTypedEntityWhitelistAndEnums(t *testing.T) {
	for kind, fields := range factTypes {
		for field, valueType := range fields {
			value := valueForType(valueType)
			status := StatusObserved
			if kind == EntityTopologyDeclaration && (field == "mode" || field == "role") {
				status = StatusDeclared
			}
			fact := Fact{Field: field, Value: value, Status: status, ObservedAt: "2026-10-02T01:02:03.000000000Z", Source: Source{Collector: "test", FieldPath: "fixture.field"}}
			if err := validateFact(fact, kind); err != nil {
				t.Errorf("%s.%s: %v", kind, field, err)
			}
		}
	}
	s := mustSnapshot(t, "schema-v1-snapshot-docker-aggregate.json")
	s.Spec.Entities[0].Facts[0].Field = "command"
	resign(t, s)
	if err := VerifyRecord(s); err == nil {
		t.Fatal("accepted unknown fact field")
	}
	s = mustSnapshot(t, "schema-v1-snapshot-docker-aggregate.json")
	v := int64(1)
	s.Spec.Entities[0].Facts[0].Value = &FactValue{Type: ValueString, StringValue: StringValue("x").StringValue, IntegerValue: &v}
	resign(t, s)
	if err := VerifyRecord(s); err == nil {
		t.Fatal("accepted malformed tagged union")
	}
	s = mustSnapshot(t, "schema-v1-snapshot-docker-aggregate.json")
	s.Spec.Relations[0].Type = "transfersKV"
	resign(t, s)
	if err := VerifyRecord(s); err == nil {
		t.Fatal("accepted future relation")
	}
}

func valueForType(valueType ValueType) *FactValue {
	switch valueType {
	case ValueString:
		return StringValue("synthetic")
	case ValueInteger:
		return IntegerValue(1)
	case ValueBoolean:
		return BooleanValue(true)
	case ValueStringList:
		return StringListValue([]string{"synthetic"})
	default:
		panic("unknown test value type")
	}
}

func TestSecretValuesNeverEnterDomainJSON(t *testing.T) {
	marker := "DO_NOT_LEAK_TOKEN_7E31"
	s := mustSnapshot(t, "schema-v1-snapshot-docker-aggregate.json")
	s.Digest = ""
	s.Spec.Entities[0].Facts[0].Value = StringValue(marker)
	err := FinalizeRecord(s)
	if err == nil {
		t.Fatal("accepted secret material")
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatal("secret leaked in error")
	}
	if got := RedactString("https://user:password@example.invalid/path"); got != Redacted {
		t.Fatalf("redaction failed: %q", got)
	}
	parameterNames := Fact{Field: "parameterNames", Value: StringListValue([]string{"API_TOKEN"}), Status: StatusObserved, ObservedAt: "2026-10-02T01:02:03.000000000Z", Source: Source{Collector: "test", FieldPath: "fixture.parameters"}}
	if err := validateFact(parameterNames, EntityConfiguration); err == nil {
		t.Fatal("accepted sensitive parameter name")
	}
}

func TestCanonicalizationRejectsDeepNestingAndTypedInvalidUTF8(t *testing.T) {
	deep := []byte(strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65))
	if _, err := CanonicalizeJSON(deep, CanonicalOptions{}); err == nil {
		t.Fatal("accepted excessive nesting")
	}
	if _, err := CanonicalizeJSON(make([]byte, MaxSnapshotBytes+1), CanonicalOptions{}); err == nil {
		t.Fatal("accepted oversized canonical input")
	}
	env := mustEnvironment(t)
	env.Spec.HostID = string([]byte{0xff})
	if _, err := CanonicalRecord(env); err == nil {
		t.Fatal("silently replaced invalid UTF-8 in typed record")
	}
	var nilEnv *Environment
	if _, err := CanonicalRecord(nilEnv); err == nil {
		t.Fatal("accepted typed nil record")
	}
}

func TestFourCombinationFixturesMapToEmbeddedEntities(t *testing.T) {
	for _, runtime := range []string{"docker", "k8s"} {
		for _, mode := range []string{"aggregate", "pd"} {
			name := "schema-v1-snapshot-" + runtime + "-" + mode + ".json"
			s, err := DecodeInventorySnapshot(fixture(t, name))
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			seen := map[EntityKind]bool{}
			for _, e := range s.Spec.Entities {
				seen[e.EntityKind] = true
			}
			for _, kind := range []EntityKind{EntityComponent, EntityTopologyDeclaration, EntityOwnership} {
				if !seen[kind] {
					t.Errorf("%s missing %s", name, kind)
				}
			}
		}
	}
}

func TestValidationBudgets(t *testing.T) {
	env := mustEnvironment(t)
	env.Digest = ""
	env.Spec.HostID = strings.Repeat("h", MaxStringBytes)
	if err := FinalizeRecord(env); err != nil {
		t.Fatalf("max string rejected: %v", err)
	}
	env = mustEnvironment(t)
	env.Digest = ""
	env.Spec.HostID = strings.Repeat("h", MaxStringBytes+1)
	if err := FinalizeRecord(env); err == nil {
		t.Fatal("accepted overlong string")
	}
	s := mustSnapshot(t, "schema-v1-snapshot-docker-aggregate.json")
	s.Digest = ""
	s.Spec.Relations = make([]Relation, MaxRelations+1)
	if err := FinalizeRecord(s); err == nil {
		t.Fatal("accepted excess relations")
	}
	s = mustSnapshot(t, "schema-v1-snapshot-docker-aggregate.json")
	s.Digest = ""
	s.Spec.Issues = make([]Issue, MaxIssues+1)
	if err := FinalizeRecord(s); err == nil {
		t.Fatal("accepted excess issues")
	}
	s = mustSnapshot(t, "schema-v1-snapshot-docker-aggregate.json")
	s.Digest = ""
	s.Spec.Entities = make([]Entity, MaxEntities+1)
	if err := FinalizeRecord(s); err == nil {
		t.Fatal("accepted excess entities")
	}
	identity := Identity{Runtime: RuntimeDocker, EnvironmentID: "11111111-1111-4111-8111-111111111111", EntityKind: EntityHost, NativeID: "budget-host", HostID: "synthetic-host", DaemonID: "synthetic-daemon"}
	eid, err := EntityID(identity)
	if err != nil {
		t.Fatal(err)
	}
	makeEntity := func(n int) Entity {
		values := make([]string, n)
		for i := range values {
			values[i] = strings.Repeat("x", MaxStringBytes)
		}
		return Entity{EntityID: eid, EntityKind: EntityHost, Identity: identity, Facts: []Fact{{Field: "deviceRequests", Value: StringListValue(values), Status: StatusObserved, ObservedAt: "2026-10-02T01:02:03.000000000Z", Source: Source{Collector: "test", FieldPath: "fixture.devices"}}}}
	}
	if err := validateEntity(makeEntity(15), identity.EnvironmentID); err != nil {
		t.Fatalf("entity below 64 KiB rejected: %v", err)
	}
	if err := validateEntity(makeEntity(16), identity.EnvironmentID); err == nil {
		t.Fatal("entity above 64 KiB accepted")
	}
}

func TestDomainHasNoBridgeImports(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(info os.FileInfo) bool {
		return strings.HasSuffix(info.Name(), ".go") && !strings.HasSuffix(info.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if strings.HasPrefix(path, "gitcode.com/openFuyao/InferNex/") {
					t.Errorf("domain production file imports repository package %s", path)
				}
			}
		}
	}
}

func TestNormalizationFreshOnlyAndConflictFacts(t *testing.T) {
	s := mustSnapshot(t, "schema-v1-snapshot-docker-aggregate.json")
	if err := NormalizeSnapshot(s); err == nil {
		t.Fatal("normalized signed record")
	}
	s = mustSnapshot(t, "schema-v1-snapshot-docker-aggregate.json")
	s.Digest = ""
	e := &s.Spec.Entities[0]
	other := e.Facts[0]
	other.Value = StringValue("pd")
	e.Facts = append(e.Facts, e.Facts[0], other)
	if err := NormalizeSnapshot(s); err != nil {
		t.Fatal(err)
	}
	if len(e.Facts) != 2 || e.Facts[0].Status != StatusConflict || e.Facts[1].Status != StatusConflict {
		t.Fatalf("facts not normalized as conflict: %#v", e.Facts)
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func mustEnvironment(t *testing.T) *Environment {
	t.Helper()
	v, err := DecodeEnvironment(fixture(t, "schema-v1-environment.json"))
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func mustSnapshot(t *testing.T, name string) *InventorySnapshot {
	t.Helper()
	v, err := DecodeInventorySnapshot(fixture(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func resign(t *testing.T, r Record) {
	t.Helper()
	switch v := r.(type) {
	case *Environment:
		v.Digest = ""
		d, err := ComputeDigest(v)
		if err != nil {
			t.Fatal(err)
		}
		v.Digest = d
	case *InventorySnapshot:
		v.Digest = ""
		d, err := ComputeDigest(v)
		if err != nil {
			t.Fatal(err)
		}
		v.Digest = d
	}
}
