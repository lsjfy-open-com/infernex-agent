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
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infernexv1alpha1 "gitcode.com/openFuyao/InferNex/api/v1alpha1"
)

const (
	managedByLabel = "app.kubernetes.io/managed-by"
	catalogLabel   = "infernex.io/catalog-id"
	managedByAgent = "infernex-agent"
)

type KubernetesDeployer struct {
	client client.Client
}

func New(kubeClient client.Client) *KubernetesDeployer {
	return &KubernetesDeployer{client: kubeClient}
}

func (d *KubernetesDeployer) Deploy(ctx context.Context, request Request) (Result, error) {
	request, err := validateRequest(request, "deploy")
	if err != nil {
		return Result{}, err
	}

	desired := tinyModelService(request.Namespace, request.Name)
	current := &infernexv1alpha1.InferNexService{}
	key := types.NamespacedName{Namespace: request.Namespace, Name: request.Name}
	if err := d.client.Get(ctx, key, current); err != nil {
		if !apierrors.IsNotFound(err) {
			return Result{}, fmt.Errorf("check InferNexService %s: %w", key, err)
		}
		if err := d.client.Create(ctx, desired); err != nil {
			return Result{}, fmt.Errorf("create catalog InferNexService %s: %w", key, err)
		}
		return resultFor(request, "created"), nil
	}

	if err := verifyOwned(current, request.CatalogID); err != nil {
		return Result{}, err
	}
	if !equality.Semantic.DeepEqual(current.Spec, desired.Spec) {
		return Result{}, fmt.Errorf(
			"InferNexService %s is Agent-owned but its spec drifted from catalog %q; refusing to overwrite it",
			key,
			request.CatalogID,
		)
	}
	return resultFor(request, "already-exists"), nil
}

func (d *KubernetesDeployer) Delete(ctx context.Context, request Request) (Result, error) {
	request, err := validateRequest(request, "delete")
	if err != nil {
		return Result{}, err
	}

	current := &infernexv1alpha1.InferNexService{}
	key := types.NamespacedName{Namespace: request.Namespace, Name: request.Name}
	if err := d.client.Get(ctx, key, current); err != nil {
		if apierrors.IsNotFound(err) {
			return resultFor(request, "already-absent"), nil
		}
		return Result{}, fmt.Errorf("check InferNexService %s: %w", key, err)
	}
	if err := verifyOwned(current, request.CatalogID); err != nil {
		return Result{}, err
	}
	if err := d.client.Delete(ctx, current); err != nil && !apierrors.IsNotFound(err) {
		return Result{}, fmt.Errorf("delete catalog InferNexService %s: %w", key, err)
	}
	result := resultFor(request, "deleted")
	result.Endpoint = ""
	result.InferenceAPI = ""
	return result, nil
}

func validateRequest(request Request, action string) (Request, error) {
	request.Namespace = strings.TrimSpace(request.Namespace)
	request.Name = strings.TrimSpace(request.Name)
	request.CatalogID = strings.TrimSpace(request.CatalogID)

	if problems := validation.IsDNS1123Label(request.Namespace); len(problems) > 0 {
		return Request{}, fmt.Errorf(
			"invalid namespace %q: %s",
			request.Namespace,
			strings.Join(problems, "; "),
		)
	}
	if problems := validation.IsDNS1123Subdomain(request.Name); len(problems) > 0 {
		return Request{}, fmt.Errorf(
			"invalid InferNexService name %q: %s",
			request.Name,
			strings.Join(problems, "; "),
		)
	}
	if request.CatalogID != TinyModelCatalogID {
		return Request{}, fmt.Errorf(
			"unsupported catalogId %q; the only enabled test catalog entry is %q",
			request.CatalogID,
			TinyModelCatalogID,
		)
	}
	if !request.Confirm {
		return Request{}, fmt.Errorf(
			"%s requires confirm=true after reviewing namespace, name, and catalogId",
			action,
		)
	}
	return request, nil
}

func verifyOwned(service *infernexv1alpha1.InferNexService, catalogID string) error {
	key := types.NamespacedName{Namespace: service.Namespace, Name: service.Name}
	if service.Labels[managedByLabel] != managedByAgent ||
		service.Labels[catalogLabel] != catalogID {
		return fmt.Errorf(
			"InferNexService %s is not owned by InferNex Agent catalog %q; refusing to mutate it",
			key,
			catalogID,
		)
	}
	return nil
}

func resultFor(request Request, operation string) Result {
	return Result{
		Namespace:    request.Namespace,
		Name:         request.Name,
		CatalogID:    request.CatalogID,
		Operation:    operation,
		ResourceKind: "InferNexService",
		Endpoint: fmt.Sprintf(
			"http://%s-engine-aggregate.%s.svc:8080",
			request.Name,
			request.Namespace,
		),
		InferenceAPI: "/v1/chat/completions",
	}
}

func objectMeta(namespace string, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Namespace: namespace,
		Name:      name,
		Labels: map[string]string{
			managedByLabel: managedByAgent,
			catalogLabel:   TinyModelCatalogID,
		},
	}
}
