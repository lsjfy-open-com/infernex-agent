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

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infernexv1alpha1 "gitcode.com/openFuyao/InferNex/api/v1alpha1"
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
