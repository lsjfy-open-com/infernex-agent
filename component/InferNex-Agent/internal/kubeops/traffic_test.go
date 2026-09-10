package kubeops

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func trafficPtr[T any](v T) *T { return &v }
func trafficService() *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "models", UID: "service-1"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "model"}, Ports: []corev1.ServicePort{{Name: "http", Port: 80}}}}
}
func trafficSlice(name string, endpoints ...discoveryv1.Endpoint) *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "models", Labels: map[string]string{discoveryv1.LabelServiceName: "model"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: "model", UID: "service-1"}}}, AddressType: discoveryv1.AddressTypeIPv4, Ports: []discoveryv1.EndpointPort{{Name: trafficPtr("http"), Port: trafficPtr(int32(8080))}}, Endpoints: endpoints}
}
func trafficEndpoint(uid, ip, node string) discoveryv1.Endpoint {
	return discoveryv1.Endpoint{Addresses: []string{ip}, TargetRef: &corev1.ObjectReference{Kind: "Pod", UID: types.UID(uid)}, NodeName: &node}
}
func trafficReader(t *testing.T, objects ...client.Object) *KubernetesReader {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return &KubernetesReader{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()}
}
func hasTrafficFinding(report ServiceBackendReport, code string) bool {
	for _, finding := range report.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func TestServiceBackendsDeduplicatePodsAcrossSlicesAndAddressFamilies(t *testing.T) {
	a := trafficEndpoint("a", "10.0.0.1", "n1")
	b := trafficEndpoint("b", "10.0.0.2", "n2")
	draining := trafficEndpoint("c", "10.0.0.3", "n2")
	draining.Conditions.Terminating = trafficPtr(true)
	unready := trafficEndpoint("d", "10.0.0.4", "n2")
	unready.Conditions.Ready = trafficPtr(false)
	v6 := trafficSlice("v6", trafficEndpoint("a", "fd00::1", "n1"))
	v6.AddressType = discoveryv1.AddressTypeIPv6
	r := trafficReader(t, trafficService(), trafficSlice("one", a, b, draining, unready), trafficSlice("duplicate", a), v6)
	got, err := r.InspectServiceBackends(context.Background(), ServiceBackendRequest{Namespace: "models", Name: "model"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Ports) != 1 || got.Ports[0].ReadyBackends != 2 || got.Ports[0].ReadyNodes != 2 || got.TrafficVerified {
		t.Fatalf("bad report: %+v", got)
	}
	if hasTrafficFinding(got, "insufficient-backends") || !hasTrafficFinding(got, "traffic-not-measured") {
		t.Fatalf("bad findings: %+v", got.Findings)
	}
}

func TestServiceBackendsMatchPortsAndExcludeOldService(t *testing.T) {
	svc := trafficService()
	svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{Name: "metrics", Port: 9090})
	old := trafficSlice("stale", trafficEndpoint("old", "10.0.0.3", "n2"))
	old.OwnerReferences[0].UID = "old-service"
	r := trafficReader(t, svc, trafficSlice("live", trafficEndpoint("a", "10.0.0.1", "n1")), old)
	got, err := r.InspectServiceBackends(context.Background(), ServiceBackendRequest{Namespace: "models", Name: "model"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Ports[0].ReadyBackends != 1 || got.Ports[1].ReadyBackends != 0 || !hasTrafficFinding(got, "stale-service-identity") {
		t.Fatalf("bad report: %+v", got)
	}
}

func TestServiceBackendsReportPoliciesWithoutClaimingTrafficBalance(t *testing.T) {
	svc := trafficService()
	svc.Spec.SessionAffinity = corev1.ServiceAffinityClientIP
	svc.Spec.InternalTrafficPolicy = trafficPtr(corev1.ServiceInternalTrafficPolicyLocal)
	svc.Spec.ClusterIP = corev1.ClusterIPNone
	svc.Spec.PublishNotReadyAddresses = true
	r := trafficReader(t, svc, trafficSlice("one", trafficEndpoint("a", "10.0.0.1", "n1")))
	got, err := r.InspectServiceBackends(context.Background(), ServiceBackendRequest{Namespace: "models", Name: "model"})
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"client-ip-affinity", "internal-local-policy", "headless-service", "publish-not-ready", "insufficient-backends"} {
		if !hasTrafficFinding(got, code) {
			t.Fatalf("missing %s", code)
		}
	}
	if got.TrafficVerified {
		t.Fatal("configuration inspection must not claim measured distribution")
	}
}

type trafficReadWrapper struct {
	client.Client
	deny    bool
	partial bool
	change  bool
	gets    int
}

func (c *trafficReadWrapper) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if c.deny {
		return errors.New("forbidden")
	}
	if err := c.Client.List(ctx, list, opts...); err != nil {
		return err
	}
	if c.partial {
		list.SetContinue("next")
	}
	return nil
}
func (c *trafficReadWrapper) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := c.Client.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	c.gets++
	if c.change && c.gets > 1 {
		obj.SetUID("replacement")
	}
	return nil
}
func TestServiceBackendReadFailuresNeverBecomeHealthyResults(t *testing.T) {
	for _, test := range []struct {
		name                  string
		deny, change, partial bool
	}{{name: "denied", deny: true}, {name: "replacement", change: true}, {name: "partial", partial: true}} {
		t.Run(test.name, func(t *testing.T) {
			r := trafficReader(t, trafficService())
			r.client = &trafficReadWrapper{Client: r.client, deny: test.deny, change: test.change, partial: test.partial}
			got, err := r.InspectServiceBackends(context.Background(), ServiceBackendRequest{Namespace: "models", Name: "model"})
			if test.partial {
				if err != nil || !got.Truncated || !hasTrafficFinding(got, "incomplete-snapshot") || hasTrafficFinding(got, "insufficient-backends") {
					t.Fatalf("partial report: %+v %v", got, err)
				}
			} else if err == nil {
				t.Fatal("expected read failure")
			}
		})
	}
}
func TestServiceBackendRequiresExplicitTarget(t *testing.T) {
	r := trafficReader(t)
	_, err := r.InspectServiceBackends(context.Background(), ServiceBackendRequest{Name: "model"})
	if err == nil || !strings.Contains(err.Error(), "namespace") {
		t.Fatal(err)
	}
}
