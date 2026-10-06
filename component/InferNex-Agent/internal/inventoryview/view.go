package inventoryview

import (
	"errors"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domain"
)

type environmentView struct {
	SchemaVersion string              `json:"schemaVersion"`
	Kind          domain.Kind         `json:"kind"`
	ID            string              `json:"id"`
	Revision      int64               `json:"revision"`
	CreatedAt     string              `json:"createdAt"`
	Digest        string              `json:"digest"`
	Spec          environmentSpecView `json:"spec"`
}

// environmentSpecView deliberately has no endpoint, credential, connection,
// or identity-fingerprint member. Those values are store/runtime bindings and
// are not dashboard data.
type environmentSpecView struct {
	Runtime           domain.Runtime       `json:"runtime"`
	HostID            string               `json:"hostID,omitempty"`
	ClusterID         string               `json:"clusterID,omitempty"`
	AllowedNamespaces []string             `json:"allowedNamespaces"`
	NetworkPolicy     domain.NetworkPolicy `json:"networkPolicy"`
	Enabled           bool                 `json:"enabled"`
}

type snapshotView struct {
	SchemaVersion string           `json:"schemaVersion"`
	Kind          domain.Kind      `json:"kind"`
	ID            string           `json:"id"`
	Revision      int64            `json:"revision"`
	CreatedAt     string           `json:"createdAt"`
	Digest        string           `json:"digest"`
	Spec          snapshotSpecView `json:"spec"`
}

type snapshotSpecView struct {
	EnvironmentRef      publicRecordRef     `json:"environmentRef"`
	PreviousSnapshotRef *publicRecordRef    `json:"previousSnapshotRef,omitempty"`
	CollectorVersion    string              `json:"collectorVersion"`
	RulesetVersion      string              `json:"rulesetVersion"`
	StartedAt           string              `json:"startedAt"`
	FinishedAt          string              `json:"finishedAt"`
	Coverage            []domain.Coverage   `json:"coverage"`
	Entities            []entityView        `json:"entities"`
	Relations           []relationView      `json:"relations"`
	Issues              []issueView         `json:"issues"`
	OmittedIssueCount   int64               `json:"omittedIssueCount"`
	Completeness        domain.Completeness `json:"completeness"`
}

type entityView struct {
	EntityID   string            `json:"entityId"`
	EntityKind domain.EntityKind `json:"entityKind"`
	Identity   domain.Identity   `json:"identity"`
	Facts      []factView        `json:"facts"`
}

type factView struct {
	Field      string            `json:"field"`
	Value      *domain.FactValue `json:"value,omitempty"`
	Status     domain.FactStatus `json:"status"`
	ObservedAt string            `json:"observedAt"`
	Source     sourceView        `json:"source"`
}

type sourceView struct {
	Collector     string        `json:"collector"`
	ObjectRef     objectRefView `json:"objectRef"`
	ObjectVersion string        `json:"objectVersion,omitempty"`
	FieldPath     string        `json:"fieldPath"`
}

type objectRefView struct {
	Kind        domain.Kind      `json:"kind,omitempty"`
	ID          string           `json:"id,omitempty"`
	Revision    int64            `json:"revision,omitempty"`
	SnapshotRef *publicRecordRef `json:"snapshotRef,omitempty"`
	EntityID    string           `json:"entityId,omitempty"`
}

type entityRefView struct {
	SnapshotRef publicRecordRef `json:"snapshotRef"`
	EntityID    string          `json:"entityId"`
}

type relationView struct {
	Type         domain.RelationType `json:"type"`
	FromEntityID string              `json:"fromEntityId"`
	ToEntityID   string              `json:"toEntityId"`
	EvidenceRefs []entityRefView     `json:"evidenceRefs"`
	Status       domain.FactStatus   `json:"status"`
}

type issueView struct {
	Code        domain.IssueCode  `json:"code"`
	Stage       domain.IssueStage `json:"stage"`
	ObjectRef   *objectRefView    `json:"objectRef,omitempty"`
	Retryable   bool              `json:"retryable"`
	Description string            `json:"description"`
}

func safeRecord(record domain.Record, scope string) (any, error) {
	if record == nil || record.Reference().TenantScope != scope {
		return nil, errors.New("record is outside fixed scope")
	}
	switch value := record.(type) {
	case *domain.Environment:
		return environmentView{
			SchemaVersion: value.SchemaVersion, Kind: value.Kind, ID: value.ID, Revision: value.Revision,
			CreatedAt: value.CreatedAt, Digest: value.Digest,
			Spec: environmentSpecView{
				Runtime: value.Spec.Runtime, HostID: value.Spec.HostID, ClusterID: value.Spec.ClusterID,
				AllowedNamespaces: append([]string(nil), value.Spec.AllowedNamespaces...),
				NetworkPolicy:     value.Spec.NetworkPolicy, Enabled: value.Spec.Enabled,
			},
		}, nil
	case *domain.InventorySnapshot:
		return safeSnapshot(value, scope)
	default:
		return nil, errors.New("unsupported record type")
	}
}

