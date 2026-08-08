/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package kubeops

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	metafake "k8s.io/client-go/metadata/fake"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
)

type fixedLogReader struct {
	contents []byte
}

func (f fixedLogReader) Read(
	context.Context,
	string,
	string,
	string,
	time.Time,
	bool,
	int64,
	int64,
) ([]byte, error) {
	return append([]byte(nil), f.contents...), nil
}

func TestDetectEnvironmentAndListNativeWorkloads(t *testing.T) {
	reader := newTestReader(t, fixedLogReader{contents: []byte("ready")})

	environment, err := reader.DetectEnvironment(context.Background())
	if err != nil {
		t.Fatalf("detect environment: %v", err)
	}
	if environment.Platform != "openfuyao" || !environment.Capabilities["bkeClusterLifecycle"] || !environment.Capabilities["leaderWorkerSet"] {
		t.Fatalf("environment = %#v", environment)
	}
	if environment.APIServer != "https://business-api.example.invalid:6443" {
		t.Fatalf("api server = %q", environment.APIServer)
	}
	if !contains(environment.ClusterRoles, "bootstrap-or-management-control-plane") || !contains(environment.ClusterRoles, "inference-business-cluster") {
		t.Fatalf("roles = %#v", environment.ClusterRoles)
	}

	inventory, err := reader.ListWorkloads(context.Background(), WorkloadRequest{Namespace: "ai-inference"})
	if err != nil {
		t.Fatalf("list workloads: %v", err)
	}
	if inventory.Total != 4 || len(inventory.Workloads) != 2 || len(inventory.Pods) != 1 || len(inventory.Services) != 1 {
		t.Fatalf("inventory = %#v", inventory)
	}
	if inventory.Workloads[1].Kind != "LeaderWorkerSet" || inventory.Workloads[1].Ready != 1 {
		t.Fatalf("workloads = %#v", inventory.Workloads)
	}
	if inventory.Pods[0].HelmRelease != "qwen" || !inventory.Pods[0].Ready {
		t.Fatalf("pod = %#v", inventory.Pods[0])
	}
}

func TestEventsAndLogsAreBoundedAndRedacted(t *testing.T) {
	reader := newTestReader(t, fixedLogReader{contents: []byte("Authorization: Bearer abc password=hunter2\nmodel ready")})
	reader.now = func() time.Time { return time.Date(2026, 8, 8, 8, 0, 0, 0, time.UTC) }

	events, err := reader.GetEvents(context.Background(), EventRequest{
		Namespace: "ai-inference", Kind: "Pod", Name: "qwen-0", SinceMinutes: 60,
	})
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	if len(events.Events) != 1 || strings.Contains(events.Events[0].Message, "hunter2") || !strings.Contains(events.Events[0].Message, "<redacted>") {
		t.Fatalf("events = %#v", events)
	}

	logs, err := reader.GetPodLogs(context.Background(), PodLogRequest{
		Namespace: "ai-inference", Pod: "qwen-0", TailLines: 20,
	})
	if err != nil {
		t.Fatalf("get logs: %v", err)
	}
	if len(logs.Streams) != 1 || strings.Contains(logs.Streams[0].Text, "hunter2") || strings.Contains(logs.Streams[0].Text, "abc") {
		t.Fatalf("logs = %#v", logs)
	}
}

func TestListHelmReleasesUsesMetadataOnlyAndLatestRevision(t *testing.T) {
	reader := newTestReader(t, fixedLogReader{})
	releases, err := reader.ListHelmReleases(context.Background(), HelmReleaseRequest{Namespace: "ai-inference"})
	if err != nil {
		t.Fatalf("list Helm releases: %v", err)
	}
	if releases.Total != 1 || len(releases.Releases) != 1 || releases.Releases[0].Name != "qwen" || releases.Releases[0].Revision != 2 {
		t.Fatalf("releases = %#v", releases)
	}
}

func newTestReader(t *testing.T, logs fixedLogReader) *KubernetesReader {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := lwsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	replicas := int32(1)
	objects := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cluster-system"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "openfuyao-system"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ai-inference"}},
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "npu-01"},
			Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ai-inference", Name: "router"},
			Spec:       appsv1.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "router"}}, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "router", Image: "router:v1"}}}}},
			Status:     appsv1.DeploymentStatus{ReadyReplicas: 1, AvailableReplicas: 1},
		},
		&lwsv1.LeaderWorkerSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ai-inference", Name: "qwen-engine", Annotations: map[string]string{"meta.helm.sh/release-name": "qwen"}},
			Spec:       lwsv1.LeaderWorkerSetSpec{Replicas: &replicas, LeaderWorkerTemplate: lwsv1.LeaderWorkerTemplate{WorkerTemplate: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "engine", Image: "vllm-ascend:v0.23.0"}}}}}},
			Status:     lwsv1.LeaderWorkerSetStatus{ReadyReplicas: 1, Replicas: 1},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ai-inference", Name: "qwen-0", Annotations: map[string]string{"meta.helm.sh/release-name": "qwen"}},
			Spec:       corev1.PodSpec{NodeName: "npu-01", Containers: []corev1.Container{{Name: "engine", Image: "vllm-ascend:v0.23.0"}}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
		},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "ai-inference", Name: "qwen", Annotations: map[string]string{"meta.helm.sh/release-name": "qwen"}}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.96.0.10", Ports: []corev1.ServicePort{{Name: "http", Port: 8000}}}},
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Namespace: "ai-inference", Name: "qwen-event"},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "qwen-0"},
			Type:           corev1.EventTypeWarning, Reason: "Failed", Message: "password=hunter2 image pull failed",
			LastTimestamp: metav1.NewTime(time.Date(2026, 8, 8, 7, 50, 0, 0, time.UTC)), Count: 2,
		},
	}
	controllerClient := ctrlfake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()
	clientset := fake.NewSimpleClientset()
	fakeDiscovery := clientset.Discovery().(*fakediscovery.FakeDiscovery)
	fakeDiscovery.Resources = []*metav1.APIResourceList{
		{GroupVersion: "bke.bocloud.com/v1beta1", APIResources: []metav1.APIResource{{Name: "bkeclusters"}, {Name: "bkenodes"}}},
		{GroupVersion: "leaderworkerset.x-k8s.io/v1", APIResources: []metav1.APIResource{{Name: "leaderworkersets"}}},
	}
	fakeDiscovery.FakedServerVersion = &version.Info{GitVersion: "v1.33.1-openfuyao"}

	metadataScheme := metafake.NewTestScheme()
	metav1.AddMetaToScheme(metadataScheme)
	metadataClient := metafake.NewSimpleMetadataClient(metadataScheme,
		helmMetadata("ai-inference", "sh.helm.release.v1.qwen.v1", "qwen", "1", "superseded"),
		helmMetadata("ai-inference", "sh.helm.release.v1.qwen.v2", "qwen", "2", "deployed"),
	)
	reader, err := New(controllerClient, fakeDiscovery, metadataClient, logs, "https://business-api.example.invalid:6443")
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

func helmMetadata(namespace, objectName, release, revision, status string) *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace, Name: objectName,
			Labels: map[string]string{"owner": "helm", "name": release, "version": revision, "status": status},
		},
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
