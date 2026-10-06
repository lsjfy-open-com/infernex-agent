package dockerdiscovery

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domain"
)

func normalizeContainer(snapshot *domain.InventorySnapshot, environment domain.Environment, daemonID string, inspect containerInspect, observedAt string) (domain.Entity, []domain.Issue, bool) {
	identity := domain.Identity{
		Runtime:       domain.RuntimeDocker,
		EnvironmentID: environment.ID,
		EntityKind:    domain.EntityContainer,
		NativeID:      inspect.id,
		HostID:        environment.Spec.HostID,
		DaemonID:      daemonID,
	}
	entityID, err := domain.EntityID(identity)
	if err != nil {
		return domain.Entity{}, []domain.Issue{normalizationIssue(domain.IssueUnparsed)}, false
	}
	self := domain.EntityRef{SnapshotRef: snapshot.Reference(), EntityID: entityID}
	source := func(fieldPath string) domain.Source {
		return domain.Source{
			Collector: collectorVersion,
			ObjectRef: domain.EntityObjectRef(self),
			FieldPath: fieldPath,
		}
	}

	issues := make([]domain.Issue, 0, 2)
	redacted := false
	limited := false
	facts := make([]domain.Fact, 0, 7)
	addScalarFact := func(field, value, path string) {
		projected, wasRedacted, ok := safeString(value)
		redacted = redacted || wasRedacted
		if !ok {
			limited = true
			facts = append(facts, unknownFact(field, observedAt, source(path)))
			return
		}
		if value == "" {
			facts = append(facts, unknownFact(field, observedAt, source(path)))
			return
		}
		facts = append(facts, observedFact(field, domain.StringValue(projected), observedAt, source(path)))
	}
	addListFact := func(field string, values []string, path string) {
		projected, wasRedacted, ok := safeStringList(values)
		redacted = redacted || wasRedacted
		if !ok {
			limited = true
			facts = append(facts, unknownFact(field, observedAt, source(path)))
			return
		}
		facts = append(facts, observedFact(field, domain.StringListValue(projected), observedAt, source(path)))
	}

	addScalarFact("name", inspect.name, "Name")
	addScalarFact("imageRef", inspect.imageReference, "Config.Image")
	addScalarFact("imageDigest", inspect.imageID, "Image")
	addScalarFact("phase", inspect.state, "State.Status")
	addListFact("ports", inspect.ports, "NetworkSettings.Ports")
	addListFact("mountRefs", projectMounts(inspect.mounts), "Mounts")
	addListFact("deviceRequests", projectDeviceRequests(inspect.deviceRequests), "HostConfig.DeviceRequests")

	entity := domain.Entity{
		EntityID:   entityID,
		EntityKind: domain.EntityContainer,
		Identity:   identity,
		Facts:      facts,
	}
	if limited {
		issues = append(issues, normalizationIssue(domain.IssueLimitExceeded))
	}
	if redacted {
		issues = append(issues, normalizationIssue(domain.IssueRedacted))
	}
	encoded, err := json.Marshal(entity)
	if err != nil || len(encoded) > domain.MaxEntityBytes || len(entity.Facts) > domain.MaxFactsPerEntity {
		issues = append(issues, normalizationIssue(domain.IssueLimitExceeded))
		return domain.Entity{}, issues, false
	}
	return entity, issues, true
}

func observedFact(field string, value *domain.FactValue, observedAt string, source domain.Source) domain.Fact {
	return domain.Fact{Field: field, Value: value, Status: domain.StatusObserved, ObservedAt: observedAt, Source: source}
}

func unknownFact(field, observedAt string, source domain.Source) domain.Fact {
	return domain.Fact{Field: field, Status: domain.StatusUnknown, ObservedAt: observedAt, Source: source}
}

func safeString(value string) (string, bool, bool) {
	if len([]byte(value)) > domain.MaxStringBytes {
		return "", false, false
	}
	projected := domain.RedactString(value)
	return projected, projected != value, true
}

func safeStringList(values []string) ([]string, bool, bool) {
	projected := make([]string, 0, len(values))
	redacted := false
	for _, value := range values {
		item, itemRedacted, ok := safeString(value)
		if !ok {
			return nil, redacted, false
		}
		redacted = redacted || itemRedacted
		projected = append(projected, item)
	}
	sort.Strings(projected)
	projected = compactStrings(projected)
	if projected == nil {
		projected = make([]string, 0)
	}
	return projected, redacted, true
}

func compactStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func projectMounts(mounts []mountProjection) []string {
	result := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		reference := mount.name
		if reference == "" {
			reference = mount.destination
		}
		if reference != "" {
			result = append(result, reference)
		}
	}
	return result
}

func projectDeviceRequests(requests []deviceRequestProjection) []string {
	result := make([]string, 0, len(requests))
	for _, request := range requests {
		parts := []string{"count=" + strconv.FormatInt(request.count, 10)}
		if request.driver != "" {
			parts = append(parts, "driver="+request.driver)
		}
		capabilities := make([]string, 0)
		for _, set := range request.capabilities {
			capabilities = append(capabilities, set...)
		}
		if len(capabilities) > 0 {
			sort.Strings(capabilities)
			parts = append(parts, "capabilities="+strings.Join(compactStrings(capabilities), ","))
		}
		if len(request.deviceIDs) > 0 {
			ids := append([]string(nil), request.deviceIDs...)
			sort.Strings(ids)
			parts = append(parts, "deviceIDs="+strings.Join(compactStrings(ids), ","))
		}
		result = append(result, strings.Join(parts, ";"))
	}
	return result
}

func normalizationIssue(code domain.IssueCode) domain.Issue {
	return domain.Issue{
		Code:        code,
		Stage:       domain.StageNormalize,
		Retryable:   false,
		Description: fmt.Sprintf("docker discovery normalize ended with %s", code),
	}
}
