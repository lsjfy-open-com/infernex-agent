package inventoryview

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domain"
)

type diffResponse struct {
	Before      publicRecordRef `json:"before"`
	After       publicRecordRef `json:"after"`
	Direction   string          `json:"direction"`
	Environment diffEnvironment `json:"environment"`
	Coverage    diffCoverage    `json:"coverage"`
	Entities    entityChanges   `json:"entities"`
	Relations   relationChanges `json:"relations"`
}

type diffEnvironment struct {
	ID       string         `json:"id"`
	Runtime  domain.Runtime `json:"runtime"`
	Revision revisionChange `json:"revision"`
}

type revisionChange struct {
	Before  int64 `json:"before"`
	After   int64 `json:"after"`
	Changed bool  `json:"changed"`
}

type diffCoverage struct {
	Comparable     bool `json:"comparable"`
	BeforeComplete bool `json:"beforeComplete"`
	AfterComplete  bool `json:"afterComplete"`
}

type entitySummary struct {
	EntityID   string            `json:"entityId"`
	EntityKind domain.EntityKind `json:"entityKind"`
	Identity   domain.Identity   `json:"identity"`
}

type entityChanges struct {
	Added         []entitySummary `json:"added"`
	NewlyObserved []entitySummary `json:"newlyObserved"`
	Changed       []entityChange  `json:"changed"`
	Removed       []entitySummary `json:"removed"`
	NotObserved   []entitySummary `json:"notObserved"`
}

type entityChange struct {
	Identity       domain.Identity `json:"identity"`
	BeforeEntityID string          `json:"beforeEntityId"`
	AfterEntityID  string          `json:"afterEntityId"`
	Facts          factChanges     `json:"facts"`
}

type factChanges struct {
	Added         []factView   `json:"added"`
	NewlyObserved []factView   `json:"newlyObserved"`
	Changed       []factChange `json:"changed"`
	Removed       []factView   `json:"removed"`
	NotObserved   []factView   `json:"notObserved"`
}

type factChange struct {
	Field  string   `json:"field"`
	Before factView `json:"before"`
	After  factView `json:"after"`
}

type relationSummary struct {
	Type         domain.RelationType `json:"type"`
	FromEntityID string              `json:"fromEntityId"`
	ToEntityID   string              `json:"toEntityId"`
	Status       domain.FactStatus   `json:"status"`
}

type relationChanges struct {
	Added         []relationSummary `json:"added"`
	NewlyObserved []relationSummary `json:"newlyObserved"`
	Changed       []relationChange  `json:"changed"`
	Removed       []relationSummary `json:"removed"`
	NotObserved   []relationSummary `json:"notObserved"`
}

type relationChange struct {
	Before relationSummary `json:"before"`
	After  relationSummary `json:"after"`
}

