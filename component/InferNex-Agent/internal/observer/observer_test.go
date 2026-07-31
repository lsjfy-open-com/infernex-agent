/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 * You can use this software according to the terms and conditions of the Mulan PSL v2.
 * You may obtain a copy of Mulan PSL v2 at:
 *          http://license.coscl.org.cn/MulanPSL2
 * THIS SOFTWARE IS PROVIDED ON AN "AS IS" BASIS, WITHOUT WARRANTIES OF ANY KIND,
 * EITHER EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO NON-INFRINGEMENT,
 * MERCHANTABILITY OR FIT FOR A PARTICULAR PURPOSE.
 * See the Mulan PSL v2 for more details.
 */

package observer

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	infernexv1alpha1 "gitcode.com/openFuyao/InferNex/api/v1alpha1"
)

func TestObserverUsesInferNexStatusAndManagedTopology(t *testing.T) {
	scheme := runtime.NewScheme()
	mustAddScheme(t, clientgoscheme.AddToScheme(scheme))
	mustAddScheme(t, infernexv1alpha1.AddToScheme(scheme))
	mustAddScheme(t, lwsv1.AddToScheme(scheme))

	replicas := int32(2)
	groupSize := int32(4)
	now := metav1.NewTime(time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC))
	service := &infernexv1alpha1.InferNexService{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  "models",
			Name:       "llama",
			Generation: 7,
			Annotations: map[string]string{
				autoRecoveryAnnotation:    "true",
				recoveryProfileAnnotation: "llama-pd-recovery-v1",
				recoveryNameAnnotation:    "llama-recovery",
			},
		},
		Spec: infernexv1alpha1.InferNexServiceSpec{
			BaseRefs: []infernexv1alpha1.NamedRef{{Name: "infernex-default-pd-template"}},
			Model: &infernexv1alpha1.LLMModelSpec{
				Name: "llama",
				URI:  "https://user:token@example.invalid/models/llama?signature=secret#fragment",
			},
			SourceRef: &infernexv1alpha1.SourceRef{
				APIVersion: "serving.kserve.io/v1alpha2",
				Kind:       "LLMInferenceService",
				Name:       "llama",
			},
		},
		Status: infernexv1alpha1.InferNexServiceStatus{
			Mode:               "pd",
			Ready:              false,
			ObservedGeneration: 7,
			Components: &infernexv1alpha1.InferNexComponentStatuses{
				InferenceEngine: &infernexv1alpha1.ComponentStatus{Ready: false, Message: "1/2 groups ready"},
				HermesRouter:    &infernexv1alpha1.ComponentStatus{Ready: true},
			},
			Conditions: []metav1.Condition{{
				Type:               "Ready",
				Status:             metav1.ConditionFalse,
				Reason:             "ComponentsNotReady",
				Message:            "inference engine is progressing",
				ObservedGeneration: 7,
				LastTransitionTime: now,
			}},
		},
	}

	labels := map[string]string{ownerLabel: "llama", componentLabel: "engine-pd-decode"}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "models", Name: "llama-proxy", Labels: map[string]string{
			ownerLabel: "llama", componentLabel: "proxy-server",
		}},
		Spec:   appsv1.DeploymentSpec{Replicas: &replicas},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 2},
	}
	leaderWorkerSet := &lwsv1.LeaderWorkerSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "models", Name: "llama-decode", Labels: labels},
		Spec: lwsv1.LeaderWorkerSetSpec{
			Replicas: &replicas,
			LeaderWorkerTemplate: lwsv1.LeaderWorkerTemplate{
				Size:           &groupSize,
				WorkerTemplate: corev1.PodTemplateSpec{},
			},
		},
		Status: lwsv1.LeaderWorkerSetStatus{ReadyReplicas: 1},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "models", Name: "llama-decode-0", Labels: labels},
		Spec:       corev1.PodSpec{NodeName: "inference-01"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodReady, Status: corev1.ConditionFalse,
			}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "engine", RestartCount: 3,
				State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
				},
			}},
		},
	}

	kubeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(service, deployment, leaderWorkerSet, pod).
		Build()
	domainObserver := New(kubeClient)

	detail, err := domainObserver.InspectService(context.Background(), "models", "llama")
	if err != nil {
		t.Fatalf("InspectService returned error: %v", err)
	}
	if detail.Service.Ready {
		t.Fatal("InspectService reported ready; want existing CRD status false")
	}
	if detail.Service.Model == nil || detail.Service.Model.URI != "https://example.invalid/models/llama" {
		t.Fatalf("sanitized model URI = %#v", detail.Service.Model)
	}
	if detail.Source == nil || detail.Source.Namespace != "models" {
		t.Fatalf("source namespace = %#v, want resource namespace default", detail.Source)
	}
	if detail.Service.Recovery == nil ||
		!detail.Service.Recovery.Enabled ||
		detail.Service.Recovery.Profile != "llama-pd-recovery-v1" ||
		detail.Service.Recovery.Name != "llama-recovery" {
		t.Fatalf("recovery policy = %#v", detail.Service.Recovery)
	}
	if len(detail.Service.Components) != 2 || detail.Service.Components[0].Name != "hermes-router" {
		t.Fatalf("sorted components = %#v", detail.Service.Components)
	}

	topology, err := domainObserver.GetTopology(context.Background(), "models", "llama")
	if err != nil {
		t.Fatalf("GetTopology returned error: %v", err)
	}
	if len(topology.Workloads) != 2 {
		t.Fatalf("workload count = %d, want 2", len(topology.Workloads))
	}
	if topology.Workloads[0].Kind != "LeaderWorkerSet" ||
		topology.Workloads[0].Ready != 1 ||
		topology.Workloads[0].GroupSize == nil ||
		*topology.Workloads[0].GroupSize != 4 {
		t.Fatalf("LeaderWorkerSet summary = %#v", topology.Workloads[0])
	}
	if len(topology.Pods) != 1 ||
		topology.Pods[0].Reason != "CrashLoopBackOff" ||
		topology.Pods[0].Restarts != 3 {
		t.Fatalf("pod summary = %#v", topology.Pods)
	}
}

