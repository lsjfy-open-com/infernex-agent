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

package deployer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infernexv1alpha1 "gitcode.com/openFuyao/InferNex/api/v1alpha1"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/changesafety"
)

func TestDeployCreatesOnlyCatalogInferNexServiceAndIsIdempotent(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := infernexv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	domainDeployer := New(kubeClient)
	request := Request{
		Namespace: "models",
		Name:      "tiny",
		CatalogID: TinyModelCatalogID,
		Confirm:   true,
	}

	first, err := domainDeployer.Deploy(context.Background(), request)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if first.Operation != "created" ||
		first.Endpoint != "http://tiny-engine-aggregate.models.svc:8080" {
		t.Fatalf("unexpected result: %#v", first)
	}

	created := &infernexv1alpha1.InferNexService{}
	key := types.NamespacedName{Namespace: "models", Name: "tiny"}
	if err := kubeClient.Get(context.Background(), key, created); err != nil {
		t.Fatalf("get created resource: %v", err)
	}
	if created.Labels[managedByLabel] != managedByAgent ||
		created.Spec.Model == nil ||
		created.Spec.Model.Name != modelName {
		t.Fatalf("unexpected created service: %#v", created)
	}
	podSpec := created.Spec.Engine.Template.Spec
	if len(created.Spec.Engine.Template.Labels) != 0 {
		t.Fatalf(
			"catalog PodTemplate labels are pruned by the InferNexService CRD: %#v",
			created.Spec.Engine.Template.Labels,
		)
	}
	if len(podSpec.Containers) != 1 ||
		podSpec.Containers[0].Image != serverImage ||
		len(podSpec.InitContainers) != 1 ||
		!strings.Contains(podSpec.InitContainers[0].Args[0], modelSHA) {
		t.Fatalf("catalog workload was not immutable: %#v", podSpec)
	}

	second, err := domainDeployer.Deploy(context.Background(), request)
	if err != nil {
		t.Fatalf("idempotent deploy: %v", err)
	}
	if second.Operation != "already-exists" {
		t.Fatalf("idempotent operation = %q", second.Operation)
	}
}

