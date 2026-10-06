// Package domain defines the versioned, runtime-neutral records used by private
// deployment inventory. It deliberately has no persistence or runtime adapter
// dependencies.
package domain

import "time"

const (
	SchemaVersion = "private-deployment/v1"
	DigestPrefix  = "sha256:"
	EntityPrefix  = "eid:sha256:"

	MaxScanDuration             = 30 * time.Second
	MaxEntities                 = 300
	MaxRawResponseBytes         = 2 << 20
	MaxEntityBytes              = 64 << 10
	MaxSnapshotBytes            = 8 << 20
	MaxFactsPerEntity           = 128
	MaxRelations                = 1200
	MaxStringBytes              = 4096
	MaxIssues                   = 100
	MaxIssueDescription         = 512
	DefaultPageSize             = 20
	MaxPageSize                 = 100
	MaxToolResponseBytes        = 256 << 10
	MaxPreviewsPerSubject       = 4
	MaxPreviewsPerService       = 16
	PreviewTTL                  = 5 * time.Minute
	MaxSafeInteger        int64 = 9007199254740991
	MinSafeInteger        int64 = -9007199254740991
)

type Kind string

const (
	KindEnvironment       Kind = "Environment"
	KindInventorySnapshot Kind = "InventorySnapshot"
)

type Runtime string

const (
	RuntimeDocker     Runtime = "docker"
	RuntimeKubernetes Runtime = "kubernetes"
)

type NetworkPolicy string

const (
	NetworkOffline        NetworkPolicy = "offline"
	NetworkApprovedOnline NetworkPolicy = "approved-online"
)

type EntityKind string

const (
	EntityHost                EntityKind = "host"
	EntityNode                EntityKind = "node"
	EntityContainer           EntityKind = "container"
	EntityWorkload            EntityKind = "workload"
	EntityComponent           EntityKind = "component"
	EntityConfiguration       EntityKind = "configuration"
	EntityModelReference      EntityKind = "model-reference"
	EntityEndpoint            EntityKind = "endpoint"
	EntityTopologyDeclaration EntityKind = "topology-declaration"
	EntityOwnership           EntityKind = "ownership"
)

type FactStatus string

const (
	StatusObserved FactStatus = "observed"
	StatusDeclared FactStatus = "declared"
	StatusInferred FactStatus = "inferred"
	StatusUnknown  FactStatus = "unknown"
	StatusConflict FactStatus = "conflict"
)

type ValueType string

const (
	ValueString     ValueType = "string"
	ValueInteger    ValueType = "integer"
	ValueBoolean    ValueType = "boolean"
	ValueStringList ValueType = "string-list"
)

type RelationType string

const (
	RelationRunsOn       RelationType = "runsOn"
	RelationManagedBy    RelationType = "managedBy"
	RelationConfiguredBy RelationType = "configuredBy"
	RelationDependsOn    RelationType = "dependsOn"
	RelationRoutesTo     RelationType = "routesTo"
	RelationUsesModel    RelationType = "usesModel"
)

type CoverageState string

const (
	CoverageComplete    CoverageState = "complete"
	CoveragePartial     CoverageState = "partial"
	CoverageForbidden   CoverageState = "forbidden"
	CoverageUnsupported CoverageState = "unsupported"
	CoverageTimeout     CoverageState = "timeout"
	CoverageFailed      CoverageState = "failed"
)

type Completeness string

const (
	CompletenessComplete Completeness = "complete"
	CompletenessPartial  Completeness = "partial"
	CompletenessFailed   Completeness = "failed"
)

type IssueCode string

const (
	IssueForbidden       IssueCode = "forbidden"
	IssueUnavailable     IssueCode = "unavailable"
	IssueTimeout         IssueCode = "timeout"
	IssueNotFound        IssueCode = "not_found"
	IssueUnsupported     IssueCode = "unsupported"
	IssueIdentityChanged IssueCode = "identity_changed"
	IssueConflict        IssueCode = "conflict"
	IssueLimitExceeded   IssueCode = "limit_exceeded"
	IssueCursorExpired   IssueCode = "cursor_expired"
	IssueUnparsed        IssueCode = "unparsed"
	IssueRedacted        IssueCode = "redacted"
)

