package domain

import (
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	uuidPattern     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	digestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	entityIDPattern = regexp.MustCompile(`^eid:sha256:[0-9a-f]{64}$`)
)

var factTypes = map[EntityKind]map[string]ValueType{
	EntityHost:                {"name": ValueString, "cpuMilli": ValueInteger, "memoryBytes": ValueInteger, "os": ValueString, "architecture": ValueString, "deviceRequests": ValueStringList},
	EntityNode:                {"name": ValueString, "cpuMilli": ValueInteger, "memoryBytes": ValueInteger, "os": ValueString, "architecture": ValueString, "deviceRequests": ValueStringList},
	EntityContainer:           {"name": ValueString, "imageRef": ValueString, "imageDigest": ValueString, "phase": ValueString, "replicasDeclared": ValueInteger, "replicasObserved": ValueInteger, "nodeRef": ValueString, "ports": ValueStringList, "mountRefs": ValueStringList, "deviceRequests": ValueStringList},
	EntityWorkload:            {"name": ValueString, "imageRef": ValueString, "imageDigest": ValueString, "phase": ValueString, "replicasDeclared": ValueInteger, "replicasObserved": ValueInteger, "nodeRef": ValueString, "ports": ValueStringList, "mountRefs": ValueStringList, "deviceRequests": ValueStringList},
	EntityComponent:           {"name": ValueString, "version": ValueString, "imageDigest": ValueString, "recognizerVersion": ValueString},
	EntityConfiguration:       {"sourceRef": ValueString, "format": ValueString, "templateRevision": ValueString, "parameterNames": ValueStringList, "redacted": ValueBoolean},
	EntityModelReference:      {"mountRef": ValueString, "modelRevision": ValueString, "tokenizerRevision": ValueString, "precision": ValueString},
	EntityEndpoint:            {"serviceRef": ValueString, "ports": ValueStringList, "readyBackends": ValueInteger},
	EntityTopologyDeclaration: {"mode": ValueString, "role": ValueString, "tpDeclared": ValueInteger},
	EntityOwnership:           {"manager": ValueString, "ownerRef": ValueString, "verification": ValueString},
}

func VerifyRecord(record Record) error {
	if err := ValidateRecord(record); err != nil {
		return err
	}
	want, err := ComputeDigest(record)
	if err != nil {
		return err
	}
	var got string
	switch r := record.(type) {
	case *Environment:
		got = r.Digest
	case *InventorySnapshot:
		got = r.Digest
	default:
		return fmt.Errorf("unsupported domain record type")
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return fmt.Errorf("record digest mismatch")
	}
	return nil
}

// FinalizeRecord normalizes fresh snapshot collections, validates all domain
// constraints, and fills digest. A non-empty digest is never normalized.
func FinalizeRecord(record Record) error {
	switch r := record.(type) {
	case *InventorySnapshot:
		if r.Digest != "" {
			return fmt.Errorf("signed snapshot cannot be normalized")
		}
		if err := NormalizeSnapshot(r); err != nil {
			return err
		}
	case *Environment:
		if r.Digest != "" {
			return fmt.Errorf("signed environment cannot be finalized")
		}
	default:
		return fmt.Errorf("unsupported domain record type")
	}
	if err := validateRecord(record, false); err != nil {
		return err
	}
	digest, err := ComputeDigest(record)
	if err != nil {
		return err
	}
	switch r := record.(type) {
	case *Environment:
		r.Digest = digest
	case *InventorySnapshot:
		r.Digest = digest
	}
	return ValidateRecord(record)
}

func ValidateRecord(record Record) error { return validateRecord(record, true) }

func validateRecord(record Record, requireDigest bool) error {
	if record == nil || (reflect.ValueOf(record).Kind() == reflect.Pointer && reflect.ValueOf(record).IsNil()) {
		return fmt.Errorf("record is nil")
	}
	if err := validateAllStrings(reflect.ValueOf(record), "record"); err != nil {
		return err
	}
	switch r := record.(type) {
	case *Environment:
		return validateEnvironment(r, requireDigest)
	case *InventorySnapshot:
		return validateSnapshot(r, requireDigest)
	default:
		return fmt.Errorf("unsupported domain record type")
	}
}