func (s *server) serveDiff(w http.ResponseWriter, r *http.Request) {
	query, err := strictQuery(r.URL, "before", "after")
	if err != nil || len(query) != 2 || !canonicalUUID.MatchString(query.Get("before")) || !canonicalUUID.MatchString(query.Get("after")) {
		writeError(w, http.StatusBadRequest, "invalid_request", "before and after must be canonical snapshot UUIDs")
		return
	}
	beforeRef := domain.RecordRef{TenantScope: s.scope, Kind: domain.KindInventorySnapshot, ID: query.Get("before"), Revision: 1}
	afterRef := domain.RecordRef{TenantScope: s.scope, Kind: domain.KindInventorySnapshot, ID: query.Get("after"), Revision: 1}
	before, err := s.getSnapshot(r, beforeRef)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	after, err := s.getSnapshot(r, afterRef)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if before.Spec.EnvironmentRef.TenantScope != s.scope || after.Spec.EnvironmentRef.TenantScope != s.scope ||
		!validRef(before.Spec.EnvironmentRef, domain.KindEnvironment) || !validRef(after.Spec.EnvironmentRef, domain.KindEnvironment) {
		writeError(w, http.StatusInternalServerError, "server_error", "snapshot environment binding is invalid")
		return
	}
	if before.Spec.EnvironmentRef.ID != after.Spec.EnvironmentRef.ID {
		writeError(w, http.StatusConflict, "incomparable_snapshots", "snapshots belong to different environments")
		return
	}
	beforeEnvironment, err := s.getEnvironment(r, before.Spec.EnvironmentRef)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	afterEnvironment := beforeEnvironment
	if before.Spec.EnvironmentRef != after.Spec.EnvironmentRef {
		afterEnvironment, err = s.getEnvironment(r, after.Spec.EnvironmentRef)
		if err != nil {
			writeStoreError(w, err)
			return
		}
	}
	if beforeEnvironment.ID != afterEnvironment.ID || beforeEnvironment.Spec.Runtime != afterEnvironment.Spec.Runtime ||
		beforeEnvironment.Spec.IdentityFingerprint != afterEnvironment.Spec.IdentityFingerprint ||
		beforeEnvironment.Spec.HostID != afterEnvironment.Spec.HostID || beforeEnvironment.Spec.ClusterID != afterEnvironment.Spec.ClusterID {
		writeError(w, http.StatusConflict, "incomparable_snapshots", "environment runtime or physical identity changed")
		return
	}

	comparable := coverageComparable(before, after)
	output := diffResponse{
		Before: publicRef(beforeRef), After: publicRef(afterRef), Direction: "before-to-after",
		Environment: diffEnvironment{
			ID: beforeEnvironment.ID, Runtime: beforeEnvironment.Spec.Runtime,
			Revision: revisionChange{Before: beforeEnvironment.Revision, After: afterEnvironment.Revision, Changed: beforeEnvironment.Revision != afterEnvironment.Revision},
		},
		Coverage: diffCoverage{Comparable: comparable, BeforeComplete: snapshotComplete(before), AfterComplete: snapshotComplete(after)},
		Entities: emptyEntityChanges(), Relations: emptyRelationChanges(),
	}
	if err = buildEntityDiff(&output.Entities, before, after, comparable, s.scope); err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "snapshot facts could not be rendered")
		return
	}
	buildRelationDiff(&output.Relations, before, after, comparable)
	writeBoundedJSON(w, http.StatusOK, output)
}

func (s *server) getSnapshot(r *http.Request, ref domain.RecordRef) (*domain.InventorySnapshot, error) {
	record, err := s.store.Get(r.Context(), s.scope, ref)
	if err != nil {
		return nil, err
	}
	snapshot, ok := record.(*domain.InventorySnapshot)
	if !ok || snapshot == nil || snapshot.Reference() != ref {
		return nil, &viewStoreError{message: "invalid snapshot record"}
	}
	return snapshot, nil
}

func (s *server) getEnvironment(r *http.Request, ref domain.RecordRef) (*domain.Environment, error) {
	record, err := s.store.Get(r.Context(), s.scope, ref)
	if err != nil {
		return nil, err
	}
	environment, ok := record.(*domain.Environment)
	if !ok || environment == nil || environment.Reference() != ref {
		return nil, &viewStoreError{message: "invalid environment record"}
	}
	return environment, nil
}

// viewStoreError is deliberately opaque at the HTTP boundary.
type viewStoreError struct{ message string }

func (e *viewStoreError) Error() string { return e.message }

func snapshotComplete(snapshot *domain.InventorySnapshot) bool {
	if snapshot == nil || snapshot.Spec.Completeness != domain.CompletenessComplete || snapshot.Spec.PreviousSnapshotRef != nil || len(snapshot.Spec.Coverage) == 0 {
		return false
	}
	for _, coverage := range snapshot.Spec.Coverage {
		if coverage.State != domain.CoverageComplete {
			return false
		}
	}
	return true
}

// coverageComparable is intentionally conservative. An absence is a
// confirmed addition/removal only when both captures are complete standalone
// observations of exactly the same resource-kind/namespace set.
func coverageComparable(before, after *domain.InventorySnapshot) bool {
	if !snapshotComplete(before) || !snapshotComplete(after) || len(before.Spec.Coverage) != len(after.Spec.Coverage) {
		return false
	}
	beforeKeys := make([]string, 0, len(before.Spec.Coverage))
	afterKeys := make([]string, 0, len(after.Spec.Coverage))
	for _, item := range before.Spec.Coverage {
		beforeKeys = append(beforeKeys, item.ResourceKind+"\x00"+item.Namespace)
	}
	for _, item := range after.Spec.Coverage {
		afterKeys = append(afterKeys, item.ResourceKind+"\x00"+item.Namespace)
	}
	sort.Strings(beforeKeys)
	sort.Strings(afterKeys)
	for index := range beforeKeys {
		if beforeKeys[index] != afterKeys[index] {
			return false
		}
	}
	return true
}

