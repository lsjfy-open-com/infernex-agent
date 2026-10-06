package kubernetesdiscovery

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domain"
)

func normalizeObject(snapshot *domain.InventorySnapshot, env domain.Environment, task resourceTask, obj map[string]any, observedAt string) ([]domain.Entity, []domain.Issue) {
	redacted, limited := projectionHazards(obj)
	projectionIssues := make([]domain.Issue, 0, 2)
	if redacted {
		projectionIssues = append(projectionIssues, domain.Issue{Code: domain.IssueRedacted, Stage: domain.StageNormalize, Description: "sensitive Kubernetes fields were excluded from the domain projection"})
	}
	if limited {
		projectionIssues = append(projectionIssues, domain.Issue{Code: domain.IssueLimitExceeded, Stage: domain.StageNormalize, Description: "oversized Kubernetes fields were excluded from the domain projection"})
	}
	meta, _ := obj["metadata"].(map[string]any)
	uid := stringField(meta, "uid")
	name := stringField(meta, "name")
	ns := stringField(meta, "namespace")
	rv := stringField(meta, "resourceVersion")
	apiVersion := stringField(obj, "apiVersion")
	resourceKind := stringField(obj, "kind")
	if uid == "" || name == "" || rv == "" || apiVersion == "" || resourceKind == "" {
		return nil, []domain.Issue{{Code: domain.IssueUnparsed, Stage: domain.StageNormalize, Retryable: false, Description: "Kubernetes object omitted because identity fields were missing"}}
	}
	if !allValidUTF8(uid, name, ns, rv, apiVersion, resourceKind) {
		return nil, []domain.Issue{{Code: domain.IssueUnparsed, Stage: domain.StageNormalize, Description: "Kubernetes object omitted because identity fields were not valid UTF-8"}}
	}
	if apiVersion != task.groupVersion || resourceKind != expectedKind(task.resource) {
		return nil, []domain.Issue{{Code: domain.IssueConflict, Stage: domain.StageNormalize, Description: "Kubernetes object type did not match the requested resource"}}
	}
	if task.namespace != "" && ns != task.namespace {
		return nil, []domain.Issue{{Code: domain.IssueConflict, Stage: domain.StageNormalize, Description: "Kubernetes object namespace did not match the authorized read"}}
	}
	kind := entityKind(task.resource)
	namespace := ns
	identity := domain.Identity{Runtime: domain.RuntimeKubernetes, EnvironmentID: env.ID, EntityKind: kind, NativeID: uid, ClusterID: env.Spec.ClusterID, Namespace: &namespace, APIVersion: apiVersion, ResourceKind: resourceKind, UID: uid}
	eid, err := domain.EntityID(identity)
	if err != nil {
		return nil, []domain.Issue{{Code: domain.IssueUnparsed, Stage: domain.StageNormalize, Description: "Kubernetes identity could not be normalized"}}
	}
	self := domain.EntityRef{SnapshotRef: snapshot.Reference(), EntityID: eid}
	source := func(path string) domain.Source {
		return domain.Source{Collector: collectorVersion, ObjectRef: domain.EntityObjectRef(self), ObjectVersion: rv, FieldPath: path}
	}
	facts := []domain.Fact{}
	if kind == domain.EntityWorkload || kind == domain.EntityNode {
		facts = append(facts, observed("name", domain.StringValue(safe(name)), observedAt, source("metadata.name")))
	}
	spec, _ := obj["spec"].(map[string]any)
	status, _ := obj["status"].(map[string]any)
	switch task.resource {
	case "deployments", "statefulsets":
		facts = append(facts, optionalInteger("replicasDeclared", spec["replicas"], observedAt, source("spec.replicas")), optionalInteger("replicasObserved", status["readyReplicas"], observedAt, source("status.readyReplicas")))
		if refs := imageRefs(spec); len(refs) > 0 {
			facts = append(facts, observed("imageRef", domain.StringValue(safe(strings.Join(refs, ","))), observedAt, source("spec.template.spec.containers[].image")))
		}
	case "pods":
		facts = append(facts, optionalString("phase", stringField(status, "phase"), observedAt, source("status.phase")), optionalString("nodeRef", stringField(spec, "nodeName"), observedAt, source("spec.nodeName")))
		if refs := imageRefs(map[string]any{"template": map[string]any{"spec": spec}}); len(refs) > 0 {
			facts = append(facts, observed("imageRef", domain.StringValue(safe(strings.Join(refs, ","))), observedAt, source("spec.containers[].image")))
		}
	case "services":
		facts = append(facts, observed("serviceRef", domain.StringValue(safe("Service/"+ns+"/"+name)), observedAt, source("metadata")))
		if ports := ports(spec); len(ports) > 0 {
			facts = append(facts, observed("ports", domain.StringListValue(ports), observedAt, source("spec.ports")))
		}
	case "endpointslices":
		serviceName := label(meta, "kubernetes.io/service-name")
		serviceRef := ""
		if serviceName != "" {
			serviceRef = safe("Service/" + ns + "/" + serviceName)
		}
		facts = append(facts, optionalString("serviceRef", serviceRef, observedAt, source("metadata.labels")), observed("readyBackends", domain.IntegerValue(readyBackends(obj)), observedAt, source("endpoints")))
	case "persistentvolumeclaims":
		facts = []domain.Fact{observed("sourceRef", domain.StringValue(safe("PersistentVolumeClaim/"+ns+"/"+name)), observedAt, source("metadata")), observed("format", domain.StringValue("kubernetes-object-reference"), observedAt, source("kind")), observed("redacted", domain.BooleanValue(true), observedAt, source("spec"))}
	case "nodes":
		facts = append(facts, optionalString("os", nestedString(status, "nodeInfo", "osImage"), observedAt, source("status.nodeInfo.osImage")), optionalString("architecture", nestedString(status, "nodeInfo", "architecture"), observedAt, source("status.nodeInfo.architecture")))
	}
	facts = compactFacts(facts)
	entity := domain.Entity{EntityID: eid, EntityKind: kind, Identity: identity, Facts: facts}
	raw, _ := json.Marshal(entity)
	if len(raw) > domain.MaxEntityBytes || len(facts) > domain.MaxFactsPerEntity {
		return nil, []domain.Issue{{Code: domain.IssueLimitExceeded, Stage: domain.StageNormalize, Description: "Kubernetes entity omitted because it exceeded a domain limit"}}
	}
	entities := []domain.Entity{entity}
	if ownership := helmOwnership(snapshot, env, identity, meta, observedAt, source); ownership != nil {
		entities = append(entities, *ownership)
	}
	return entities, projectionIssues
}