type IssueStage string

const (
	StageConnect   IssueStage = "connect"
	StageList      IssueStage = "list"
	StageInspect   IssueStage = "inspect"
	StageNormalize IssueStage = "normalize"
)

// Record is intentionally closed by the unexported marker method.
type Record interface {
	isDomainRecord()
	Reference() RecordRef
}

type RecordRef struct {
	TenantScope string `json:"tenantScope"`
	Kind        Kind   `json:"kind"`
	ID          string `json:"id"`
	Revision    int64  `json:"revision"`
}

type EntityRef struct {
	SnapshotRef RecordRef `json:"snapshotRef"`
	EntityID    string    `json:"entityId"`
}

// ObjectRef is a strict direct-shape union: either the four RecordRef fields,
// or snapshotRef+entityId. No wrapper field appears in JSON.
type ObjectRef struct {
	TenantScope string     `json:"tenantScope,omitempty"`
	Kind        Kind       `json:"kind,omitempty"`
	ID          string     `json:"id,omitempty"`
	Revision    int64      `json:"revision,omitempty"`
	SnapshotRef *RecordRef `json:"snapshotRef,omitempty"`
	EntityID    string     `json:"entityId,omitempty"`
}

func RecordObjectRef(ref RecordRef) ObjectRef {
	return ObjectRef{TenantScope: ref.TenantScope, Kind: ref.Kind, ID: ref.ID, Revision: ref.Revision}
}

func EntityObjectRef(ref EntityRef) ObjectRef {
	return ObjectRef{SnapshotRef: &ref.SnapshotRef, EntityID: ref.EntityID}
}

type LocalRef struct {
	ID string `json:"id"`
}

type Environment struct {
	SchemaVersion string          `json:"schemaVersion"`
	Kind          Kind            `json:"kind"`
	TenantScope   string          `json:"tenantScope"`
	ID            string          `json:"id"`
	Revision      int64           `json:"revision"`
	CreatedAt     string          `json:"createdAt"`
	Digest        string          `json:"digest"`
	Spec          EnvironmentSpec `json:"spec"`
}

func (*Environment) isDomainRecord() {}
func (e *Environment) Reference() RecordRef {
	return RecordRef{TenantScope: e.TenantScope, Kind: e.Kind, ID: e.ID, Revision: e.Revision}
}

type EnvironmentSpec struct {
	Runtime             Runtime       `json:"runtime"`
	EndpointRef         LocalRef      `json:"endpointRef"`
	CredentialRef       *LocalRef     `json:"credentialRef,omitempty"`
	IdentityFingerprint string        `json:"identityFingerprint"`
	HostID              string        `json:"hostID,omitempty"`
	ClusterID           string        `json:"clusterID,omitempty"`
	AllowedNamespaces   []string      `json:"allowedNamespaces"`
	NetworkPolicy       NetworkPolicy `json:"networkPolicy"`
	Enabled             bool          `json:"enabled"`
}

type InventorySnapshot struct {
	SchemaVersion string       `json:"schemaVersion"`
	Kind          Kind         `json:"kind"`
	TenantScope   string       `json:"tenantScope"`
	ID            string       `json:"id"`
	Revision      int64        `json:"revision"`
	CreatedAt     string       `json:"createdAt"`
	Digest        string       `json:"digest"`
	Spec          SnapshotSpec `json:"spec"`
}

func (*InventorySnapshot) isDomainRecord() {}
func (s *InventorySnapshot) Reference() RecordRef {
	return RecordRef{TenantScope: s.TenantScope, Kind: s.Kind, ID: s.ID, Revision: s.Revision}
}