func emptyEntityChanges() entityChanges {
	return entityChanges{
		Added: []entitySummary{}, NewlyObserved: []entitySummary{}, Changed: []entityChange{},
		Removed: []entitySummary{}, NotObserved: []entitySummary{},
	}
}

func emptyRelationChanges() relationChanges {
	return relationChanges{
		Added: []relationSummary{}, NewlyObserved: []relationSummary{}, Changed: []relationChange{},
		Removed: []relationSummary{}, NotObserved: []relationSummary{},
	}
}

func buildEntityDiff(output *entityChanges, before, after *domain.InventorySnapshot, comparable bool, scope string) error {
	beforeByID := make(map[string]domain.Entity, len(before.Spec.Entities))
	afterByID := make(map[string]domain.Entity, len(after.Spec.Entities))
	for _, entity := range before.Spec.Entities {
		beforeByID[entity.EntityID] = entity
	}
	for _, entity := range after.Spec.Entities {
		afterByID[entity.EntityID] = entity
	}
	for _, entity := range before.Spec.Entities {
		afterEntity, present := afterByID[entity.EntityID]
		if !present {
			summary := summarizeEntity(entity)
			if comparable {
				output.Removed = append(output.Removed, summary)
			} else {
				output.NotObserved = append(output.NotObserved, summary)
			}
			continue
		}
		facts, err := diffFacts(entity.Facts, afterEntity.Facts, comparable, scope)
		if err != nil {
			return err
		}
		if hasFactChanges(facts) {
			output.Changed = append(output.Changed, entityChange{
				Identity: afterEntity.Identity, BeforeEntityID: entity.EntityID, AfterEntityID: afterEntity.EntityID, Facts: facts,
			})
		}
	}
	for _, entity := range after.Spec.Entities {
		if _, present := beforeByID[entity.EntityID]; present {
			continue
		}
		summary := summarizeEntity(entity)
		if comparable {
			output.Added = append(output.Added, summary)
		} else {
			output.NewlyObserved = append(output.NewlyObserved, summary)
		}
	}
	return nil
}

func summarizeEntity(entity domain.Entity) entitySummary {
	return entitySummary{EntityID: entity.EntityID, EntityKind: entity.EntityKind, Identity: entity.Identity}
}

func diffFacts(before, after []domain.Fact, comparable bool, scope string) (factChanges, error) {
	output := factChanges{Added: []factView{}, NewlyObserved: []factView{}, Changed: []factChange{}, Removed: []factView{}, NotObserved: []factView{}}
	beforeItems, err := indexFacts(before)
	if err != nil {
		return output, err
	}
	afterItems, err := indexFacts(after)
	if err != nil {
		return output, err
	}
	for _, item := range beforeItems {
		other, present := findFact(afterItems, item.key)
		beforeView, viewErr := safeFact(item.fact, scope)
		if viewErr != nil {
			return output, viewErr
		}
		if !present {
			if comparable {
				output.Removed = append(output.Removed, beforeView)
			} else {
				output.NotObserved = append(output.NotObserved, beforeView)
			}
			continue
		}
		if !sameFactMeaning(item.fact, other.fact) {
			afterView, viewErr := safeFact(other.fact, scope)
			if viewErr != nil {
				return output, viewErr
			}
			output.Changed = append(output.Changed, factChange{Field: item.fact.Field, Before: beforeView, After: afterView})
		}
	}
	for _, item := range afterItems {
		if _, present := findFact(beforeItems, item.key); present {
			continue
		}
		view, viewErr := safeFact(item.fact, scope)
		if viewErr != nil {
			return output, viewErr
		}
		if comparable {
			output.Added = append(output.Added, view)
		} else {
			output.NewlyObserved = append(output.NewlyObserved, view)
		}
	}
	return output, nil
}