func expectedKind(resource string) string {
	switch resource {
	case "deployments":
		return "Deployment"
	case "statefulsets":
		return "StatefulSet"
	case "pods":
		return "Pod"
	case "services":
		return "Service"
	case "endpointslices":
		return "EndpointSlice"
	case "persistentvolumeclaims":
		return "PersistentVolumeClaim"
	case "nodes":
		return "Node"
	}
	return ""
}

func objectIdentityKey(obj map[string]any) (string, string, bool) {
	meta, _ := obj["metadata"].(map[string]any)
	uid, name, namespace := stringField(meta, "uid"), stringField(meta, "name"), stringField(meta, "namespace")
	apiVersion, kind := stringField(obj, "apiVersion"), stringField(obj, "kind")
	if uid == "" || name == "" || apiVersion == "" || kind == "" {
		return "", "", false
	}
	return apiVersion + "\x00" + kind + "\x00" + namespace + "\x00" + name, uid, true
}

func projectionHazards(value any) (redacted, limited bool) {
	type item struct {
		value any
		depth int
	}
	stack := []item{{value: value}}
	const maxProjectionDepth = 64
	const maxProjectionNodes = 100_000
	for visited := 0; len(stack) > 0; visited++ {
		if visited >= maxProjectionNodes {
			limited = true
			break
		}
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		if current.depth > maxProjectionDepth {
			limited = true
			continue
		}
		switch typed := current.value.(type) {
		case map[string]any:
			for key, item := range typed {
				if !utf8.ValidString(key) {
					limited = true
					continue
				}
				lower := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
				if lower == "token" || lower == "password" || lower == "secret" || lower == "authorization" || strings.Contains(lower, "credential") || strings.Contains(lower, "private_key") {
					redacted = true
				}
				stack = append(stack, struct {
					value any
					depth int
				}{value: item, depth: current.depth + 1})
			}
		case []any:
			for _, item := range typed {
				stack = append(stack, struct {
					value any
					depth int
				}{value: item, depth: current.depth + 1})
			}
		case string:
			if !utf8.ValidString(typed) {
				limited = true
				continue
			}
			if domain.ContainsSecretMaterial(typed) {
				redacted = true
			}
			if len([]byte(typed)) > domain.MaxStringBytes {
				limited = true
			}
		}
	}
	return redacted, limited
}

func allValidUTF8(values ...string) bool {
	for _, value := range values {
		if !utf8.ValidString(value) {
			return false
		}
	}
	return true
}