func validateHeader(schema string, kind Kind, scope, id string, revision int64, created, digest string, expected Kind, requireDigest bool) error {
	if schema != SchemaVersion {
		return fmt.Errorf("unsupported schemaVersion")
	}
	if kind != expected {
		return fmt.Errorf("invalid record kind")
	}
	if scope == "" {
		return fmt.Errorf("tenantScope is required")
	}
	if !uuidPattern.MatchString(id) {
		return fmt.Errorf("id must be a canonical UUID")
	}
	if revision <= 0 || revision > MaxSafeInteger {
		return fmt.Errorf("revision must be a positive safe integer")
	}
	if err := validateTimestamp(created); err != nil {
		return fmt.Errorf("createdAt: %w", err)
	}
	if requireDigest && !digestPattern.MatchString(digest) {
		return fmt.Errorf("digest has invalid format")
	}
	if !requireDigest && digest != "" {
		return fmt.Errorf("digest must be empty before finalization")
	}
	return nil
}

func validateEnvironment(e *Environment, requireDigest bool) error {
	if err := validateHeader(e.SchemaVersion, e.Kind, e.TenantScope, e.ID, e.Revision, e.CreatedAt, e.Digest, KindEnvironment, requireDigest); err != nil {
		return err
	}
	s := e.Spec
	if s.Runtime != RuntimeDocker && s.Runtime != RuntimeKubernetes {
		return fmt.Errorf("environment runtime is invalid")
	}
	if !uuidPattern.MatchString(s.EndpointRef.ID) || s.EndpointRef.ID != e.ID {
		return fmt.Errorf("endpointRef must identify this environment")
	}
	if !digestPattern.MatchString(s.IdentityFingerprint) {
		return fmt.Errorf("identityFingerprint has invalid format")
	}
	if s.NetworkPolicy != NetworkOffline && s.NetworkPolicy != NetworkApprovedOnline {
		return fmt.Errorf("networkPolicy is invalid")
	}
	if s.AllowedNamespaces == nil {
		return fmt.Errorf("allowedNamespaces is required")
	}
	switch s.Runtime {
	case RuntimeDocker:
		if s.HostID == "" || s.ClusterID != "" || len(s.AllowedNamespaces) != 0 || s.CredentialRef != nil {
			return fmt.Errorf("docker environment has invalid runtime-specific fields")
		}
	case RuntimeKubernetes:
		if s.ClusterID == "" || s.HostID != "" || len(s.AllowedNamespaces) == 0 || s.CredentialRef == nil {
			return fmt.Errorf("kubernetes environment has invalid runtime-specific fields")
		}
		if !uuidPattern.MatchString(s.CredentialRef.ID) || s.CredentialRef.ID != e.ID {
			return fmt.Errorf("credentialRef must identify this environment")
		}
		seen := map[string]bool{}
		for _, ns := range s.AllowedNamespaces {
			if ns == "" || seen[ns] {
				return fmt.Errorf("allowedNamespaces contains empty or duplicate namespace")
			}
			seen[ns] = true
		}
	}
	return checkEncodedSize(e, MaxSnapshotBytes, "environment")
}

