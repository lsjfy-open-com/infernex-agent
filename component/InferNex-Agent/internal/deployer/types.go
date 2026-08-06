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
	"time"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/changesafety"
)

// TinyModelCatalogID remains as a non-production compatibility fixture for
// Kind tests. It is intentionally not exposed by the conversational MCP API.
const TinyModelCatalogID = "smollm2-135m-q4"

// Deployer is the narrow mutation boundary exposed to the MCP server. It
// deploys only from live, already-approved InferNex sources discovered by the
// Agent. It does not accept images, commands, model URLs, or arbitrary objects.
type Deployer interface {
	ListSources(context.Context) (SourceList, error)
	Deploy(context.Context, Request) (Result, error)
	Delete(context.Context, Request) (Result, error)
	GetChange(context.Context, string) (changesafety.ChangeStatus, error)
}

type Request struct {
	Namespace string
	Name      string
	SourceID  string
	CatalogID string // Deprecated: Kind-test compatibility only.
	Confirm   bool
}

type Result struct {
	Namespace    string `json:"namespace"`
	Name         string `json:"name"`
	SourceID     string `json:"sourceId,omitempty"`
	CatalogID    string `json:"catalogId,omitempty"` // Deprecated.
	Operation    string `json:"operation"`
	ResourceKind string `json:"resourceKind"`
	ChangeID     string `json:"changeId,omitempty"`
	ChangeStatus string `json:"changeStatus,omitempty"`
	Message      string `json:"message,omitempty"`
	Endpoint     string `json:"endpoint,omitempty"`     // Deprecated Kind fixture.
	InferenceAPI string `json:"inferenceApi,omitempty"` // Deprecated Kind fixture.
}

// Source is an existing, administrator-created InferNex definition that the
// Agent can safely reuse. Stable-service sources clone the exact desired spec
// of a Ready service. Profile sources create a service with one existing
// InferNexServiceConfig baseRef. SourceID is opaque to end users and intended
// to be selected by the conversational model after ListSources.
type Source struct {
	SourceID        string   `json:"sourceId"`
	Kind            string   `json:"kind"`
	Namespace       string   `json:"namespace"`
	Name            string   `json:"name"`
	ModelName       string   `json:"modelName,omitempty"`
	ModelURI        string   `json:"modelUri,omitempty"`
	Mode            string   `json:"mode,omitempty"`
	BaseRefs        []string `json:"baseRefs,omitempty"`
	TargetNamespace string   `json:"targetNamespace"`
}

type SourceList struct {
	TargetNamespace string   `json:"targetNamespace"`
	Sources         []Source `json:"sources"`
	Message         string   `json:"message"`
}

type Option func(*KubernetesDeployer)

func WithStore(store changesafety.Store) Option {
	return func(deployer *KubernetesDeployer) {
		if store != nil {
			deployer.store = store
		}
	}
}

func WithReadiness(timeout time.Duration, pollInterval time.Duration) Option {
	return func(deployer *KubernetesDeployer) {
		deployer.readinessTimeout = timeout
		deployer.pollInterval = pollInterval
	}
}

func WithDeploymentScope(targetNamespace, templateNamespace string, sourceNamespaces []string) Option {
	return func(deployer *KubernetesDeployer) {
		deployer.targetNamespace = targetNamespace
		deployer.templateNamespace = templateNamespace
		deployer.sourceNamespaces = append([]string(nil), sourceNamespaces...)
	}
}
