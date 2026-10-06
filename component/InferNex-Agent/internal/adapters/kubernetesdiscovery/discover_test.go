package kubernetesdiscovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domain"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/kubeops"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type fakeReader struct {
	calls []kubeops.ResourceReadRequest
	read  func(kubeops.ResourceReadRequest) (kubeops.ResourceReadResult, error)
}

func (f *fakeReader) ReadResources(_ context.Context, r kubeops.ResourceReadRequest) (kubeops.ResourceReadResult, error) {
	f.calls = append(f.calls, r)
	return f.read(r)
}

func TestDiscoverKubernetesNativeAndHelmOwnership(t *testing.T) {
	env := testEnvironment(t)
	reader := &fakeReader{read: func(r kubeops.ResourceReadRequest) (kubeops.ResourceReadResult, error) {
		switch r.Resource {
		case "deployments":
			return kubeops.ResourceReadResult{Objects: []map[string]any{deploymentObject()}}, nil
		case "services":
			return kubeops.ResourceReadResult{Objects: []map[string]any{serviceObject()}}, nil
		case "pods":
			return kubeops.ResourceReadResult{Objects: []map[string]any{podObject("pod-a", "uid-a", true), podObject("pod-b", "uid-b", false)}}, nil
		default:
			t.Fatalf("unexpected resource %q", r.Resource)
			return kubeops.ResourceReadResult{}, nil
		}
	}}
	d, _ := NewDiscoverer(reader, env.Spec.ClusterID, env.Spec.IdentityFingerprint)
	result, err := d.Discover(context.Background(), *env, DiscoverRequest{PrincipalScope: env.TenantScope, Namespaces: []string{"models"}, ResourceKinds: []string{"deployments", "services", "pods"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Spec.Completeness != domain.CompletenessComplete {
		t.Fatalf("completeness %s", result.Snapshot.Spec.Completeness)
	}
	kinds := map[domain.EntityKind]int{}
	for _, e := range result.Snapshot.Spec.Entities {
		kinds[e.EntityKind]++
	}
	if kinds[domain.EntityWorkload] != 3 || kinds[domain.EntityEndpoint] != 1 || kinds[domain.EntityOwnership] != 1 {
		t.Fatalf("unexpected entity kinds %#v", kinds)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "shape-only") {
		t.Fatal("raw metadata escaped projection")
	}
}

func TestKubernetesPartialRBACStaysPartial(t *testing.T) {
	env := testEnvironment(t)
	reader := &fakeReader{read: func(r kubeops.ResourceReadRequest) (kubeops.ResourceReadResult, error) {
		switch r.Resource {
		case "deployments":
			return kubeops.ResourceReadResult{Objects: []map[string]any{deploymentObject()}}, nil
		case "pods":
			return kubeops.ResourceReadResult{}, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("DO_NOT_LEAK_TOKEN_7E31"))
		case "services":
			return kubeops.ResourceReadResult{}, context.DeadlineExceeded
		default:
			t.Fatalf("unexpected resource %q", r.Resource)
			return kubeops.ResourceReadResult{}, nil
		}
	}}
	d, _ := NewDiscoverer(reader, env.Spec.ClusterID, env.Spec.IdentityFingerprint)
	result, err := d.Discover(context.Background(), *env, DiscoverRequest{PrincipalScope: env.TenantScope, Namespaces: []string{"models"}, ResourceKinds: []string{"deployments", "pods", "services"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Spec.Completeness != domain.CompletenessPartial {
		t.Fatalf("got %s", result.Snapshot.Spec.Completeness)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "DO_NOT_LEAK") {
		t.Fatal("server error leaked")
	}
	codes := map[domain.IssueCode]bool{}
	for _, issue := range result.Snapshot.Spec.Issues {
		codes[issue.Code] = true
	}
	if !codes[domain.IssueForbidden] || !codes[domain.IssueTimeout] {
		t.Fatalf("partial causes were not preserved: %#v", codes)
	}
}

func TestKubernetesSecretsAndLiveYAMLDoNotLeak(t *testing.T) {
	env := testEnvironment(t)
	reader := &fakeReader{read: func(r kubeops.ResourceReadRequest) (kubeops.ResourceReadResult, error) {
		o := podObject("secret-pod", "uid-secret", false)
		o["data"] = map[string]any{"token": "DO_NOT_LEAK_TOKEN_7E31"}
		o["spec"].(map[string]any)["command"] = []any{"--password=DO_NOT_LEAK_TOKEN_7E31"}
		return kubeops.ResourceReadResult{Objects: []map[string]any{o}}, nil
	}}
	d, _ := NewDiscoverer(reader, env.Spec.ClusterID, env.Spec.IdentityFingerprint)
	result, err := d.Discover(context.Background(), *env, DiscoverRequest{PrincipalScope: env.TenantScope, Namespaces: []string{"models"}, ResourceKinds: []string{"pods"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "DO_NOT_LEAK") || strings.Contains(string(raw), "command") {
		t.Fatal("unprojected secret escaped")
	}
}

func TestKubernetesMaliciousDepthAndInvalidIdentityAreBounded(t *testing.T) {
	env := testEnvironment(t)
	deep := podObject("deep", "uid-deep", false)
	var nested any = "leaf"
	for i := 0; i < 1_000; i++ {
		nested = map[string]any{"next": nested}
	}
	deep["untrusted"] = nested
	invalid := podObject("invalid", "uid-invalid", false)
	invalid["metadata"].(map[string]any)["uid"] = string([]byte{0xff})
	reader := &fakeReader{read: func(kubeops.ResourceReadRequest) (kubeops.ResourceReadResult, error) {
		return kubeops.ResourceReadResult{Objects: []map[string]any{deep, invalid}}, nil
	}}
	d, _ := NewDiscoverer(reader, env.Spec.ClusterID, env.Spec.IdentityFingerprint)
	result, err := d.Discover(context.Background(), *env, DiscoverRequest{PrincipalScope: env.TenantScope, Namespaces: []string{"models"}, ResourceKinds: []string{"pods"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Spec.Completeness != domain.CompletenessPartial || len(result.Snapshot.Spec.Entities) != 1 {
		t.Fatalf("malicious inputs were not bounded: completeness=%s entities=%d", result.Snapshot.Spec.Completeness, len(result.Snapshot.Spec.Entities))
	}
	if err := domain.VerifyRecord(result.Snapshot); err != nil {
		t.Fatalf("bounded snapshot is invalid: %v", err)
	}
}

func TestKubernetesPaginationAndLimitsPropagate(t *testing.T) {
	env := testEnvironment(t)
	reader := &fakeReader{read: func(r kubeops.ResourceReadRequest) (kubeops.ResourceReadResult, error) {
		page := 0
		if r.Continue != "" {
			if _, err := fmt.Sscanf(r.Continue, "raw-page-%d", &page); err != nil {
				t.Fatalf("unexpected raw token %q", r.Continue)
			}
		}
		if page == 10 {
			return kubeops.ResourceReadResult{Objects: []map[string]any{podObject("continued", "uid-continued", false)}}, nil
		}
		objects := make([]map[string]any, 0, 15)
		for i := 0; i < 15; i++ {
			objects = append(objects, podObject(fmt.Sprintf("pod-%d-%d", page, i), fmt.Sprintf("uid-%d-%d", page, i), true))
		}
		return kubeops.ResourceReadResult{Continue: fmt.Sprintf("raw-page-%d", page+1), Objects: objects}, nil
	}}
	d, _ := NewDiscoverer(reader, env.Spec.ClusterID, env.Spec.IdentityFingerprint)
	first, err := d.Discover(context.Background(), *env, DiscoverRequest{PrincipalScope: env.TenantScope, Namespaces: []string{"models"}, ResourceKinds: []string{"pods"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.CursorHandle == "" || strings.Contains(first.CursorHandle, "raw-page") {
		t.Fatal("cursor is absent or exposes Kubernetes token")
	}
	if len(first.Snapshot.Spec.Entities) != domain.MaxEntities {
		t.Fatalf("first scan did not consume pages to budget: %d", len(first.Snapshot.Spec.Entities))
	}
	if _, err := d.Discover(context.Background(), *env, DiscoverRequest{PrincipalScope: "other", CursorHandle: first.CursorHandle}); err == nil {
		t.Fatal("cross-scope cursor accepted")
	}
	if _, err := d.Discover(context.Background(), *env, DiscoverRequest{PrincipalScope: env.TenantScope, Namespaces: []string{"denied"}, ResourceKinds: []string{"pods"}, CursorHandle: first.CursorHandle}); !errors.Is(err, ErrCursorUnauthorized) {
		t.Fatalf("changed cursor filters returned %v", err)
	}
	second, err := d.Discover(context.Background(), *env, DiscoverRequest{PrincipalScope: env.TenantScope, Namespaces: []string{"models"}, ResourceKinds: []string{"pods"}, CursorHandle: first.CursorHandle})
	if err != nil {
		t.Fatal(err)
	}
	if second.Snapshot.Spec.PreviousSnapshotRef == nil || second.Snapshot.Spec.PreviousSnapshotRef.ID != first.Snapshot.ID {
		t.Fatal("continuation lineage missing")
	}
}

func TestKubernetesFingerprintAndScopeFailBeforeRead(t *testing.T) {
	env := testEnvironment(t)
	reader := &fakeReader{read: func(kubeops.ResourceReadRequest) (kubeops.ResourceReadResult, error) {
		t.Fatal("reader called")
		return kubeops.ResourceReadResult{}, nil
	}}
	d, _ := NewDiscoverer(reader, env.Spec.ClusterID, "sha256:"+strings.Repeat("b", 64))
	if _, err := d.Discover(context.Background(), *env, DiscoverRequest{PrincipalScope: env.TenantScope}); err == nil {
		t.Fatal("fingerprint mismatch accepted")
	}
	d, _ = NewDiscoverer(reader, env.Spec.ClusterID, env.Spec.IdentityFingerprint)
	if _, err := d.Discover(context.Background(), *env, DiscoverRequest{PrincipalScope: "other"}); err == nil {
		t.Fatal("scope mismatch accepted")
	}
	if _, err := d.Discover(context.Background(), *env, DiscoverRequest{PrincipalScope: env.TenantScope, Namespaces: []string{"outside"}, ResourceKinds: []string{"pods"}}); err == nil {
		t.Fatal("namespace expansion accepted")
	}
	if _, err := d.Discover(context.Background(), *env, DiscoverRequest{PrincipalScope: env.TenantScope, Namespaces: []string{"models"}, ResourceKinds: []string{"secrets"}}); err == nil {
		t.Fatal("unsupported resource accepted")
	}
}

func TestKubernetesCancellationAndUIDReplacement(t *testing.T) {
	env := testEnvironment(t)
	reader := &fakeReader{read: func(r kubeops.ResourceReadRequest) (kubeops.ResourceReadResult, error) {
		return kubeops.ResourceReadResult{Objects: []map[string]any{podObject("same-name", "uid-one", false)}}, nil
	}}
	d, _ := NewDiscoverer(reader, env.Spec.ClusterID, env.Spec.IdentityFingerprint)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Discover(ctx, *env, DiscoverRequest{PrincipalScope: env.TenantScope, Namespaces: []string{"models"}, ResourceKinds: []string{"pods"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled scan returned %v", err)
	}
	if len(reader.calls) != 0 {
		t.Fatal("canceled scan called reader")
	}
	first, err := d.Discover(context.Background(), *env, DiscoverRequest{PrincipalScope: env.TenantScope, Namespaces: []string{"models"}, ResourceKinds: []string{"pods"}})
	if err != nil {
		t.Fatal(err)
	}
	reader.read = func(r kubeops.ResourceReadRequest) (kubeops.ResourceReadResult, error) {
		return kubeops.ResourceReadResult{Objects: []map[string]any{podObject("same-name", "uid-two", false)}}, nil
	}
	second, err := d.Discover(context.Background(), *env, DiscoverRequest{PrincipalScope: env.TenantScope, Namespaces: []string{"models"}, ResourceKinds: []string{"pods"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Snapshot.Spec.Entities[0].EntityID == second.Snapshot.Spec.Entities[0].EntityID {
		t.Fatal("UID replacement reused physical identity")
	}
	reader.read = func(r kubeops.ResourceReadRequest) (kubeops.ResourceReadResult, error) {
		objects := make([]map[string]any, r.Limit+1)
		for i := range objects {
			objects[i] = podObject(fmt.Sprintf("overflow-%d", i), fmt.Sprintf("uid-overflow-%d", i), false)
		}
		return kubeops.ResourceReadResult{Objects: objects}, nil
	}
	limited, err := d.Discover(context.Background(), *env, DiscoverRequest{PrincipalScope: env.TenantScope, Namespaces: []string{"models"}, ResourceKinds: []string{"pods"}})
	if err != nil {
		t.Fatal(err)
	}
	if limited.Snapshot.Spec.Completeness != domain.CompletenessPartial || len(limited.Snapshot.Spec.Entities) != 0 {
		t.Fatalf("reader overrun was not bounded: completeness=%s entities=%d", limited.Snapshot.Spec.Completeness, len(limited.Snapshot.Spec.Entities))
	}
}

func TestKubernetesDuplicateLogicalObjectsAreReported(t *testing.T) {
	env := testEnvironment(t)
	reader := &fakeReader{read: func(kubeops.ResourceReadRequest) (kubeops.ResourceReadResult, error) {
		return kubeops.ResourceReadResult{Objects: []map[string]any{
			podObject("same-name", "uid-one", false),
			podObject("same-name", "uid-one", false),
			podObject("same-name", "uid-two", false),
		}}, nil
	}}
	d, _ := NewDiscoverer(reader, env.Spec.ClusterID, env.Spec.IdentityFingerprint)
	result, err := d.Discover(context.Background(), *env, DiscoverRequest{PrincipalScope: env.TenantScope, Namespaces: []string{"models"}, ResourceKinds: []string{"pods"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Spec.Completeness != domain.CompletenessPartial || len(result.Snapshot.Spec.Entities) != 2 {
		t.Fatalf("duplicate identity was not bounded: completeness=%s entities=%d", result.Snapshot.Spec.Completeness, len(result.Snapshot.Spec.Entities))
	}
	foundConflict := false
	for _, issue := range result.Snapshot.Spec.Issues {
		foundConflict = foundConflict || issue.Code == domain.IssueConflict
	}
	if !foundConflict {
		t.Fatal("duplicate logical object was not reported")
	}
}

func TestKubernetesRejectsMismatchedObjectTypeAndClassifiesExpiredRead(t *testing.T) {
	env := testEnvironment(t)
	reader := &fakeReader{read: func(kubeops.ResourceReadRequest) (kubeops.ResourceReadResult, error) {
		object := podObject("wrong-type", "uid-wrong", false)
		object["kind"] = "Service"
		return kubeops.ResourceReadResult{Objects: []map[string]any{object}}, nil
	}}
	d, _ := NewDiscoverer(reader, env.Spec.ClusterID, env.Spec.IdentityFingerprint)
	result, err := d.Discover(context.Background(), *env, DiscoverRequest{PrincipalScope: env.TenantScope, Namespaces: []string{"models"}, ResourceKinds: []string{"pods"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Snapshot.Spec.Entities) != 0 || result.Snapshot.Spec.Completeness != domain.CompletenessPartial {
		t.Fatalf("mismatched object escaped projection: completeness=%s entities=%d", result.Snapshot.Spec.Completeness, len(result.Snapshot.Spec.Entities))
	}
	code, state := classify(apierrors.NewResourceExpired("synthetic expired token"))
	if code != domain.IssueCursorExpired || state != domain.CoverageFailed {
		t.Fatalf("expired Kubernetes read classified as code=%s state=%s", code, state)
	}
}

func TestKubernetesTimeoutReturnsPartialPreview(t *testing.T) {
	env := testEnvironment(t)
	reader := &fakeReader{read: func(r kubeops.ResourceReadRequest) (kubeops.ResourceReadResult, error) {
		if r.Resource == "services" {
			return kubeops.ResourceReadResult{}, context.DeadlineExceeded
		}
		return kubeops.ResourceReadResult{Objects: []map[string]any{podObject("visible", "uid-visible", false)}}, nil
	}}
	d, _ := NewDiscoverer(reader, env.Spec.ClusterID, env.Spec.IdentityFingerprint)
	result, err := d.Discover(context.Background(), *env, DiscoverRequest{PrincipalScope: env.TenantScope, Namespaces: []string{"models"}, ResourceKinds: []string{"pods", "services"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Spec.Completeness != domain.CompletenessPartial || len(result.Snapshot.Spec.Entities) != 1 || result.CursorHandle == "" {
		t.Fatalf("timeout did not preserve a resumable partial preview: completeness=%s entities=%d cursor=%q", result.Snapshot.Spec.Completeness, len(result.Snapshot.Spec.Entities), result.CursorHandle)
	}
}

func TestKubernetesCursorTTLAndCapacity(t *testing.T) {
	env := testEnvironment(t)
	reader := &fakeReader{read: func(kubeops.ResourceReadRequest) (kubeops.ResourceReadResult, error) {
		return kubeops.ResourceReadResult{}, nil
	}}
	d, _ := NewDiscoverer(reader, env.Spec.ClusterID, env.Spec.IdentityFingerprint)
	now := time.Date(2026, 10, 2, 1, 2, 3, 0, time.UTC)
	d.now = func() time.Time { return now }
	state := cursorState{scope: env.TenantScope, environment: env.Reference(), tasks: []resourceTask{{groupVersion: "v1", resource: "pods", namespace: "models"}}, previous: domain.RecordRef{TenantScope: env.TenantScope, Kind: domain.KindInventorySnapshot, ID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Revision: 1}, expires: now.Add(domain.PreviewTTL)}
	handles := make([]string, 0, domain.MaxPreviewsPerSubject)
	for i := 0; i < domain.MaxPreviewsPerSubject; i++ {
		h, err := d.putCursor(state)
		if err != nil {
			t.Fatal(err)
		}
		handles = append(handles, h)
	}
	if _, err := d.putCursor(state); err == nil {
		t.Fatal("accepted fifth cursor for one scope")
	}
	now = now.Add(domain.PreviewTTL)
	if _, err := d.putCursor(cursorState{scope: state.scope, environment: state.environment, tasks: state.tasks, previous: state.previous, expires: now.Add(domain.PreviewTTL)}); err != nil {
		t.Fatalf("expired cursors still consumed capacity: %v", err)
	}
	if _, err := d.takeCursor(handles[0], state.scope, state.environment, ""); err == nil {
		t.Fatal("expired cursor remained usable")
	}
}

func testEnvironment(t *testing.T) *domain.Environment {
	t.Helper()
	id := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	e := &domain.Environment{SchemaVersion: domain.SchemaVersion, Kind: domain.KindEnvironment, TenantScope: "scope-a", ID: id, Revision: 1, CreatedAt: "2026-10-02T01:02:03.000000000Z", Spec: domain.EnvironmentSpec{Runtime: domain.RuntimeKubernetes, EndpointRef: domain.LocalRef{ID: id}, CredentialRef: &domain.LocalRef{ID: id}, IdentityFingerprint: "sha256:" + strings.Repeat("a", 64), ClusterID: "cluster-a", AllowedNamespaces: []string{"models", "denied"}, NetworkPolicy: domain.NetworkOffline, Enabled: true}}
	if err := domain.FinalizeRecord(e); err != nil {
		t.Fatal(err)
	}
	return e
}
func metadata(name, uid string) map[string]any {
	return map[string]any{"name": name, "namespace": "models", "uid": uid, "resourceVersion": "7"}
}
func podObject(name, uid string, helm bool) map[string]any {
	m := metadata(name, uid)
	if helm {
		m["labels"] = map[string]any{"app.kubernetes.io/managed-by": "Helm"}
		m["annotations"] = map[string]any{"meta.helm.sh/release-name": "release-a", "meta.helm.sh/release-namespace": "models"}
	} else {
		m["labels"] = map[string]any{"app.kubernetes.io/managed-by": "shape-only"}
	}
	return map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": m, "spec": map[string]any{"nodeName": "node-a", "containers": []any{map[string]any{"image": "example.invalid/synthetic:v1"}}}, "status": map[string]any{"phase": "Running"}}
}
func serviceObject() map[string]any {
	return map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": metadata("svc", "uid-svc"), "spec": map[string]any{"ports": []any{map[string]any{"port": float64(80), "protocol": "TCP"}}}}
}

func deploymentObject() map[string]any {
	return map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": metadata("deployment-a", "uid-deployment"), "spec": map[string]any{"replicas": float64(2), "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"image": "example.invalid/deployment:v1"}}}}}, "status": map[string]any{"readyReplicas": float64(1)}}
}
