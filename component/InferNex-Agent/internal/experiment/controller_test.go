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

package experiment

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infernexv1alpha1 "gitcode.com/openFuyao/InferNex/api/v1alpha1"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/changesafety"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/diagnostics"
)

type fakeDiagnoser struct {
	criticalService string
	criticalGate    <-chan struct{}
}

func (f *fakeDiagnoser) Diagnose(
	_ context.Context,
	request diagnostics.Request,
) (diagnostics.Report, error) {
	report := diagnostics.Report{
		Service:   diagnostics.ServiceReference{Namespace: request.Namespace, Name: request.Name},
		Incidents: []diagnostics.Incident{},
	}
	criticalEnabled := f.criticalGate == nil
	if f.criticalGate != nil {
		select {
		case <-f.criticalGate:
			criticalEnabled = true
		default:
		}
	}
	if request.Name == f.criticalService && criticalEnabled {
		report.Incidents = append(report.Incidents, diagnostics.Incident{
			RootCategory: "kv-transport-failure",
			Severity:     diagnostics.SeverityCritical,
		})
	}
	return report, nil
}

func TestControllerRunsOrderedSingleFeatureStage(t *testing.T) {
	t.Parallel()
	controller, kubeClient, planStore := newTestController(t, &fakeDiagnoser{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := controller.Start(ctx); err != nil {
		t.Fatal(err)
	}
	plan, err := controller.Create(ctx, Request{
		Namespace:       "models",
		BaselineName:    "stable",
		CandidatePrefix: "trial",
		FeatureProfiles: []string{"enable-mooncake"},
		Confirm:         true,
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := waitForCandidate(t, kubeClient, "trial-s01")
	if len(candidate.Spec.BaseRefs) != 2 ||
		candidate.Spec.BaseRefs[0].Name != "enable-mooncake" ||
		candidate.Spec.BaseRefs[1].Name != "stable-base" {
		t.Fatalf("feature profile must precede the stable baseRefs: %#v", candidate.Spec.BaseRefs)
	}
	markReady(t, kubeClient, candidate)

	completed := waitForPlan(t, planStore, plan.ID, PlanStatusCompleted)
	if completed.StableService != "trial-s01" || completed.Stages[0].Status != StageStatusPassed {
		t.Fatalf("unexpected completed plan: %#v", completed)
	}
	if completed.Stages[0].ChangeID == "" {
		t.Fatal("stage change id was not recorded")
	}
}

func TestControllerRollsBackPreReadinessDiagnosticRegression(t *testing.T) {
	t.Parallel()
	criticalGate := make(chan struct{})
	controller, kubeClient, planStore := newTestController(t, &fakeDiagnoser{
		criticalService: "trial-s01",
		criticalGate:    criticalGate,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := controller.Start(ctx); err != nil {
		t.Fatal(err)
	}
	plan, err := controller.Create(ctx, Request{
		Namespace:       "models",
		BaselineName:    "stable",
		CandidatePrefix: "trial",
		FeatureProfiles: []string{"enable-mooncake"},
		Confirm:         true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = waitForCandidate(t, kubeClient, "trial-s01")
	close(criticalGate)

	failed := waitForPlan(t, planStore, plan.ID, PlanStatusFailed)
	if failed.StableService != "stable" || failed.Stages[0].Status != StageStatusRolledBack {
		t.Fatalf("unexpected failed plan: %#v", failed)
	}
	err = kubeClient.Get(context.Background(), types.NamespacedName{Namespace: "models", Name: "trial-s01"}, &infernexv1alpha1.InferNexService{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("candidate was not rolled back: %v", err)
	}
	if err := kubeClient.Get(context.Background(), types.NamespacedName{Namespace: "models", Name: "stable"}, &infernexv1alpha1.InferNexService{}); err != nil {
		t.Fatalf("stable baseline was changed: %v", err)
	}
}

func TestControllerBuildsEachStageFromLastPassedCandidate(t *testing.T) {
	t.Parallel()
	controller, kubeClient, planStore := newTestController(t, &fakeDiagnoser{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := controller.Start(ctx); err != nil {
		t.Fatal(err)
	}
	plan, err := controller.Create(ctx, Request{
		Namespace:       "models",
		BaselineName:    "stable",
		CandidatePrefix: "ordered",
		FeatureProfiles: []string{"enable-mooncake", "enable-cache-indexer"},
		Confirm:         true,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := waitForCandidate(t, kubeClient, "ordered-s01")
	markReady(t, kubeClient, first)
	second := waitForCandidate(t, kubeClient, "ordered-s02")
	if len(second.Spec.BaseRefs) != 3 ||
		second.Spec.BaseRefs[0].Name != "enable-cache-indexer" ||
		second.Spec.BaseRefs[1].Name != "enable-mooncake" ||
		second.Spec.BaseRefs[2].Name != "stable-base" {
		t.Fatalf("second stage did not build on the passed candidate: %#v", second.Spec.BaseRefs)
	}
	markReady(t, kubeClient, second)

	completed := waitForPlan(t, planStore, plan.ID, PlanStatusCompleted)
	if completed.StableService != "ordered-s02" ||
		completed.Stages[0].Status != StageStatusPassed ||
		completed.Stages[1].Status != StageStatusPassed {
		t.Fatalf("unexpected ordered experiment: %#v", completed)
	}
}

func TestControllerRejectsInlineBaseline(t *testing.T) {
	t.Parallel()
	controller, kubeClient, _ := newTestController(t, &fakeDiagnoser{})
	baseline := &infernexv1alpha1.InferNexService{}
	key := types.NamespacedName{Namespace: "models", Name: "stable"}
	if err := kubeClient.Get(context.Background(), key, baseline); err != nil {
		t.Fatal(err)
	}
	baseline.Spec.Engine = &infernexv1alpha1.InferenceEngineSpec{}
	if err := kubeClient.Update(context.Background(), baseline); err != nil {
		t.Fatal(err)
	}
	_, err := controller.Create(context.Background(), Request{
		Namespace:       "models",
		BaselineName:    "stable",
		CandidatePrefix: "trial",
		FeatureProfiles: []string{"enable-mooncake"},
		Confirm:         true,
	})
	if err == nil {
		t.Fatal("expected inline baseline to be rejected")
	}
}

func TestControllerRejectsConcurrentPlan(t *testing.T) {
	t.Parallel()
	controller, _, _ := newTestController(t, &fakeDiagnoser{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := controller.Start(ctx); err != nil {
		t.Fatal(err)
	}
	request := Request{
		Namespace:       "models",
		BaselineName:    "stable",
		CandidatePrefix: "first",
		FeatureProfiles: []string{"enable-mooncake"},
		Confirm:         true,
	}
	if _, err := controller.Create(ctx, request); err != nil {
		t.Fatal(err)
	}
	request.CandidatePrefix = "second"
	if _, err := controller.Create(ctx, request); err == nil {
		t.Fatal("expected a concurrent experiment plan to be rejected")
	}
}

func TestControllerRefusesMultiplePendingPlansOnResume(t *testing.T) {
	t.Parallel()
	controller, _, store := newTestController(t, &fakeDiagnoser{})
	now := time.Date(2026, 8, 6, 8, 0, 0, 0, time.UTC)
	for index, id := range []string{
		"00112233445566778899aabbccddeeff",
		"ffeeddccbbaa99887766554433221100",
	} {
		if err := store.Append(Plan{
			APIVersion: "agent.infernex.io/v1alpha1",
			Kind:       "InferNexExperiment",
			ID:         id,
			Status:     PlanStatusRunning,
			CreatedAt:  now.Add(time.Duration(index) * time.Second),
			UpdatedAt:  now.Add(time.Duration(index) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := controller.Start(context.Background()); err == nil {
		t.Fatal("expected unsafe parallel resume to be rejected")
	}
}

func newTestController(
	t *testing.T,
	diagnoser diagnostics.Diagnoser,
) (*Controller, client.Client, Store) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := infernexv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	baseline := &infernexv1alpha1.InferNexService{
		ObjectMeta: metav1.ObjectMeta{Namespace: "models", Name: "stable"},
		Spec: infernexv1alpha1.InferNexServiceSpec{
			BaseRefs: []infernexv1alpha1.NamedRef{{Name: "stable-base"}},
		},
		Status: infernexv1alpha1.InferNexServiceStatus{Ready: true},
	}
	feature := &infernexv1alpha1.InferNexServiceConfig{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "templates", Name: "enable-mooncake",
			Labels: map[string]string{ApprovedFeatureLabel: "true"},
		},
		Spec: infernexv1alpha1.InferNexServiceConfigSpec{
			InferNexServiceSpec: infernexv1alpha1.InferNexServiceSpec{
				Components: &infernexv1alpha1.InfernexComponentsSpec{
					Mooncake: &infernexv1alpha1.MooncakeComponentSpec{},
				},
			},
		},
	}
	enabled := true
	cacheFeature := &infernexv1alpha1.InferNexServiceConfig{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "templates", Name: "enable-cache-indexer",
			Labels: map[string]string{ApprovedFeatureLabel: "true"},
		},
		Spec: infernexv1alpha1.InferNexServiceConfigSpec{
			InferNexServiceSpec: infernexv1alpha1.InferNexServiceSpec{
				Components: &infernexv1alpha1.InfernexComponentsSpec{
					CacheIndexer: &infernexv1alpha1.CacheIndexerComponentSpec{Enabled: &enabled},
				},
			},
		},
	}
	kubeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&infernexv1alpha1.InferNexService{}).
		WithObjects(baseline, feature, cacheFeature).
		Build()
	planStore := NewMemoryStore()
	controller, err := New(
		kubeClient,
		changesafety.NewMemoryStore(),
		planStore,
		diagnoser,
		Config{
			TemplateNamespace:  "templates",
			ReadinessTimeout:   2 * time.Second,
			SoakDuration:       5 * time.Millisecond,
			PollInterval:       time.Millisecond,
			DiagnosticInterval: time.Millisecond,
			DiagnosticsMinutes: 1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return controller, kubeClient, planStore
}

func waitForCandidate(
	t *testing.T,
	kubeClient client.Client,
	name string,
) *infernexv1alpha1.InferNexService {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		candidate := &infernexv1alpha1.InferNexService{}
		err := kubeClient.Get(context.Background(), types.NamespacedName{Namespace: "models", Name: name}, candidate)
		if err == nil {
			return candidate
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("candidate %s was not created", name)
	return nil
}

func markReady(
	t *testing.T,
	kubeClient client.Client,
	candidate *infernexv1alpha1.InferNexService,
) {
	t.Helper()
	candidate.Status.Ready = true
	candidate.Status.ObservedGeneration = candidate.Generation
	if err := kubeClient.Status().Update(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
}

func waitForPlan(t *testing.T, store Store, id string, status string) Plan {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		plan, err := store.Latest(id)
		if err == nil && plan.Status == status {
			return plan
		}
		time.Sleep(time.Millisecond)
	}
	plan, _ := store.Latest(id)
	t.Fatalf("plan did not reach %s: %#v", status, plan)
	return Plan{}
}