func validateSnapshot(s *InventorySnapshot, requireDigest bool) error {
	if err := validateHeader(s.SchemaVersion, s.Kind, s.TenantScope, s.ID, s.Revision, s.CreatedAt, s.Digest, KindInventorySnapshot, requireDigest); err != nil {
		return err
	}
	if s.Revision != 1 {
		return fmt.Errorf("inventory snapshot revision must be 1")
	}
	self := s.Reference()
	spec := s.Spec
	if err := validateRecordRef(spec.EnvironmentRef, KindEnvironment); err != nil {
		return fmt.Errorf("environmentRef: %w", err)
	}
	if err := AuthorizeScope(s.TenantScope, spec.EnvironmentRef.TenantScope); err != nil {
		return err
	}
	if spec.PreviousSnapshotRef != nil {
		if err := validateRecordRef(*spec.PreviousSnapshotRef, KindInventorySnapshot); err != nil {
			return fmt.Errorf("previousSnapshotRef: %w", err)
		}
		if spec.PreviousSnapshotRef.TenantScope != s.TenantScope || spec.PreviousSnapshotRef.ID == s.ID {
			return fmt.Errorf("previousSnapshotRef is outside this snapshot lineage")
		}
	}
	if spec.CollectorVersion == "" || spec.RulesetVersion == "" {
		return fmt.Errorf("collectorVersion and rulesetVersion are required")
	}
	if err := validateTimestamp(spec.StartedAt); err != nil {
		return fmt.Errorf("startedAt: %w", err)
	}
	if err := validateTimestamp(spec.FinishedAt); err != nil {
		return fmt.Errorf("finishedAt: %w", err)
	}
	start, _ := time.Parse(fixedTimeLayout, spec.StartedAt)
	finish, _ := time.Parse(fixedTimeLayout, spec.FinishedAt)
	if finish.Before(start) {
		return fmt.Errorf("finishedAt precedes startedAt")
	}
	if spec.Coverage == nil || spec.Entities == nil || spec.Relations == nil || spec.Issues == nil {
		return fmt.Errorf("snapshot collection fields are required")
	}
	if len(spec.Coverage) == 0 {
		return fmt.Errorf("coverage must describe at least one requested resource")
	}
	if len(spec.Entities) > MaxEntities {
		return fmt.Errorf("entity limit exceeded")
	}
	if len(spec.Relations) > MaxRelations {
		return fmt.Errorf("relation limit exceeded")
	}
	if len(spec.Issues) > MaxIssues {
		return fmt.Errorf("issue limit exceeded")
	}
	if spec.OmittedIssueCount < 0 || spec.OmittedIssueCount > MaxSafeInteger {
		return fmt.Errorf("omittedIssueCount is invalid")
	}
	if requireDigest {
		if err := validateNormalizedSnapshot(s); err != nil {
			return err
		}
	}
	entities := map[string]Entity{}
	for i := range spec.Entities {
		e := spec.Entities[i]
		if _, ok := entities[e.EntityID]; ok {
			return fmt.Errorf("duplicate entityId")
		}
		if err := validateEntity(e, spec.EnvironmentRef.ID); err != nil {
			return fmt.Errorf("entity[%d]: %w", i, err)
		}
		entities[e.EntityID] = e
	}
	coverageKeys := map[string]bool{}
	hasGap := false
	allFailed := len(spec.Coverage) > 0
	for i, c := range spec.Coverage {
		if c.ResourceKind == "" || c.Count < 0 || c.Count > MaxSafeInteger {
			return fmt.Errorf("coverage[%d] is invalid", i)
		}
		key := c.ResourceKind + "\x00" + c.Namespace
		if coverageKeys[key] {
			return fmt.Errorf("duplicate coverage key")
		}
		coverageKeys[key] = true
		if !validCoverageState(c.State) {
			return fmt.Errorf("coverage[%d] state is invalid", i)
		}
		if c.State != CoverageComplete {
			hasGap = true
		} else {
			allFailed = false
		}
		if c.State == CoveragePartial {
			allFailed = false
		}
		if c.Reason != "" && !validIssueCode(c.Reason) {
			return fmt.Errorf("coverage[%d] reason is invalid", i)
		}
	}
	relationKeys := map[string]bool{}
	for i, r := range spec.Relations {
		key := string(r.Type) + "\x00" + r.FromEntityID + "\x00" + r.ToEntityID
		if relationKeys[key] {
			return fmt.Errorf("duplicate relation key")
		}
		relationKeys[key] = true
		if !validRelation(r.Type) || !validStatus(r.Status) {
			return fmt.Errorf("relation[%d] enum is invalid", i)
		}
		if _, ok := entities[r.FromEntityID]; !ok {
			return fmt.Errorf("relation[%d] has missing from entity", i)
		}
		if _, ok := entities[r.ToEntityID]; !ok {
			return fmt.Errorf("relation[%d] has missing to entity", i)
		}
		if r.EvidenceRefs == nil || len(r.EvidenceRefs) == 0 {
			return fmt.Errorf("relation[%d] evidenceRefs is required", i)
		}
		for _, ref := range r.EvidenceRefs {
			if err := validateEntityRef(ref, self, entities); err != nil {
				return fmt.Errorf("relation[%d] evidence: %w", i, err)
			}
		}
	}
	for i, e := range spec.Entities {
		for j, f := range e.Facts {
			if err := validateObjectRef(f.Source.ObjectRef, self, spec.EnvironmentRef, entities); err != nil {
				return fmt.Errorf("entity[%d] fact[%d] source: %w", i, j, err)
			}
		}
	}
	for i, issue := range spec.Issues {
		if !validIssueCode(issue.Code) || !validIssueStage(issue.Stage) {
			return fmt.Errorf("issue[%d] enum is invalid", i)
		}
		if len([]byte(issue.Description)) > MaxIssueDescription {
			return fmt.Errorf("issue[%d] description exceeds limit", i)
		}
		if issue.Description == "" {
			return fmt.Errorf("issue[%d] description is required", i)
		}
		if issue.ObjectRef != nil {
			if err := validateObjectRef(*issue.ObjectRef, self, spec.EnvironmentRef, entities); err != nil {
				return fmt.Errorf("issue[%d] objectRef: %w", i, err)
			}
		}
	}
	if !validCompleteness(spec.Completeness) {
		return fmt.Errorf("completeness is invalid")
	}
	if spec.Completeness == CompletenessComplete && (hasGap || len(spec.Issues) > 0 || spec.OmittedIssueCount > 0) {
		return fmt.Errorf("complete snapshot contains gaps")
	}
	if spec.Completeness == CompletenessFailed && (len(spec.Entities) > 0 || !allFailed) {
		return fmt.Errorf("failed snapshot contains valid entities or nonfailed coverage")
	}
	if spec.Completeness == CompletenessPartial && !hasGap && len(spec.Issues) == 0 && spec.OmittedIssueCount == 0 {
		return fmt.Errorf("partial snapshot has no recorded gap")
	}
	return checkEncodedSize(s, MaxSnapshotBytes, "snapshot")
}

