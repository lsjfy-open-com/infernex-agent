package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infernexv1alpha1 "gitcode.com/openFuyao/InferNex/api/v1alpha1"
)

func TestReconcileInferNexServiceDeletion_RemovesFinalizer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := gatewayTestScheme(t)
	insvc := &infernexv1alpha1.InferNexService{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "demo",
			Namespace:  "ns-a",
			Finalizers: []string{infernexServiceFinalizer},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(insvc).Build()
	r := &InferNexServiceReconciler{Client: cl, Scheme: s}

	res, err := r.reconcileInferNexServiceDeletion(ctx, insvc)
	if err != nil {
		t.Fatalf("reconcileInferNexServiceDeletion error: %v", err)
	}
	if res.Requeue {
		t.Fatalf("unexpected requeue: %#v", res)
	}
	fresh := &infernexv1alpha1.InferNexService{}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "ns-a", Name: "demo"}, fresh); err != nil {
		t.Fatalf("get updated infernexservice failed: %v", err)
	}
	if len(fresh.Finalizers) != 0 {
		t.Fatalf("expected finalizers removed, got %v", fresh.Finalizers)
	}
}

func TestReconcileInferNexServiceDeletion_ReleasesSingletonOwnerRefs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := gatewayTestScheme(t)
	ownerA := &infernexv1alpha1.InferNexService{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "isvc-a",
			Namespace:  "ns-a",
			UID:        types.UID("uid-a"),
			Finalizers: []string{infernexServiceFinalizer},
		},
	}
	ownerB := &infernexv1alpha1.InferNexService{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "isvc-b",
			Namespace:  "ns-a",
			UID:        types.UID("uid-b"),
			Finalizers: []string{infernexServiceFinalizer},
		},
	}
	refs := []metav1.OwnerReference{
		{APIVersion: "infernex.io/v1alpha1", Kind: "InferNexService", Name: ownerA.Name, UID: ownerA.UID},
		{APIVersion: "infernex.io/v1alpha1", Kind: "InferNexService", Name: ownerB.Name, UID: ownerB.UID},
	}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{
		Name:            "redis-service",
		Namespace:       "ns-a",
		OwnerReferences: refs,
	}}
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name:            componentControllerSAName(cacheIndexerComponent),
		Namespace:       "ns-a",
		OwnerReferences: refs,
	}}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(ownerA, ownerB, svc, sa).Build()
	r := &InferNexServiceReconciler{Client: cl, Scheme: s}

	if _, err := r.reconcileInferNexServiceDeletion(ctx, ownerA); err != nil {
		t.Fatalf("reconcileInferNexServiceDeletion(%s) error: %v", ownerA.Name, err)
	}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "ns-a", Name: "redis-service"}, svc); err != nil {
		t.Fatalf("expected singleton service after first owner deletion: %v", err)
	}
	assertMissingOwnerRef(t, svc.OwnerReferences, ownerA.Name, ownerA.UID)
	assertNonControllerOwnerRef(t, svc.OwnerReferences, ownerB.Name, ownerB.UID)
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "ns-a", Name: componentControllerSAName(cacheIndexerComponent)}, sa); err != nil {
		t.Fatalf("expected singleton serviceaccount after first owner deletion: %v", err)
	}
	assertMissingOwnerRef(t, sa.OwnerReferences, ownerA.Name, ownerA.UID)
	assertNonControllerOwnerRef(t, sa.OwnerReferences, ownerB.Name, ownerB.UID)

	if _, err := r.reconcileInferNexServiceDeletion(ctx, ownerB); err != nil {
		t.Fatalf("reconcileInferNexServiceDeletion(%s) error: %v", ownerB.Name, err)
	}
	mustNotFound(t, cl, types.NamespacedName{Namespace: "ns-a", Name: "redis-service"}, &corev1.Service{})
	mustNotFound(t, cl, types.NamespacedName{Namespace: "ns-a", Name: componentControllerSAName(cacheIndexerComponent)}, &corev1.ServiceAccount{})
}

func TestReconcileFetchInferNexService(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := controllerTestScheme(t)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns-a", Name: "demo"}}

	t.Run("returns existing object", func(t *testing.T) {
		insvc := &infernexv1alpha1.InferNexService{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "ns-a"}}
		cl := fake.NewClientBuilder().WithScheme(s).WithObjects(insvc).Build()
		r := &InferNexServiceReconciler{Client: cl, Scheme: s}
		got, err := r.reconcileFetchInferNexService(ctx, req)
		if err != nil {
			t.Fatalf("reconcileFetchInferNexService error: %v", err)
		}
		if got == nil || got.Name != "demo" {
			t.Fatalf("expected fetched object, got %#v", got)
		}
	})

	t.Run("notfound triggers llm build path", func(t *testing.T) {
		llm := newLinkedLLM("ns-a", "demo", infernexRuntimeValue)
		base := fake.NewClientBuilder().WithScheme(s).WithObjects(llm).Build()
		cl := &llmAwareClient{
			Client: base,
			llms:   map[types.NamespacedName]*unstructured.Unstructured{{Namespace: "ns-a", Name: "demo"}: llm},
		}
		r := &InferNexServiceReconciler{Client: cl, Scheme: s}
		got, err := r.reconcileFetchInferNexService(ctx, req)
		if err != nil {
			t.Fatalf("reconcileFetchInferNexService build-from-llm error: %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil result on create-on-miss path, got %#v", got)
		}
		created := &infernexv1alpha1.InferNexService{}
		if err := base.Get(ctx, req.NamespacedName, created); err != nil {
			t.Fatalf("expected infernexservice created from llm: %v", err)
		}
	})
}