func TestAgenticDeploymentDiscoversAndReusesStableInferNexSources(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := infernexv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	stable := tinyModelService("production", "qwen-stable")
	stable.Generation = 3
	stable.Status.Ready = true
	stable.Status.ObservedGeneration = 3
	stable.Status.Mode = "aggregate"
	stable.Spec.SourceRef = &infernexv1alpha1.SourceRef{
		APIVersion: "serving.kserve.io/v1alpha1",
		Kind:       "LLMInferenceService",
		Name:       "qwen-source",
	}
	stable.Spec.Model.URI = "https://user:secret@example.com/models/qwen?token=secret#fragment"
	profile := &infernexv1alpha1.InferNexServiceConfig{
		ObjectMeta: metav1.ObjectMeta{Namespace: "infernex-bridge-system", Name: "approved-engine"},
		Spec: infernexv1alpha1.InferNexServiceConfigSpec{
			InferNexServiceSpec: *stable.Spec.DeepCopy(),
		},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(stable, profile).Build()
	domainDeployer := New(
		kubeClient,
		WithDeploymentScope(
			"infernex-agent-workspace",
			"infernex-bridge-system",
			[]string{"production", "production"},
		),
	)

	sources, err := domainDeployer.ListSources(context.Background())
	if err != nil {
		t.Fatalf("list sources: %v", err)
	}
	if sources.TargetNamespace != "infernex-agent-workspace" || len(sources.Sources) != 2 {
		t.Fatalf("unexpected sources: %#v", sources)
	}
	if sources.Sources[1].ModelURI != "https://example.com/models/qwen" {
		t.Fatalf("deployment source leaked model URI credentials: %#v", sources.Sources[1])
	}

	result, err := domainDeployer.Deploy(context.Background(), Request{
		Name: "qwen-copy", SourceID: "service:production:qwen-stable", Confirm: true,
	})
	if err != nil {
		t.Fatalf("deploy stable source: %v", err)
	}
	if result.Namespace != "infernex-agent-workspace" || result.SourceID != "service:production:qwen-stable" {
		t.Fatalf("unexpected deployment result: %#v", result)
	}
	created := &infernexv1alpha1.InferNexService{}
	key := types.NamespacedName{Namespace: "infernex-agent-workspace", Name: "qwen-copy"}
	if err := kubeClient.Get(context.Background(), key, created); err != nil {
		t.Fatalf("get Agent deployment: %v", err)
	}
	expected := stable.Spec.DeepCopy()
	expected.SourceRef.Namespace = stable.Namespace
	if !equality.Semantic.DeepEqual(created.Spec, *expected) ||
		created.Annotations[sourceIDAnnotation] != "service:production:qwen-stable" {
		t.Fatalf("deployment did not preserve the stable spec semantics: %#v", created)
	}
}

func TestDeployRejectsUnownedCollisionAndSpecDrift(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := infernexv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	unowned := tinyModelService("models", "unowned")
	unowned.Labels = nil
	drifted := tinyModelService("models", "drifted")
	drifted.Spec.Model.Name = "changed"
	kubeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(unowned, drifted).
		Build()
	domainDeployer := New(kubeClient)

	for _, name := range []string{"unowned", "drifted"} {
		_, err := domainDeployer.Deploy(context.Background(), Request{
			Namespace: "models",
			Name:      name,
			CatalogID: TinyModelCatalogID,
			Confirm:   true,
		})
		if err == nil {
			t.Fatalf("deploy %q unexpectedly succeeded", name)
		}
	}
}

func TestDeleteOnlyRemovesOwnedCatalogResource(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := infernexv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	owned := tinyModelService("models", "tiny")
	unowned := tinyModelService("models", "keep")
	unowned.Labels = nil
	kubeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(owned, unowned).
		Build()
	domainDeployer := New(kubeClient)

	result, err := domainDeployer.Delete(context.Background(), Request{
		Namespace: "models",
		Name:      "tiny",
		CatalogID: TinyModelCatalogID,
		Confirm:   true,
	})
	if err != nil || result.Operation != "deleted" {
		t.Fatalf("delete result=%#v err=%v", result, err)
	}
	if _, err := domainDeployer.Delete(context.Background(), Request{
		Namespace: "models",
		Name:      "keep",
		CatalogID: TinyModelCatalogID,
		Confirm:   true,
	}); err == nil {
		t.Fatal("delete of unowned resource unexpectedly succeeded")
	}
}

func TestRequestsRequireFixedCatalogAndConfirmation(t *testing.T) {
	for _, request := range []Request{
		{Namespace: "models", Name: "tiny", CatalogID: TinyModelCatalogID},
		{Namespace: "models", Name: "tiny", CatalogID: "arbitrary", Confirm: true},
		{Namespace: "INVALID", Name: "tiny", CatalogID: TinyModelCatalogID, Confirm: true},
	} {
		if _, err := validateRequest(request, "deploy"); err == nil {
			t.Fatalf("request unexpectedly passed validation: %#v", request)
		}
	}
}

func TestFailedNewDeploymentAutomaticallyRestoresAbsentState(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := infernexv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := changesafety.NewMemoryStore()
	domainDeployer := New(
		kubeClient,
		WithStore(store),
		WithReadiness(25*time.Millisecond, 2*time.Millisecond),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := domainDeployer.Start(ctx); err != nil {
		t.Fatal(err)
	}

	result, err := domainDeployer.Deploy(ctx, Request{
		Namespace: "models",
		Name:      "will-fail",
		CatalogID: TinyModelCatalogID,
		Confirm:   true,
	})
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if result.ChangeID == "" || result.ChangeStatus != changesafety.StatusApplied {
		t.Fatalf("deployment result = %#v", result)
	}

	deadline := time.Now().Add(time.Second)
	for {
		record, recordErr := store.Latest(result.ChangeID)
		if recordErr != nil {
			t.Fatal(recordErr)
		}
		if record.Status == changesafety.StatusRolledBack {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("change did not roll back; latest record = %#v", record)
		}
		time.Sleep(5 * time.Millisecond)
	}

	key := types.NamespacedName{Namespace: "models", Name: "will-fail"}
	err = kubeClient.Get(ctx, key, &infernexv1alpha1.InferNexService{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("failed deployment still exists: %v", err)
	}
}

func TestRestartResumesPlannedDeploymentCreatedBeforeEventFlush(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := infernexv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	const changeID = "00112233445566778899aabbccddeeff"
	service := tinyModelService("models", "interrupted")
	service.UID = "interrupted-create-uid"
	service.Annotations = map[string]string{changeIDAnnotation: changeID}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(service).Build()
	store := changesafety.NewMemoryStore()
	request := Request{
		Namespace: "models",
		Name:      "interrupted",
		CatalogID: TinyModelCatalogID,
		Confirm:   true,
	}
	record := newChangeRecord(
		changeID,
		"deploy",
		changesafety.StatusPlanned,
		request,
		nil,
		serviceJSON(t, service),
		"test interrupted create",
	)
	if err := store.Append(record); err != nil {
		t.Fatal(err)
	}
	domainDeployer := New(
		kubeClient,
		WithStore(store),
		WithReadiness(25*time.Millisecond, 2*time.Millisecond),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := domainDeployer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	latest, err := store.Latest(changeID)
	if err != nil || latest.Target.UID != service.UID {
		t.Fatalf("recovered change did not persist the created UID: record=%#v err=%v", latest, err)
	}

	deadline := time.Now().Add(time.Second)
	for {
		latest, err := store.Latest(changeID)
		if err != nil {
			t.Fatal(err)
		}
		if latest.Status == changesafety.StatusRolledBack {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("resumed change did not roll back: %#v", latest)
		}
		time.Sleep(5 * time.Millisecond)
	}
	key := types.NamespacedName{Namespace: "models", Name: "interrupted"}
	if err := kubeClient.Get(ctx, key, &infernexv1alpha1.InferNexService{}); !apierrors.IsNotFound(err) {
		t.Fatalf("interrupted deployment still exists: %v", err)
	}
}

func TestDeployPersistsCreatedUID(t *testing.T) {
	kubeClient := newIdentityTestClient(t)
	kubeClient.create = func(ctx context.Context, object client.Object, options ...client.CreateOption) error {
		// The API server, unlike the fake client, assigns the UID on creation.
		object.SetUID("created-service-uid")
		return kubeClient.Client.Create(ctx, object, options...)
	}
	store := changesafety.NewMemoryStore()
	domainDeployer := New(kubeClient, WithStore(store))
	result, err := domainDeployer.Deploy(context.Background(), Request{
		Namespace: "models", Name: "tiny", CatalogID: TinyModelCatalogID, Confirm: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Latest(result.ChangeID)
	if err != nil || record.Target.UID != "created-service-uid" {
		t.Fatalf("created object identity not persisted: record=%#v err=%v", record, err)
	}
}

func TestDeploymentMonitorChecksIdentityBeforeReadiness(t *testing.T) {
	for _, test := range []struct {
		name       string
		mutate     func(*infernexv1alpha1.InferNexService, *changesafety.ChangeRecord)
		wantStatus string
		wantAbsent bool
	}{
		{name: "matching identity", wantStatus: changesafety.StatusCommitted},
		{name: "legacy journal without UID", mutate: func(_ *infernexv1alpha1.InferNexService, record *changesafety.ChangeRecord) {
			record.Target.UID = ""
		}, wantStatus: changesafety.StatusCommitted},
		{name: "ownership transferred", mutate: func(service *infernexv1alpha1.InferNexService, _ *changesafety.ChangeRecord) {
			service.Labels[managedByLabel] = "another-controller"
		}, wantStatus: changesafety.StatusRollbackFailed},
		{name: "different deployment change", mutate: func(service *infernexv1alpha1.InferNexService, _ *changesafety.ChangeRecord) {
			service.Annotations[changeIDAnnotation] = "another-change"
		}, wantStatus: changesafety.StatusRollbackFailed},
		{name: "replacement with copied annotations", mutate: func(service *infernexv1alpha1.InferNexService, _ *changesafety.ChangeRecord) {
			service.UID = "replacement-uid"
		}, wantStatus: changesafety.StatusRollbackFailed},
		{name: "edited spec with same UID", mutate: func(service *infernexv1alpha1.InferNexService, _ *changesafety.ChangeRecord) {
			service.Spec.Model.Name = "operator-edit"
		}, wantStatus: changesafety.StatusRollbackFailed},
		{name: "missing desired snapshot", mutate: func(_ *infernexv1alpha1.InferNexService, record *changesafety.ChangeRecord) {
			record.Desired = nil
		}, wantStatus: changesafety.StatusRollbackFailed},
		{name: "invalid desired snapshot", mutate: func(_ *infernexv1alpha1.InferNexService, record *changesafety.ChangeRecord) {
			record.Desired = json.RawMessage(`{"spec": "invalid"}`)
		}, wantStatus: changesafety.StatusRollbackFailed},
		{name: "desired snapshot without spec", mutate: func(_ *infernexv1alpha1.InferNexService, record *changesafety.ChangeRecord) {
			record.Desired = json.RawMessage(`{}`)
		}, wantStatus: changesafety.StatusRollbackFailed},
		{name: "current Degraded takes precedence over Ready", mutate: func(service *infernexv1alpha1.InferNexService, _ *changesafety.ChangeRecord) {
			service.Status.Conditions = []metav1.Condition{{Type: "Degraded", Status: metav1.ConditionTrue, ObservedGeneration: service.Generation}}
		}, wantStatus: changesafety.StatusRolledBack, wantAbsent: true},
		{name: "stale Degraded cannot override current Ready", mutate: func(service *infernexv1alpha1.InferNexService, _ *changesafety.ChangeRecord) {
			service.Status.Conditions = []metav1.Condition{{Type: "Degraded", Status: metav1.ConditionTrue, ObservedGeneration: service.Generation - 1}}
		}, wantStatus: changesafety.StatusCommitted},
		{name: "undated Degraded cannot override current Ready", mutate: func(service *infernexv1alpha1.InferNexService, _ *changesafety.ChangeRecord) {
			service.Status.Conditions = []metav1.Condition{{Type: "Degraded", Status: metav1.ConditionTrue}}
		}, wantStatus: changesafety.StatusCommitted},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, record := deploymentIdentityFixture(t)
			service.Generation = 2
			service.Status.Ready = true
			service.Status.ObservedGeneration = 2
			if test.mutate != nil {
				test.mutate(service, &record)
			}
			kubeClient := newIdentityTestClient(t, service)
			store := changesafety.NewMemoryStore()
			if err := store.Append(record); err != nil {
				t.Fatal(err)
			}
			domainDeployer := New(kubeClient, WithStore(store), WithReadiness(time.Second, time.Millisecond))
			domainDeployer.monitorDeployment(context.Background(), record)
			latest, err := store.Latest(record.ID)
			if err != nil || latest.Status != test.wantStatus {
				t.Fatalf("monitor status: record=%#v err=%v, want %s", latest, err, test.wantStatus)
			}
			err = kubeClient.Get(context.Background(), client.ObjectKeyFromObject(service), &infernexv1alpha1.InferNexService{})
			if test.wantAbsent && !apierrors.IsNotFound(err) {
				t.Fatalf("failed deployment was not rolled back: %v", err)
			}
			if !test.wantAbsent && err != nil {
				t.Fatalf("monitor removed a deployment that must be preserved: %v", err)
			}
		})
	}
}

func TestRollbackPreservesReplacementWithCopiedOwnership(t *testing.T) {
	service, record := deploymentIdentityFixture(t)
	service.UID = "replacement-uid"
	kubeClient := newIdentityTestClient(t, service)
	store := changesafety.NewMemoryStore()
	domainDeployer := New(kubeClient, WithStore(store))
	domainDeployer.rollbackDeployment(context.Background(), record, "readiness deadline expired")
	latest, err := store.Latest(record.ID)
	if err != nil || latest.Status != changesafety.StatusRollbackFailed || !strings.Contains(latest.Message, "UID changed") {
		t.Fatalf("replacement must fail rollback: record=%#v err=%v", latest, err)
	}
	if err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(service), &infernexv1alpha1.InferNexService{}); err != nil {
		t.Fatalf("replacement was not preserved: %v", err)
	}
}

func TestDeploymentDeletionRejectsConcurrentIdentityChanges(t *testing.T) {
	for _, action := range []string{"explicit deletion", "automatic rollback"} {
		for _, race := range []string{"replacement", "ownership update"} {
			t.Run(action+"/"+race, func(t *testing.T) {
				service, record := deploymentIdentityFixture(t)
				kubeClient := newIdentityTestClient(t, service)
				deleteCalls := 0
				kubeClient.delete = func(ctx context.Context, object client.Object, options ...client.DeleteOption) error {
					deleteCalls++
					var deleteOptions client.DeleteOptions
					for _, option := range options {
						option.ApplyToDelete(&deleteOptions)
					}
					preconditions := deleteOptions.Preconditions
					if preconditions == nil || preconditions.UID == nil || preconditions.ResourceVersion == nil ||
						*preconditions.UID != object.GetUID() || *preconditions.ResourceVersion != object.GetResourceVersion() {
						t.Fatal("delete must be conditional on the exact UID and resourceVersion whose ownership was checked")
					}
					current := &infernexv1alpha1.InferNexService{}
					if err := kubeClient.Client.Get(ctx, client.ObjectKeyFromObject(object), current); err != nil {
						t.Fatal(err)
					}
					if race == "replacement" {
						if err := kubeClient.Client.Delete(ctx, current); err != nil {
							t.Fatal(err)
						}
						current.UID = "replacement-uid"
						current.ResourceVersion = ""
						if err := kubeClient.Client.Create(ctx, current); err != nil {
							t.Fatal(err)
						}
					} else {
						current.Labels[managedByLabel] = "another-controller"
						if err := kubeClient.Client.Update(ctx, current); err != nil {
							t.Fatal(err)
						}
					}
					// Model the API server's UID and resourceVersion checks explicitly:
					// fake client versions do not all enforce UID preconditions.
					if *preconditions.UID != current.UID || *preconditions.ResourceVersion != current.ResourceVersion {
						return apierrors.NewConflict(infernexv1alpha1.GroupVersion.WithResource("infernexservices").GroupResource(),
							object.GetName(), fmt.Errorf("delete preconditions no longer match"))
					}
					return kubeClient.Client.Delete(ctx, object, options...)
				}
				store := changesafety.NewMemoryStore()
				domainDeployer := New(kubeClient, WithStore(store))
				if action == "explicit deletion" {
					_, err := domainDeployer.Delete(context.Background(), Request{
						Namespace: service.Namespace, Name: service.Name, Confirm: true,
					})
					if !apierrors.IsConflict(err) {
						t.Fatalf("delete must report the concurrent change: %v", err)
					}
				} else {
					domainDeployer.rollbackDeployment(context.Background(), record, "readiness deadline expired")
					latest, err := store.Latest(record.ID)
					if err != nil || latest.Status != changesafety.StatusRollbackFailed {
						t.Fatalf("rollback must preserve conflict state: record=%#v err=%v", latest, err)
					}
				}
				if deleteCalls != 1 {
					t.Fatalf("conditional delete must not be retried without rechecking ownership: calls=%d", deleteCalls)
				}
				current := &infernexv1alpha1.InferNexService{}
				if err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(service), current); err != nil {
					t.Fatalf("concurrently changed object must be preserved: %v", err)
				}
				if race == "replacement" && current.UID != "replacement-uid" ||
					race == "ownership update" && current.Labels[managedByLabel] != "another-controller" {
					t.Fatalf("concurrently changed identity was not preserved: %#v", current.ObjectMeta)
				}
			})
		}
	}
}

func TestDeployUsesAPIAcceptedSpecForMonitoringAndRollback(t *testing.T) {
	for _, ready := range []bool{true, false} {
		t.Run(fmt.Sprintf("ready=%t", ready), func(t *testing.T) {
			kubeClient := newIdentityTestClient(t)
			kubeClient.create = func(ctx context.Context, object client.Object, options ...client.CreateOption) error {
				service := object.(*infernexv1alpha1.InferNexService)
				service.UID = "admitted-service-uid"
				service.Generation = 1
				// Simulate an API default or admission change that is only present
				// in the Create response, not the previously persisted request.
				service.Spec.Engine.Template.Spec.DNSPolicy = corev1.DNSClusterFirst
				return kubeClient.Client.Create(ctx, object, options...)
			}
			store := changesafety.NewMemoryStore()
			domainDeployer := New(kubeClient, WithStore(store), WithReadiness(time.Second, time.Millisecond))
			if !ready {
				domainDeployer.readinessTimeout = 25 * time.Millisecond
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := domainDeployer.Start(ctx); err != nil {
				t.Fatal(err)
			}
			result, err := domainDeployer.Deploy(ctx, Request{
				Namespace: "models", Name: "tiny", CatalogID: TinyModelCatalogID, Confirm: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			key := types.NamespacedName{Namespace: "models", Name: "tiny"}
			if ready {
				service := &infernexv1alpha1.InferNexService{}
				if err := kubeClient.Get(ctx, key, service); err != nil {
					t.Fatal(err)
				}
				service.Status.Ready = true
				service.Status.ObservedGeneration = service.Generation
				if err := kubeClient.Update(ctx, service); err != nil {
					t.Fatal(err)
				}
			}
			wantStatus := changesafety.StatusRolledBack
			if ready {
				wantStatus = changesafety.StatusCommitted
			}
			record := waitForDeploymentStatus(t, store, result.ChangeID, wantStatus)
			var accepted infernexv1alpha1.InferNexService
			if err := json.Unmarshal(record.Desired, &accepted); err != nil {
				t.Fatal(err)
			}
			if accepted.Spec.Engine.Template.Spec.DNSPolicy != corev1.DNSClusterFirst {
				t.Fatal("journal did not preserve the API-accepted spec")
			}
			err = kubeClient.Get(ctx, key, &infernexv1alpha1.InferNexService{})
			if ready && err != nil || !ready && !apierrors.IsNotFound(err) {
				t.Fatalf("unexpected deployment existence after %s: %v", wantStatus, err)
			}
		})
	}
}

func TestRollbackRejectsSpecEditedAfterMonitorRead(t *testing.T) {
	service, record := deploymentIdentityFixture(t)
	service.Generation = 2
	service.Status.ObservedGeneration = 2
	service.Status.Conditions = []metav1.Condition{{Type: "Degraded", Status: metav1.ConditionTrue, ObservedGeneration: 2}}
	kubeClient := newIdentityTestClient(t, service)
	reads := 0
	kubeClient.get = func(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
		if err := kubeClient.Client.Get(ctx, key, object, options...); err != nil {
			return err
		}
		reads++
		if reads == 1 {
			// The monitor receives the original spec; rollback's fresh Get will
			// see the operator edit with the same UID and ownership annotations.
			edited := object.(*infernexv1alpha1.InferNexService).DeepCopy()
			edited.Spec.Model.Name = "operator-edit"
			if err := kubeClient.Client.Update(ctx, edited); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}
	store := changesafety.NewMemoryStore()
	domainDeployer := New(kubeClient, WithStore(store), WithReadiness(time.Second, time.Millisecond))
	domainDeployer.monitorDeployment(context.Background(), record)
	latest, err := store.Latest(record.ID)
	if err != nil || latest.Status != changesafety.StatusRollbackFailed || !strings.Contains(latest.Message, "spec drift") {
		t.Fatalf("rollback must reject a spec edited after monitoring: record=%#v err=%v", latest, err)
	}
	current := &infernexv1alpha1.InferNexService{}
	if err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(service), current); err != nil ||
		current.UID != service.UID || current.Spec.Model.Name != "operator-edit" {
		t.Fatalf("operator edit was not preserved: service=%#v err=%v", current, err)
	}
}

func TestPlannedRecoveryDoesNotAdoptUnverifiableSpec(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*infernexv1alpha1.InferNexService, *changesafety.ChangeRecord)
	}{
		{name: "edited spec", mutate: func(service *infernexv1alpha1.InferNexService, _ *changesafety.ChangeRecord) {
			service.Spec.Model.Name = "operator-edit"
		}},
		{name: "defaulting without an applied snapshot", mutate: func(service *infernexv1alpha1.InferNexService, _ *changesafety.ChangeRecord) {
			service.Spec.Engine.Template.Spec.DNSPolicy = corev1.DNSClusterFirst
		}},
		{name: "missing desired snapshot", mutate: func(_ *infernexv1alpha1.InferNexService, record *changesafety.ChangeRecord) {
			record.Desired = nil
		}},
		{name: "invalid desired snapshot", mutate: func(_ *infernexv1alpha1.InferNexService, record *changesafety.ChangeRecord) {
			record.Desired = json.RawMessage(`{"spec": "invalid"}`)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, record := deploymentIdentityFixture(t)
			record.Status = changesafety.StatusPlanned
			record.Target.UID = ""
			test.mutate(service, &record)
			kubeClient := newIdentityTestClient(t, service)
			store := changesafety.NewMemoryStore()
			if err := store.Append(record); err != nil {
				t.Fatal(err)
			}
			domainDeployer := New(kubeClient, WithStore(store), WithReadiness(time.Second, time.Millisecond))
			if err := domainDeployer.Start(context.Background()); err == nil {
				t.Fatal("restart adopted an unverifiable spec")
			}
			latest, err := store.Latest(record.ID)
			if err != nil || latest.Status != changesafety.StatusRollbackFailed || latest.Target.UID != "" ||
				string(latest.Desired) != string(record.Desired) {
				t.Fatalf("restart must retain the original baseline and record failure: record=%#v err=%v", latest, err)
			}
			current := &infernexv1alpha1.InferNexService{}
			if err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(service), current); err != nil ||
				!equality.Semantic.DeepEqual(service.Spec, current.Spec) {
				t.Fatalf("restart changed an unverifiable object: service=%#v err=%v", current, err)
			}
		})
	}
}

func TestAppliedJournalFailureRollbackPreservesEditedSpec(t *testing.T) {
	for _, edited := range []bool{false, true} {
		t.Run(fmt.Sprintf("edited=%t", edited), func(t *testing.T) {
			kubeClient := newIdentityTestClient(t)
			kubeClient.create = func(ctx context.Context, object client.Object, options ...client.CreateOption) error {
				object.SetUID("created-service-uid")
				return kubeClient.Client.Create(ctx, object, options...)
			}
			memory := changesafety.NewMemoryStore()
			var changeID string
			store := &deploymentTestStore{Store: memory}
			store.append = func(record changesafety.ChangeRecord) error {
				if record.Status != changesafety.StatusApplied {
					return memory.Append(record)
				}
				changeID = record.ID
				if edited {
					current := &infernexv1alpha1.InferNexService{}
					key := types.NamespacedName{Namespace: record.Target.Namespace, Name: record.Target.Name}
					if err := kubeClient.Get(context.Background(), key, current); err != nil {
						t.Fatal(err)
					}
					current.Spec.Model.Name = "operator-edit"
					if err := kubeClient.Update(context.Background(), current); err != nil {
						t.Fatal(err)
					}
				}
				return errors.New("injected applied journal failure")
			}
			domainDeployer := New(kubeClient, WithStore(store))
			_, err := domainDeployer.Deploy(context.Background(), Request{
				Namespace: "models", Name: "tiny", CatalogID: TinyModelCatalogID, Confirm: true,
			})
			if err == nil || !strings.Contains(err.Error(), "injected applied journal failure") {
				t.Fatalf("deployment must report journal failure: %v", err)
			}
			if edited && !strings.Contains(err.Error(), "spec drift") {
				t.Fatalf("deployment must report the refused rollback: %v", err)
			}
			current := &infernexv1alpha1.InferNexService{}
			getErr := kubeClient.Get(context.Background(), types.NamespacedName{Namespace: "models", Name: "tiny"}, current)
			if !edited && !apierrors.IsNotFound(getErr) {
				t.Fatalf("unchanged deployment must be rolled back: %v", getErr)
			}
			if edited && (getErr != nil || current.UID != "created-service-uid" || current.Spec.Model.Name != "operator-edit") {
				t.Fatalf("emergency rollback deleted the operator edit: service=%#v err=%v", current, getErr)
			}
			if edited {
				// Only the original planned snapshot survived. Restart must not
				// adopt the live edit as a new baseline or delete it later.
				restarted := New(kubeClient, WithStore(memory), WithReadiness(time.Second, time.Millisecond))
				if err := restarted.Start(context.Background()); err == nil {
					t.Fatal("restart accepted the edit after an applied journal failure")
				}
				latest, err := memory.Latest(changeID)
				if err != nil || latest.Status != changesafety.StatusRollbackFailed {
					t.Fatalf("restart must retain conflict state: record=%#v err=%v", latest, err)
				}
			}
		})
	}
}

func waitForDeploymentStatus(t *testing.T, store changesafety.Store, id, status string) changesafety.ChangeRecord {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		record, err := store.Latest(id)
		if err != nil {
			t.Fatal(err)
		}
		if record.Status == status {
			return record
		}
		if time.Now().After(deadline) {
			t.Fatalf("deployment did not reach %s: %#v", status, record)
		}
		time.Sleep(time.Millisecond)
	}
}

type deploymentTestStore struct {
	changesafety.Store
	append func(changesafety.ChangeRecord) error
}

func (s *deploymentTestStore) Append(record changesafety.ChangeRecord) error {
	return s.append(record)
}

func deploymentIdentityFixture(t *testing.T) (*infernexv1alpha1.InferNexService, changesafety.ChangeRecord) {
	t.Helper()
	const changeID = "00112233445566778899aabbccddeeff"
	service := tinyModelService("models", "tiny")
	service.UID = "original-service-uid"
	service.Annotations[changeIDAnnotation] = changeID
	record := newChangeRecord(changeID, "deploy", changesafety.StatusApplied,
		Request{Namespace: service.Namespace, Name: service.Name}, nil, serviceJSON(t, service), "resource created")
	record.Target.UID = service.UID
	return service, record
}

func serviceJSON(t *testing.T, service *infernexv1alpha1.InferNexService) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(service)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

type identityTestClient struct {
	client.Client
	get    func(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error
	create func(context.Context, client.Object, ...client.CreateOption) error
	delete func(context.Context, client.Object, ...client.DeleteOption) error
}

func (c *identityTestClient) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	if c.get != nil {
		return c.get(ctx, key, object, options...)
	}
	return c.Client.Get(ctx, key, object, options...)
}

func newIdentityTestClient(t *testing.T, objects ...client.Object) *identityTestClient {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := infernexv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return &identityTestClient{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()}
}

func (c *identityTestClient) Create(ctx context.Context, object client.Object, options ...client.CreateOption) error {
	if c.create != nil {
		return c.create(ctx, object, options...)
	}
	return c.Client.Create(ctx, object, options...)
}

func (c *identityTestClient) Delete(ctx context.Context, object client.Object, options ...client.DeleteOption) error {
	if c.delete != nil {
		return c.delete(ctx, object, options...)
	}
	return c.Client.Delete(ctx, object, options...)
}