func validateNormalizedSnapshot(s *InventorySnapshot) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	var normalized InventorySnapshot
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return err
	}
	normalized.Digest = ""
	if err := NormalizeSnapshot(&normalized); err != nil {
		return err
	}
	want, err := canonicalValue(normalized.Spec)
	if err != nil {
		return err
	}
	got, err := canonicalValue(s.Spec)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("snapshot collections are not in pd-json-v1 normalized order")
	}
	return nil
}

func validateEntity(e Entity, environmentID string) error {
	if !entityIDPattern.MatchString(e.EntityID) {
		return fmt.Errorf("entityId has invalid format")
	}
	if e.EntityKind != e.Identity.EntityKind {
		return fmt.Errorf("entityKind does not match identity")
	}
	if err := validateIdentityShape(e.Identity, environmentID); err != nil {
		return err
	}
	want, err := EntityID(e.Identity)
	if err != nil {
		return err
	}
	if e.EntityID != want {
		return fmt.Errorf("entityId does not match identity")
	}
	if e.Facts == nil {
		return fmt.Errorf("facts is required")
	}
	if len(e.Facts) > MaxFactsPerEntity {
		return fmt.Errorf("fact limit exceeded")
	}
	for i, f := range e.Facts {
		if err := validateFact(f, e.EntityKind); err != nil {
			return fmt.Errorf("fact[%d]: %w", i, err)
		}
	}
	return checkEncodedSize(e, MaxEntityBytes, "entity")
}

func validateIdentityShape(id Identity, environmentID string) error {
	if _, ok := factTypes[id.EntityKind]; !ok {
		return fmt.Errorf("entityKind is invalid")
	}
	if id.EnvironmentID == "" || id.NativeID == "" {
		return fmt.Errorf("identity required field is empty")
	}
	if environmentID != "" && id.EnvironmentID != environmentID {
		return fmt.Errorf("identity belongs to another environment")
	}
	switch id.Runtime {
	case RuntimeDocker:
		if id.HostID == "" || id.DaemonID == "" || id.ClusterID != "" || id.Namespace != nil || id.APIVersion != "" || id.ResourceKind != "" || id.UID != "" {
			return fmt.Errorf("docker identity shape is invalid")
		}
	case RuntimeKubernetes:
		if id.ClusterID == "" || id.Namespace == nil || id.APIVersion == "" || id.ResourceKind == "" || id.UID == "" || id.HostID != "" || id.DaemonID != "" {
			return fmt.Errorf("kubernetes identity shape is invalid")
		}
	default:
		return fmt.Errorf("identity runtime is invalid")
	}
	return nil
}