func TestEmitGatewayRoutingConditionEvent(t *testing.T) {
	t.Parallel()
	rec := record.NewFakeRecorder(10)
	r := &InferNexServiceReconciler{Recorder: rec}
	obj := &infernexv1alpha1.InferNexService{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "ns-a"}}

	r.emitGatewayRoutingConditionEvent(obj, nil, []metav1.Condition{{
		Type:    "GatewayRoutingReady",
		Status:  metav1.ConditionFalse,
		Reason:  "GatewayNotFound",
		Message: "gateway ns-a/gw not found",
	}})
	select {
	case e := <-rec.Events:
		if e == "" {
			t.Fatal("expected warning event content")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for warning event")
	}

	r.emitGatewayRoutingConditionEvent(obj, nil, []metav1.Condition{{
		Type:    "GatewayRoutingReady",
		Status:  metav1.ConditionTrue,
		Reason:  "GatewayRoutingReady",
		Message: "gateway routing linked",
	}})
	select {
	case e := <-rec.Events:
		if e == "" {
			t.Fatal("expected normal event content")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for normal event")
	}

	// unchanged condition should not emit a new event.
	old := []metav1.Condition{{Type: "GatewayRoutingReady", Status: metav1.ConditionTrue, Reason: "GatewayRoutingReady", Message: "same"}}
	r.emitGatewayRoutingConditionEvent(obj, old, old)
	select {
	case <-rec.Events:
		t.Fatal("unexpected event for unchanged condition")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestRecordWarn_NoPanicPaths(t *testing.T) {
	t.Parallel()
	r := &InferNexServiceReconciler{}
	r.recordWarn(nil, "X", "msg")
	r.recordNormal(nil, "X", "msg")
	r.Recorder = record.NewFakeRecorder(4)
	obj := &infernexv1alpha1.InferNexService{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "ns-a"}}
	r.recordWarn(obj, "Warn", "hello %s", "x")
	r.recordNormal(obj, "Normal", "hello")
}

func TestReconcile_AddsFinalizerAndRequeues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := gatewayTestScheme(t)
	insvc := &infernexv1alpha1.InferNexService{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "ns-a", Generation: 1},
		Spec: infernexv1alpha1.InferNexServiceSpec{
			SourceRef: &infernexv1alpha1.SourceRef{Kind: "LLMInferenceService", Name: "llm"},
		},
	}
	llm := newLinkedLLM("ns-a", "llm", infernexRuntimeValue)
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(insvc, llm).Build()
	r := &InferNexServiceReconciler{Client: cl, Scheme: s, TemplateNamespace: "tpl-ns"}

	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns-a", Name: "demo"}})
	if err != nil {
		t.Fatalf("Reconcile add-finalizer error: %v", err)
	}
	if !res.Requeue {
		t.Fatalf("expected requeue after adding finalizer, got %#v", res)
	}
	fresh := &infernexv1alpha1.InferNexService{}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "ns-a", Name: "demo"}, fresh); err != nil {
		t.Fatalf("get infernexservice: %v", err)
	}
	if !containsString(fresh.Finalizers, infernexServiceFinalizer) {
		t.Fatalf("expected finalizer added, got %v", fresh.Finalizers)
	}
}

func TestReconcilePersistStatus_UpdatesStatus(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := gatewayTestScheme(t)
	insvc := &infernexv1alpha1.InferNexService{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "ns-a", Generation: 3},
		Spec: infernexv1alpha1.InferNexServiceSpec{
			SourceRef: &infernexv1alpha1.SourceRef{Kind: "LLMInferenceService", Name: "llm"},
		},
	}
	cl := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&infernexv1alpha1.InferNexService{}).
		WithObjects(insvc).
		Build()
	r := &InferNexServiceReconciler{Client: cl, Scheme: s, Recorder: record.NewFakeRecorder(4)}

	res, err := r.reconcilePersistStatus(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns-a", Name: "demo"}}, map[string]componentPlan{}, infernexv1alpha1.InferNexServiceSpec{}, "aggregate")
	if err != nil {
		t.Fatalf("reconcilePersistStatus error: %v", err)
	}
	if res.Requeue {
		t.Fatalf("unexpected requeue: %#v", res)
	}
	fresh := &infernexv1alpha1.InferNexService{}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "ns-a", Name: "demo"}, fresh); err != nil {
		t.Fatalf("get infernexservice: %v", err)
	}
	if fresh.Status.Mode != "aggregate" || fresh.Status.ObservedGeneration != 3 {
		t.Fatalf("unexpected status after persist: %#v", fresh.Status)
	}
	if len(fresh.Status.Conditions) == 0 {
		t.Fatal("expected conditions written to status")
	}
}

func containsString(list []string, target string) bool {
	for _, s := range list {
		if s == target {
			return true
		}
	}
	return false
}
