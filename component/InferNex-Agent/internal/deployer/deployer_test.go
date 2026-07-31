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
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
		nil,
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