func validateFact(f Fact, kind EntityKind) error {
	t, ok := factTypes[kind][f.Field]
	if !ok {
		return fmt.Errorf("field is not allowed for entityKind")
	}
	if !validStatus(f.Status) {
		return fmt.Errorf("status is invalid")
	}
	if err := validateTimestamp(f.ObservedAt); err != nil {
		return fmt.Errorf("observedAt: %w", err)
	}
	if f.Source.Collector == "" || f.Source.FieldPath == "" {
		return fmt.Errorf("source collector and fieldPath are required")
	}
	if f.Status == StatusUnknown {
		if f.Value != nil {
			return fmt.Errorf("unknown fact must omit value")
		}
		return nil
	}
	if f.Value == nil {
		return fmt.Errorf("non-unknown fact requires value")
	}
	if err := validateFactValue(*f.Value, t); err != nil {
		return err
	}
	if kind == EntityConfiguration && f.Field == "parameterNames" {
		for _, name := range *f.Value.StringListValue {
			if name != Redacted && sensitiveParameterName(name) {
				return fmt.Errorf("sensitive parameter name must be redacted")
			}
		}
	}
	if kind == EntityTopologyDeclaration && (f.Field == "mode" || f.Field == "role") && f.Status != StatusDeclared && f.Status != StatusInferred && f.Status != StatusConflict {
		return fmt.Errorf("topology declaration cannot be observed without a validated rule")
	}
	return nil
}

func sensitiveParameterName(name string) bool {
	n := strings.ToLower(strings.ReplaceAll(name, "-", "_"))
	for _, marker := range []string{"token", "password", "passwd", "secret", "api_key", "apikey", "credential", "private_key"} {
		if strings.Contains(n, marker) {
			return true
		}
	}
	return false
}

func validateFactValue(v FactValue, expected ValueType) error {
	if v.Type != expected {
		return fmt.Errorf("fact value type does not match field")
	}
	n := 0
	if v.StringValue != nil {
		n++
	}
	if v.IntegerValue != nil {
		n++
	}
	if v.BooleanValue != nil {
		n++
	}
	if v.StringListValue != nil {
		n++
	}
	if n != 1 {
		return fmt.Errorf("fact value must contain exactly one tagged member")
	}
	switch v.Type {
	case ValueString:
		if v.StringValue == nil {
			return fmt.Errorf("stringValue is required")
		}
	case ValueInteger:
		if v.IntegerValue == nil || *v.IntegerValue < MinSafeInteger || *v.IntegerValue > MaxSafeInteger {
			return fmt.Errorf("integerValue is invalid")
		}
	case ValueBoolean:
		if v.BooleanValue == nil {
			return fmt.Errorf("booleanValue is required")
		}
	case ValueStringList:
		if v.StringListValue == nil {
			return fmt.Errorf("stringListValue is required")
		}
	default:
		return fmt.Errorf("fact value type is invalid")
	}
	return nil
}

