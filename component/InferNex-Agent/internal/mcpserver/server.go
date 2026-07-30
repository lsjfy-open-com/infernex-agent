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

package mcpserver

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/deployer"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/observer"
)

const readOnlyInstructions = `Use these tools for InferNex-specific observation.
Observation calls are read-only and namespace-scoped. Prefer
infernex_inspect_service for control-plane status and infernex_get_topology
for the actual managed workloads and pods. Use infernex_get_events for recent
causal evidence. Do not infer a successful rollout from desired state alone.`

const deploymentInstructions = `
Catalog deployment is explicitly enabled. It only accepts a fixed catalogId,
namespace, and name; it never accepts arbitrary images, commands, URLs, or
Kubernetes objects. Deployment and deletion both require confirm=true. Inspect
the resulting service and topology before reporting a successful rollout.`

type namespaceInput struct {
	Namespace string `json:"namespace" jsonschema:"Kubernetes namespace containing the InferNexService resources"`
}

type serviceInput struct {
	Namespace string `json:"namespace" jsonschema:"Kubernetes namespace containing the InferNexService"`
	Name      string `json:"name" jsonschema:"InferNexService resource name"`
}

type eventInput struct {
	Namespace    string `json:"namespace" jsonschema:"Kubernetes namespace containing the InferNexService"`
	Name         string `json:"name" jsonschema:"InferNexService resource name"`
	SinceMinutes int    `json:"sinceMinutes,omitempty" jsonschema:"Lookback window in minutes; defaults to 60 and must not exceed 1440"`
	Limit        int    `json:"limit,omitempty" jsonschema:"Maximum event records; defaults to 50 and must not exceed 200"`
}

type deploymentInput struct {
	Namespace string `json:"namespace" jsonschema:"Kubernetes namespace approved by the Agent RBAC scope"`
	Name      string `json:"name" jsonschema:"DNS-compatible name for the InferNexService instance"`
	CatalogID string `json:"catalogId" jsonschema:"Fixed deployment catalog identifier; currently smollm2-135m-q4"`
	Confirm   bool   `json:"confirm" jsonschema:"Must be true after reviewing namespace, name, and catalogId"`
}

type serverOptions struct {
	deployer deployer.Deployer
}

type Option func(*serverOptions)

func WithDeployer(domainDeployer deployer.Deployer) Option {
	return func(options *serverOptions) {
		options.deployer = domainDeployer
	}
}

func New(domainObserver observer.Observer, version string, optionFunctions ...Option) *mcp.Server {
	options := serverOptions{}
	for _, option := range optionFunctions {
		option(&options)
	}
	serverInstructions := readOnlyInstructions
	if options.deployer != nil {
		serverInstructions += deploymentInstructions
	}
	server := mcp.NewServer(
		&mcp.Implementation{Name: "infernex-agent", Version: version},
		&mcp.ServerOptions{Instructions: serverInstructions},
	)

	readOnly := func(title string) *mcp.ToolAnnotations {
		notDestructive := false
		closedWorld := false
		return &mcp.ToolAnnotations{
			Title:           title,
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: &notDestructive,
			OpenWorldHint:   &closedWorld,
		}
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "infernex_list_services",
		Description: "List normalized InferNexService readiness summaries in one namespace.",
		Annotations: readOnly("List InferNex services"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input namespaceInput) (*mcp.CallToolResult, observer.ServiceList, error) {
		output, err := domainObserver.ListServices(ctx, input.Namespace)
		return nil, output, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "infernex_inspect_service",
		Description: "Inspect one InferNexService using its existing status, model, source, base templates, components, and conditions.",
		Annotations: readOnly("Inspect InferNex service"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input serviceInput) (*mcp.CallToolResult, observer.ServiceDetail, error) {
		output, err := domainObserver.InspectService(ctx, input.Namespace, input.Name)
		return nil, output, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "infernex_get_topology",
		Description: "Get the actual Deployment, DaemonSet, LeaderWorkerSet, and Pod topology managed for one InferNexService.",
		Annotations: readOnly("Get InferNex service topology"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input serviceInput) (*mcp.CallToolResult, observer.Topology, error) {
		output, err := domainObserver.GetTopology(ctx, input.Namespace, input.Name)
		return nil, output, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "infernex_get_events",
		Description: "Get recent Kubernetes events only for one InferNexService and its InferNex-managed workloads and pods.",
		Annotations: readOnly("Get InferNex service events"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input eventInput) (*mcp.CallToolResult, observer.EventEvidence, error) {
		output, err := domainObserver.GetEvents(
			ctx,
			input.Namespace,
			input.Name,
			input.SinceMinutes,
			input.Limit,
		)
		return nil, output, err
	})

	if options.deployer != nil {
		mutating := func(title string, destructive bool) *mcp.ToolAnnotations {
			openWorld := true
			return &mcp.ToolAnnotations{
				Title:           title,
				ReadOnlyHint:    false,
				IdempotentHint:  true,
				DestructiveHint: &destructive,
				OpenWorldHint:   &openWorld,
			}
		}

		mcp.AddTool(server, &mcp.Tool{
			Name: "infernex_deploy_model",
			Description: "Create one Agent-owned InferNexService from the fixed CPU test-model catalog. " +
				"Arbitrary workload fields are not accepted.",
			Annotations: mutating("Deploy catalog model", false),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, input deploymentInput) (*mcp.CallToolResult, deployer.Result, error) {
			output, err := options.deployer.Deploy(ctx, deployer.Request{
				Namespace: input.Namespace,
				Name:      input.Name,
				CatalogID: input.CatalogID,
				Confirm:   input.Confirm,
			})
			return nil, output, err
		})

		mcp.AddTool(server, &mcp.Tool{
			Name: "infernex_delete_model",
			Description: "Delete one Agent-owned catalog InferNexService. " +
				"Resources not owned by this Agent catalog are refused.",
			Annotations: mutating("Delete catalog model", true),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, input deploymentInput) (*mcp.CallToolResult, deployer.Result, error) {
			output, err := options.deployer.Delete(ctx, deployer.Request{
				Namespace: input.Namespace,
				Name:      input.Name,
				CatalogID: input.CatalogID,
				Confirm:   input.Confirm,
			})
			return nil, output, err
		})
	}

	return server
}

func StreamableHTTPHandler(server *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
}