func TestObserverRejectsUnscopedOrInvalidReads(t *testing.T) {
	domainObserver := New(fake.NewClientBuilder().Build())
	_, err := domainObserver.ListServices(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "invalid namespace") {
		t.Fatalf("ListServices error = %v, want invalid namespace", err)
	}
	_, err = domainObserver.InspectService(context.Background(), "models", "../secret")
	if err == nil || !strings.Contains(err.Error(), "invalid InferNexService name") {
		t.Fatalf("InspectService error = %v, want invalid name", err)
	}
}

func TestObserverReturnsOnlyBoundedRelatedEvents(t *testing.T) {
	scheme := runtime.NewScheme()
	mustAddScheme(t, clientgoscheme.AddToScheme(scheme))
	mustAddScheme(t, infernexv1alpha1.AddToScheme(scheme))
	mustAddScheme(t, lwsv1.AddToScheme(scheme))

	service := &infernexv1alpha1.InferNexService{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "models",
			Name:      "llama",
			UID:       types.UID("service-uid"),
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "models",
			Name:      "llama-0",
			UID:       types.UID("pod-uid"),
			Labels: map[string]string{
				ownerLabel: "llama", componentLabel: "engine-aggregate",
			},
		},
	}
	at := func(hour int, minute int) metav1.Time {
		return metav1.NewTime(time.Date(2026, 7, 30, hour, minute, 0, 0, time.UTC))
	}
	serviceEvent := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Namespace: "models", Name: "service-progress"},
		InvolvedObject: corev1.ObjectReference{
			Kind: "InferNexService", Name: "llama", UID: types.UID("service-uid"),
		},
		Type:          corev1.EventTypeWarning,
		Reason:        "ComponentsNotReady",
		Message:       "engine\nis progressing",
		LastTimestamp: at(8, 29),
		Count:         2,
	}
	podEvent := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Namespace: "models", Name: "pod-backoff"},
		InvolvedObject: corev1.ObjectReference{
			Kind: "Pod", Name: "llama-0", UID: types.UID("pod-uid"),
		},
		Type:          corev1.EventTypeWarning,
		Reason:        "BackOff",
		Message:       "back-off restarting failed container",
		LastTimestamp: at(8, 28),
	}
	oldEvent := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Namespace: "models", Name: "old-event"},
		InvolvedObject: corev1.ObjectReference{
			Kind: "Pod", Name: "llama-0", UID: types.UID("pod-uid"),
		},
		Reason:        "Old",
		LastTimestamp: at(6, 0),
	}
	unrelatedEvent := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Namespace: "models", Name: "unrelated"},
		InvolvedObject: corev1.ObjectReference{
			Kind: "Pod", Name: "other-0", UID: types.UID("other-pod-uid"),
		},
		Reason:        "Unrelated",
		LastTimestamp: at(8, 29),
	}

	kubeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(service, pod, serviceEvent, podEvent, oldEvent, unrelatedEvent).
		Build()
	domainObserver := New(kubeClient)
	domainObserver.now = func() time.Time {
		return time.Date(2026, 7, 30, 8, 30, 0, 0, time.UTC)
	}

	evidence, err := domainObserver.GetEvents(context.Background(), "models", "llama", 60, 1)
	if err != nil {
		t.Fatalf("GetEvents returned error: %v", err)
	}
	if evidence.TotalEvents != 2 || !evidence.EventsTruncated || len(evidence.Events) != 1 {
		t.Fatalf("event bounds = %#v", evidence)
	}
	if evidence.Events[0].Reason != "ComponentsNotReady" ||
		evidence.Events[0].Count != 2 ||
		evidence.Events[0].Note != "engine is progressing" {
		t.Fatalf("newest event = %#v", evidence.Events[0])
	}

	if _, err := domainObserver.GetEvents(
		context.Background(), "models", "llama", maxEventMinutes+1, 1,
	); err == nil || !strings.Contains(err.Error(), "sinceMinutes") {
		t.Fatalf("invalid event window error = %v", err)
	}
}

func mustAddScheme(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