func validateRecordRef(r RecordRef, kind Kind) error {
	if r.TenantScope == "" || r.Kind != kind || !uuidPattern.MatchString(r.ID) || r.Revision <= 0 || r.Revision > MaxSafeInteger {
		return fmt.Errorf("record reference is incomplete or invalid")
	}
	if kind == KindInventorySnapshot && r.Revision != 1 {
		return fmt.Errorf("snapshot reference revision must be 1")
	}
	return nil
}
func validateEntityRef(r EntityRef, self RecordRef, entities map[string]Entity) error {
	if !sameRecordRef(r.SnapshotRef, self) {
		return fmt.Errorf("entity reference points outside current snapshot")
	}
	if _, ok := entities[r.EntityID]; !ok {
		return fmt.Errorf("entity reference target is missing")
	}
	return nil
}
func validateObjectRef(r ObjectRef, self, environment RecordRef, entities map[string]Entity) error {
	isEntity := r.SnapshotRef != nil || r.EntityID != ""
	isRecord := r.TenantScope != "" || r.Kind != "" || r.ID != "" || r.Revision != 0
	if isEntity == isRecord {
		return fmt.Errorf("objectRef must select exactly one reference shape")
	}
	if isEntity {
		if r.SnapshotRef == nil || r.EntityID == "" {
			return fmt.Errorf("entity objectRef is incomplete")
		}
		return validateEntityRef(EntityRef{SnapshotRef: *r.SnapshotRef, EntityID: r.EntityID}, self, entities)
	}
	rr := RecordRef{TenantScope: r.TenantScope, Kind: r.Kind, ID: r.ID, Revision: r.Revision}
	if !sameRecordRef(rr, environment) {
		return fmt.Errorf("record objectRef is not the registered environment")
	}
	return nil
}

const fixedTimeLayout = "2006-01-02T15:04:05.000000000Z"

// FormatTimestamp converts collector times to the only timestamp form accepted
// by pd-json-v1 records.
func FormatTimestamp(t time.Time) string { return t.UTC().Format(fixedTimeLayout) }

func validateTimestamp(s string) error {
	if len(s) != 30 || !strings.HasSuffix(s, "Z") {
		return fmt.Errorf("must use RFC3339 UTC with exactly nine fractional digits")
	}
	t, err := time.Parse(fixedTimeLayout, s)
	if err != nil || t.Format(fixedTimeLayout) != s {
		return fmt.Errorf("must use RFC3339 UTC with exactly nine fractional digits")
	}
	return nil
}
func validStatus(v FactStatus) bool {
	return v == StatusObserved || v == StatusDeclared || v == StatusInferred || v == StatusUnknown || v == StatusConflict
}
func validRelation(v RelationType) bool {
	return v == RelationRunsOn || v == RelationManagedBy || v == RelationConfiguredBy || v == RelationDependsOn || v == RelationRoutesTo || v == RelationUsesModel
}
func validCoverageState(v CoverageState) bool {
	return v == CoverageComplete || v == CoveragePartial || v == CoverageForbidden || v == CoverageUnsupported || v == CoverageTimeout || v == CoverageFailed
}
func validCompleteness(v Completeness) bool {
	return v == CompletenessComplete || v == CompletenessPartial || v == CompletenessFailed
}
func validIssueCode(v IssueCode) bool {
	switch v {
	case IssueForbidden, IssueUnavailable, IssueTimeout, IssueNotFound, IssueUnsupported, IssueIdentityChanged, IssueConflict, IssueLimitExceeded, IssueCursorExpired, IssueUnparsed, IssueRedacted:
		return true
	}
	return false
}
func validIssueStage(v IssueStage) bool {
	return v == StageConnect || v == StageList || v == StageInspect || v == StageNormalize
}

func validateAllStrings(v reflect.Value, path string) error {
	if !v.IsValid() {
		return nil
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		return validateAllStrings(v.Elem(), path)
	}
	switch v.Kind() {
	case reflect.String:
		s := v.String()
		if !utf8.ValidString(s) {
			return fmt.Errorf("%s contains invalid UTF-8", path)
		}
		if len([]byte(s)) > MaxStringBytes {
			return fmt.Errorf("%s exceeds string limit", path)
		}
		if s != Redacted && ContainsSecretMaterial(s) {
			return fmt.Errorf("%s contains prohibited secret material", path)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if err := validateAllStrings(v.Field(i), path+"."+v.Type().Field(i).Name); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if err := validateAllStrings(v.Index(i), path); err != nil {
				return err
			}
		}
	}
	return nil
}
func checkEncodedSize(v any, limit int, label string) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", label, err)
	}
	canonical, err := CanonicalizeJSON(raw, CanonicalOptions{})
	if err != nil {
		return err
	}
	if len(canonical) > limit {
		return fmt.Errorf("%s exceeds byte limit", label)
	}
	return nil
}

func validDigestString(s string) bool {
	if !digestPattern.MatchString(s) {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(s, DigestPrefix))
	return err == nil
}