type keyedFact struct {
	key  string
	fact domain.Fact
}

func indexFacts(facts []domain.Fact) ([]keyedFact, error) {
	items := make([]keyedFact, 0, len(facts))
	seen := make(map[string]int, len(facts))
	for _, fact := range facts {
		key := fact.Field + "\x00" + fact.Source.Collector + "\x00" + fact.Source.FieldPath + "\x00" + objectIdentity(fact.Source.ObjectRef)
		occurrence := seen[key]
		seen[key]++
		// Valid snapshots may contain conflicting facts from one source. Preserve
		// each occurrence deterministically instead of collapsing it in a map.
		items = append(items, keyedFact{key: key + "\x00" + string(rune(occurrence)), fact: fact})
	}
	return items, nil
}

func findFact(items []keyedFact, key string) (keyedFact, bool) {
	for _, item := range items {
		if item.key == key {
			return item, true
		}
	}
	return keyedFact{}, false
}

func objectIdentity(ref domain.ObjectRef) string {
	if ref.SnapshotRef != nil {
		return "entity\x00" + ref.EntityID
	}
	return "record\x00" + string(ref.Kind) + "\x00" + ref.ID
}

func sameFactMeaning(before, after domain.Fact) bool {
	left, _ := json.Marshal(struct {
		Value  *domain.FactValue
		Status domain.FactStatus
	}{before.Value, before.Status})
	right, _ := json.Marshal(struct {
		Value  *domain.FactValue
		Status domain.FactStatus
	}{after.Value, after.Status})
	return string(left) == string(right)
}

func safeFact(fact domain.Fact, scope string) (factView, error) {
	ref, err := safeObjectRef(fact.Source.ObjectRef, scope)
	if err != nil {
		return factView{}, err
	}
	return factView{
		Field: fact.Field, Value: fact.Value, Status: fact.Status, ObservedAt: fact.ObservedAt,
		Source: sourceView{Collector: fact.Source.Collector, ObjectRef: ref, ObjectVersion: fact.Source.ObjectVersion, FieldPath: fact.Source.FieldPath},
	}, nil
}

func hasFactChanges(changes factChanges) bool {
	return len(changes.Added)+len(changes.NewlyObserved)+len(changes.Changed)+len(changes.Removed)+len(changes.NotObserved) > 0
}

func buildRelationDiff(output *relationChanges, before, after *domain.InventorySnapshot, comparable bool) {
	beforeByKey := make(map[string]domain.Relation, len(before.Spec.Relations))
	afterByKey := make(map[string]domain.Relation, len(after.Spec.Relations))
	for _, relation := range before.Spec.Relations {
		beforeByKey[relationKey(relation)] = relation
	}
	for _, relation := range after.Spec.Relations {
		afterByKey[relationKey(relation)] = relation
	}
	for _, relation := range before.Spec.Relations {
		afterRelation, present := afterByKey[relationKey(relation)]
		if !present {
			if comparable {
				output.Removed = append(output.Removed, summarizeRelation(relation))
			} else {
				output.NotObserved = append(output.NotObserved, summarizeRelation(relation))
			}
			continue
		}
		if relation.Status != afterRelation.Status {
			output.Changed = append(output.Changed, relationChange{Before: summarizeRelation(relation), After: summarizeRelation(afterRelation)})
		}
	}
	for _, relation := range after.Spec.Relations {
		if _, present := beforeByKey[relationKey(relation)]; present {
			continue
		}
		if comparable {
			output.Added = append(output.Added, summarizeRelation(relation))
		} else {
			output.NewlyObserved = append(output.NewlyObserved, summarizeRelation(relation))
		}
	}
}

func relationKey(relation domain.Relation) string {
	return strings.Join([]string{string(relation.Type), relation.FromEntityID, relation.ToEntityID}, "\x00")
}

func summarizeRelation(relation domain.Relation) relationSummary {
	return relationSummary{Type: relation.Type, FromEntityID: relation.FromEntityID, ToEntityID: relation.ToEntityID, Status: relation.Status}
}