func entityKind(resource string) domain.EntityKind {
	switch resource {
	case "deployments", "statefulsets", "pods":
		return domain.EntityWorkload
	case "services", "endpointslices":
		return domain.EntityEndpoint
	case "persistentvolumeclaims":
		return domain.EntityConfiguration
	case "nodes":
		return domain.EntityNode
	}
	return domain.EntityWorkload
}
func observed(field string, v *domain.FactValue, at string, s domain.Source) domain.Fact {
	return domain.Fact{Field: field, Value: v, Status: domain.StatusObserved, ObservedAt: at, Source: s}
}
func unknown(field, at string, s domain.Source) domain.Fact {
	return domain.Fact{Field: field, Status: domain.StatusUnknown, ObservedAt: at, Source: s}
}
func optionalString(field, value, at string, s domain.Source) domain.Fact {
	if value == "" {
		return unknown(field, at, s)
	}
	return observed(field, domain.StringValue(safe(value)), at, s)
}
func optionalInteger(field string, v any, at string, s domain.Source) domain.Fact {
	n, ok := integer(v)
	if !ok {
		return unknown(field, at, s)
	}
	return observed(field, domain.IntegerValue(n), at, s)
}
func compactFacts(in []domain.Fact) []domain.Fact {
	out := in[:0]
	for _, f := range in {
		if f.Field != "" {
			out = append(out, f)
		}
	}
	return out
}
func safe(s string) string {
	if !utf8.ValidString(s) || len([]byte(s)) > domain.MaxStringBytes {
		return "[REDACTED]"
	}
	return domain.RedactString(s)
}
func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}
func nestedString(m map[string]any, keys ...string) string {
	var v any = m
	for _, k := range keys {
		x, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = x[k]
	}
	s, _ := v.(string)
	return s
}
func integer(v any) (int64, bool) {
	var value int64
	var ok bool
	switch n := v.(type) {
	case int64:
		value, ok = n, true
	case int:
		value, ok = int64(n), true
	case float64:
		if n >= float64(domain.MinSafeInteger) && n <= float64(domain.MaxSafeInteger) && n == float64(int64(n)) {
			value, ok = int64(n), true
		}
	case json.Number:
		var err error
		value, err = n.Int64()
		ok = err == nil
	}
	if !ok || value < domain.MinSafeInteger || value > domain.MaxSafeInteger {
		return 0, false
	}
	return value, true
}
func imageRefs(spec map[string]any) []string {
	template, _ := spec["template"].(map[string]any)
	pod, _ := template["spec"].(map[string]any)
	containers, _ := pod["containers"].([]any)
	var out []string
	for _, c := range containers {
		m, _ := c.(map[string]any)
		if image := safe(stringField(m, "image")); image != "" {
			out = append(out, image)
		}
	}
	sort.Strings(out)
	return out
}
func ports(spec map[string]any) []string {
	items, _ := spec["ports"].([]any)
	var out []string
	for _, p := range items {
		m, _ := p.(map[string]any)
		port, ok := integer(m["port"])
		if ok {
			protocol := strings.ToLower(stringField(m, "protocol"))
			if protocol == "" {
				protocol = "tcp"
			}
			out = append(out, safe(strconv.FormatInt(port, 10)+"/"+protocol))
		}
	}
	sort.Strings(out)
	return out
}
func readyBackends(obj map[string]any) int64 {
	items, _ := obj["endpoints"].([]any)
	var n int64
	for _, item := range items {
		m, _ := item.(map[string]any)
		conditions, _ := m["conditions"].(map[string]any)
		ready, exists := conditions["ready"].(bool)
		if !exists || ready {
			n++
		}
	}
	return n
}
func label(meta map[string]any, key string) string {
	labels, _ := meta["labels"].(map[string]any)
	return safe(stringField(labels, key))
}

func helmOwnership(snapshot *domain.InventorySnapshot, env domain.Environment, parent domain.Identity, meta map[string]any, at string, source func(string) domain.Source) *domain.Entity {
	labels, _ := meta["labels"].(map[string]any)
	annotations, _ := meta["annotations"].(map[string]any)
	if stringField(labels, "app.kubernetes.io/managed-by") != "Helm" || stringField(annotations, "meta.helm.sh/release-name") == "" || stringField(annotations, "meta.helm.sh/release-namespace") != deref(parent.Namespace) {
		return nil
	}
	id := parent
	id.EntityKind = domain.EntityOwnership
	id.NativeID = parent.NativeID + "#metadata.helm"
	eid, err := domain.EntityID(id)
	if err != nil {
		return nil
	}
	return &domain.Entity{EntityID: eid, EntityKind: domain.EntityOwnership, Identity: id, Facts: []domain.Fact{{Field: "manager", Value: domain.StringValue("Helm"), Status: domain.StatusDeclared, ObservedAt: at, Source: source("metadata.labels.app.kubernetes.io/managed-by")}, {Field: "ownerRef", Value: domain.StringValue(safe("HelmRelease/" + deref(parent.Namespace) + "/" + safe(stringField(annotations, "meta.helm.sh/release-name")))), Status: domain.StatusDeclared, ObservedAt: at, Source: source("metadata.annotations")}, {Field: "verification", Value: domain.StringValue("canonical-helm-metadata"), Status: domain.StatusDeclared, ObservedAt: at, Source: source("metadata")}}}
}
func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
