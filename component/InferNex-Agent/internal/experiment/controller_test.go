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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	infernexv1alpha1 "gitcode.com/openFuyao/InferNex/api/v1alpha1"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/changesafety"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/diagnostics"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/slo"
)

func TestControllerSLORegressionDoesNotPromote(t *testing.T) {
	baselineServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer baselineServer.Close()
	candidateServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"bad"},"finish_reason":"stop"}]}`))
	}))
	defer candidateServer.Close()
	profile := slo.Profile{ID: "smoke", Version: "1", Approved: true, Model: "test", Endpoints: map[string]string{"models/stable": baselineServer.URL, "models/trial-s01": candidateServer.URL}, Cases: []slo.Case{{ID: "case", Prompt: "say ok", ExactAnswer: "ok"}}, Samples: 2, MaxTokens: 8, TimeoutMillis: 500, MinSamples: 2, Thresholds: slo.Thresholds{MinSuccessRate: 1, MaxP95Millis: 500, MaxP95RegressionRatio: 10, MinThroughputRatio: .01}}
	directory := t.TempDir()
	data, _ := json.Marshal(profile)
	if err := os.WriteFile(filepath.Join(directory, "smoke.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	profiles, err := slo.LoadProfiles(directory)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := slo.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	controller, kubeClient, planStore := newTestController(t, &fakeDiagnoser{})
	controller.config.SLOProfiles = profiles
	controller.config.SLORunner = slo.NewRunner(evidence, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := controller.Start(ctx); err != nil {
		t.Fatal(err)
	}
	plan, err := controller.Create(ctx, Request{Namespace: "models", BaselineName: "stable", CandidatePrefix: "trial", FeatureProfiles: []string{"enable-mooncake"}, SLOProfile: "smoke", Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	markReady(t, kubeClient, waitForCandidate(t, kubeClient, "trial-s01"))
	failed := waitForPlan(t, planStore, plan.ID, PlanStatusFailed)
	if failed.StableService != "stable" || failed.Stages[0].Status != StageStatusRolledBack || failed.Stages[0].SLO == nil || failed.Stages[0].SLO.Decision != "regression" {
		t.Fatalf("SLO regression promoted candidate: %+v", failed)
	}
}

func TestControllerSLOIntentAfterRestartDoesNotPromote(t *testing.T) {
	baselineServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer baselineServer.Close()
	candidateServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer candidateServer.Close()
	profile := slo.Profile{ID: "smoke", Version: "1", Approved: true, Model: "test", Endpoints: map[string]string{"models/stable": baselineServer.URL, "models/trial-s01": candidateServer.URL}, Cases: []slo.Case{{ID: "case", Prompt: "say ok", ExactAnswer: "ok"}}, Samples: 2, MaxTokens: 8, TimeoutMillis: 500, MinSamples: 2, Thresholds: slo.Thresholds{MinSuccessRate: 1, MaxP95Millis: 500, MaxP95RegressionRatio: 10, MinThroughputRatio: .01}}
	directory := t.TempDir()
	data, _ := json.Marshal(profile)
	if err := os.WriteFile(filepath.Join(directory, "smoke.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	profiles, err := slo.LoadProfiles(directory)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := slo.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	controller, kubeClient, planStore := newTestController(t, &fakeDiagnoser{})
	controller.config.SLOProfiles = profiles
	controller.config.SLORunner = slo.NewRunner(evidence, nil)
	ctx, cancel := context.WithCancel(context.Background())
	if err := controller.Start(ctx); err != nil {
		t.Fatal(err)
	}
	plan, err := controller.Create(ctx, Request{Namespace: "models", BaselineName: "stable", CandidatePrefix: "trial", FeatureProfiles: []string{"enable-mooncake"}, SLOProfile: "smoke", Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	candidate := waitForCandidate(t, kubeClient, "trial-s01")
	cancel()
	time.Sleep(10 * time.Millisecond)
	persisted, err := planStore.Latest(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted.Stages[0].SLO = &slo.Result{RunID: "interrupted", Decision: "running"}
	if err := planStore.Append(persisted); err != nil {
		t.Fatal(err)
	}
	resumed, err := New(kubeClient, controller.changes, planStore, &fakeDiagnoser{}, controller.config)
	if err != nil {
		t.Fatal(err)
	}
	newCtx, newCancel := context.WithCancel(context.Background())
	defer newCancel()
	if err := resumed.Start(newCtx); err != nil {
		t.Fatal(err)
	}
	markReady(t, kubeClient, candidate)
	failed := waitForPlan(t, planStore, plan.ID, PlanStatusFailed)
	if failed.StableService != "stable" || failed.Stages[0].Status != StageStatusRolledBack || failed.Stages[0].SLO.Decision != "inconclusive" {
		t.Fatalf("interrupted SLO run promoted candidate: %+v", failed)
	}
}

func TestSLOTemplateFingerprintsIncludeNestedReferences(t *testing.T) {
	controller, kubeClient, _ := newTestController(t, &fakeDiagnoser{})
	ctx := context.Background()
	parent := &infernexv1alpha1.InferNexServiceConfig{ObjectMeta: metav1.ObjectMeta{Namespace: "templates", Name: "nested-parent"}, Spec: infernexv1alpha1.InferNexServiceConfigSpec{InferNexServiceSpec: infernexv1alpha1.InferNexServiceSpec{BaseRefs: []infernexv1alpha1.NamedRef{{Name: "stable-base"}}}}}
	if err := kubeClient.Create(ctx, parent); err != nil {
		t.Fatal(err)
	}
	before, err := controller.templateFingerprints(ctx, []string{"nested-parent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 2 {
		t.Fatalf("nested references missing: %+v", before)
	}
	service := &infernexv1alpha1.InferNexService{Spec: infernexv1alpha1.InferNexServiceSpec{BaseRefs: []infernexv1alpha1.NamedRef{{Name: "nested-parent"}}}}
	stage := &Stage{TemplateFingerprints: before}
	if err := controller.checkTemplateFingerprints(ctx, stage, service, service); err != nil {
		t.Fatal(err)
	}
	base := &infernexv1alpha1.InferNexServiceConfig{}
	if err := kubeClient.Get(ctx, types.NamespacedName{Namespace: "templates", Name: "stable-base"}, base); err != nil {
		t.Fatal(err)
	}
	base.Spec.BaseRefs = []infernexv1alpha1.NamedRef{{Name: "nested-parent"}}
	if err := kubeClient.Update(ctx, base); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.templateFingerprints(ctx, []string{"nested-parent"}); err == nil {
		t.Fatal("reference cycle accepted")
	}
	if err := controller.checkTemplateFingerprints(ctx, stage, service, service); err == nil {
		t.Fatal("nested template change accepted")
	}
}

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

func TestCandidateRollbackPreservesConcurrentReplacementOrEdit(t *testing.T) {
	for _, mutation := range []string{"replacement", "edit"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			if err := infernexv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			candidate := &infernexv1alpha1.InferNexService{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "models", Name: "trial-s01", UID: "original-candidate", ResourceVersion: "12",
					Labels:      map[string]string{managedByLabel: managedByAgent, experimentIDLabel: "experiment"},
					Annotations: map[string]string{changeIDAnnotation: "change"},
				},
			}
			var retained *infernexv1alpha1.InferNexService
			deleteCalled := false
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(candidate).
				WithInterceptorFuncs(interceptor.Funcs{
					Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
						deleteCalled = true
						pre := (&client.DeleteOptions{}).ApplyOptions(opts).Preconditions
						if pre == nil || pre.UID == nil || pre.ResourceVersion == nil ||
							*pre.UID != candidate.UID || *pre.ResourceVersion != candidate.ResourceVersion {
							t.Fatalf("candidate rollback must bind the checked object identity: %#v", pre)
						}
						retained = candidate.DeepCopy()
						retained.Spec.BaseRefs = []infernexv1alpha1.NamedRef{{Name: "operator-change"}}
						if mutation == "replacement" {
							if err := c.Delete(ctx, candidate); err != nil {
								t.Fatal(err)
							}
							retained.UID, retained.ResourceVersion = "replacement-candidate", ""
							if err := c.Create(ctx, retained); err != nil {
								t.Fatal(err)
							}
						} else if err := c.Update(ctx, retained); err != nil {
							t.Fatal(err)
						}
						// Simulate API precondition enforcement, including UID, which
						// the controller-runtime fake client does not enforce.
						if retained.UID == *pre.UID && retained.ResourceVersion == *pre.ResourceVersion {
							t.Fatal("test mutation did not change the protected identity")
						}
						return apierrors.NewConflict(infernexv1alpha1.GroupVersion.WithResource("infernexservices").GroupResource(), obj.GetName(), errors.New("delete precondition failed"))
					},
				}).Build()
			controller := &Controller{client: kubeClient}
			err := controller.deleteOwnedCandidate(ctx, client.ObjectKeyFromObject(candidate), "experiment", "change")
			if !deleteCalled || !apierrors.IsConflict(err) {
				t.Fatalf("expected candidate rollback conflict, got %v", err)
			}
			current := &infernexv1alpha1.InferNexService{}
			if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(retained), current); err != nil {
				t.Fatalf("concurrent candidate was removed: %v", err)
			}
			if current.UID != retained.UID || current.Spec.BaseRefs[0].Name != "operator-change" {
				t.Fatalf("concurrent candidate was changed: %#v", current)
			}
		})
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
	stableBase := &infernexv1alpha1.InferNexServiceConfig{ObjectMeta: metav1.ObjectMeta{Namespace: "templates", Name: "stable-base"}}
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
		WithObjects(baseline, feature, cacheFeature, stableBase).
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