func safeSnapshot(snapshot *domain.InventorySnapshot, scope string) (snapshotView, error) {
	if snapshot == nil || snapshot.TenantScope != scope || snapshot.Spec.EnvironmentRef.TenantScope != scope {
		return snapshotView{}, errors.New("snapshot is outside fixed scope")
	}
	view := snapshotView{
		SchemaVersion: snapshot.SchemaVersion, Kind: snapshot.Kind, ID: snapshot.ID, Revision: snapshot.Revision,
		CreatedAt: snapshot.CreatedAt, Digest: snapshot.Digest,
		Spec: snapshotSpecView{
			EnvironmentRef: publicRef(snapshot.Spec.EnvironmentRef), CollectorVersion: snapshot.Spec.CollectorVersion,
			RulesetVersion: snapshot.Spec.RulesetVersion, StartedAt: snapshot.Spec.StartedAt, FinishedAt: snapshot.Spec.FinishedAt,
			Coverage: append([]domain.Coverage(nil), snapshot.Spec.Coverage...),
			Entities: make([]entityView, 0, len(snapshot.Spec.Entities)), Relations: make([]relationView, 0, len(snapshot.Spec.Relations)),
			Issues: make([]issueView, 0, len(snapshot.Spec.Issues)), OmittedIssueCount: snapshot.Spec.OmittedIssueCount,
			Completeness: snapshot.Spec.Completeness,
		},
	}
	if snapshot.Spec.PreviousSnapshotRef != nil {
		if snapshot.Spec.PreviousSnapshotRef.TenantScope != scope {
			return snapshotView{}, errors.New("snapshot lineage is outside fixed scope")
		}
		ref := publicRef(*snapshot.Spec.PreviousSnapshotRef)
		view.Spec.PreviousSnapshotRef = &ref
	}
	for _, entity := range snapshot.Spec.Entities {
		item := entityView{EntityID: entity.EntityID, EntityKind: entity.EntityKind, Identity: entity.Identity, Facts: make([]factView, 0, len(entity.Facts))}
		for _, fact := range entity.Facts {
			objectRef, err := safeObjectRef(fact.Source.ObjectRef, scope)
			if err != nil {
				return snapshotView{}, err
			}
			item.Facts = append(item.Facts, factView{
				Field: fact.Field, Value: fact.Value, Status: fact.Status, ObservedAt: fact.ObservedAt,
				Source: sourceView{Collector: fact.Source.Collector, ObjectRef: objectRef, ObjectVersion: fact.Source.ObjectVersion, FieldPath: fact.Source.FieldPath},
			})
		}
		view.Spec.Entities = append(view.Spec.Entities, item)
	}
	for _, relation := range snapshot.Spec.Relations {
		item := relationView{
			Type: relation.Type, FromEntityID: relation.FromEntityID, ToEntityID: relation.ToEntityID,
			Status: relation.Status, EvidenceRefs: make([]entityRefView, 0, len(relation.EvidenceRefs)),
		}
		for _, evidence := range relation.EvidenceRefs {
			if evidence.SnapshotRef.TenantScope != scope {
				return snapshotView{}, errors.New("relation evidence is outside fixed scope")
			}
			item.EvidenceRefs = append(item.EvidenceRefs, entityRefView{SnapshotRef: publicRef(evidence.SnapshotRef), EntityID: evidence.EntityID})
		}
		view.Spec.Relations = append(view.Spec.Relations, item)
	}
	for _, issue := range snapshot.Spec.Issues {
		item := issueView{Code: issue.Code, Stage: issue.Stage, Retryable: issue.Retryable, Description: issue.Description}
		if issue.ObjectRef != nil {
			objectRef, err := safeObjectRef(*issue.ObjectRef, scope)
			if err != nil {
				return snapshotView{}, err
			}
			item.ObjectRef = &objectRef
		}
		view.Spec.Issues = append(view.Spec.Issues, item)
	}
	return view, nil
}

func safeObjectRef(ref domain.ObjectRef, scope string) (objectRefView, error) {
	if ref.SnapshotRef != nil {
		if ref.SnapshotRef.TenantScope != scope {
			return objectRefView{}, errors.New("entity reference is outside fixed scope")
		}
		public := publicRef(*ref.SnapshotRef)
		return objectRefView{SnapshotRef: &public, EntityID: ref.EntityID}, nil
	}
	if ref.TenantScope != scope {
		return objectRefView{}, errors.New("record reference is outside fixed scope")
	}
	return objectRefView{Kind: ref.Kind, ID: ref.ID, Revision: ref.Revision}, nil
}
