package plogcapture

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func TestListTargetsUsesSelectorPhaseAndContainer(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "models", Name: "running", UID: types.UID("uid-running"), Labels: map[string]string{"app": "vllm"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "vllm"}, {Name: "sidecar"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "models", Name: "pending", UID: types.UID("uid-pending"), Labels: map[string]string{"app": "vllm"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "vllm"}}}, Status: corev1.PodStatus{Phase: corev1.PodPending}},
	)
	source, err := NewKubernetesSource(client, &rest.Config{Host: "https://example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	targets, err := source.ListTargets(context.Background(), "models", "app=vllm", "vllm")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Pod != "running" || targets[0].Container != "vllm" || targets[0].UID != "uid-running" {
		t.Fatalf("unexpected targets: %#v", targets)
	}
}

func TestPlogRootsPreferContainerLogMounts(t *testing.T) {
	roots := plogRootsForContainer(corev1.Container{VolumeMounts: []corev1.VolumeMount{
		{Name: "ascend-logs", MountPath: "/data/array/ascend/log"},
		{Name: "models", MountPath: "/models"},
	}})
	if len(roots) < 2 || roots[0] != "/data/array/ascend/log" {
		t.Fatalf("roots=%v", roots)
	}
	if withinPlogRoots("/etc/shadow", roots) {
		t.Fatal("path outside discovered roots accepted")
	}
}