type SnapshotSpec struct {
	EnvironmentRef      RecordRef    `json:"environmentRef"`
	PreviousSnapshotRef *RecordRef   `json:"previousSnapshotRef,omitempty"`
	CollectorVersion    string       `json:"collectorVersion"`
	RulesetVersion      string       `json:"rulesetVersion"`
	StartedAt           string       `json:"startedAt"`
	FinishedAt          string       `json:"finishedAt"`
	Coverage            []Coverage   `json:"coverage"`
	Entities            []Entity     `json:"entities"`
	Relations           []Relation   `json:"relations"`
	Issues              []Issue      `json:"issues"`
	OmittedIssueCount   int64        `json:"omittedIssueCount"`
	Completeness        Completeness `json:"completeness"`
}

type Entity struct {
	EntityID   string     `json:"entityId"`
	EntityKind EntityKind `json:"entityKind"`
	Identity   Identity   `json:"identity"`
	Facts      []Fact     `json:"facts"`
}

type Identity struct {
	Runtime       Runtime    `json:"runtime"`
	EnvironmentID string     `json:"environmentId"`
	EntityKind    EntityKind `json:"entityKind"`
	NativeID      string     `json:"nativeId"`
	HostID        string     `json:"hostID,omitempty"`
	DaemonID      string     `json:"daemonID,omitempty"`
	ClusterID     string     `json:"clusterID,omitempty"`
	Namespace     *string    `json:"namespace,omitempty"`
	APIVersion    string     `json:"apiVersion,omitempty"`
	ResourceKind  string     `json:"resourceKind,omitempty"`
	UID           string     `json:"uid,omitempty"`
}

type Fact struct {
	Field      string     `json:"field"`
	Value      *FactValue `json:"value,omitempty"`
	Status     FactStatus `json:"status"`
	ObservedAt string     `json:"observedAt"`
	Source     Source     `json:"source"`
}

// FactValue uses pointers so false, zero, and an empty list remain distinct
// from an absent member of the tagged union.
type FactValue struct {
	Type            ValueType `json:"type"`
	StringValue     *string   `json:"stringValue,omitempty"`
	IntegerValue    *int64    `json:"integerValue,omitempty"`
	BooleanValue    *bool     `json:"booleanValue,omitempty"`
	StringListValue *[]string `json:"stringListValue,omitempty"`
}

func StringValue(v string) *FactValue { return &FactValue{Type: ValueString, StringValue: &v} }
func IntegerValue(v int64) *FactValue { return &FactValue{Type: ValueInteger, IntegerValue: &v} }
func BooleanValue(v bool) *FactValue  { return &FactValue{Type: ValueBoolean, BooleanValue: &v} }
func StringListValue(v []string) *FactValue {
	return &FactValue{Type: ValueStringList, StringListValue: &v}
}

type Source struct {
	Collector     string    `json:"collector"`
	ObjectRef     ObjectRef `json:"objectRef"`
	ObjectVersion string    `json:"objectVersion,omitempty"`
	FieldPath     string    `json:"fieldPath"`
}

type Relation struct {
	Type         RelationType `json:"type"`
	FromEntityID string       `json:"fromEntityId"`
	ToEntityID   string       `json:"toEntityId"`
	EvidenceRefs []EntityRef  `json:"evidenceRefs"`
	Status       FactStatus   `json:"status"`
}

type Coverage struct {
	ResourceKind string        `json:"resourceKind"`
	Namespace    string        `json:"namespace"`
	Count        int64         `json:"count"`
	State        CoverageState `json:"state"`
	Reason       IssueCode     `json:"reason,omitempty"`
}

type Issue struct {
	Code        IssueCode  `json:"code"`
	Stage       IssueStage `json:"stage"`
	ObjectRef   *ObjectRef `json:"objectRef,omitempty"`
	Retryable   bool       `json:"retryable"`
	Description string     `json:"description"`
}
