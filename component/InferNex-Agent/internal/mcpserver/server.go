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

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/changesafety"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/deployer"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/diagnostics"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/experiment"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/observer"
)

const readOnlyInstructions = `Use these tools for InferNex-specific observation.
Observation calls are read-only and namespace-scoped. Prefer
infernex_inspect_service for control-plane status and infernex_get_topology
for the actual managed workloads and pods. Use infernex_get_events for recent
causal evidence. Do not infer a successful rollout from desired state alone.`

const deploymentInstructions = `
Conversational deployment is explicitly enabled. First call
infernex_list_deployment_sources, then select its opaque sourceId. The Agent
reuses only an existing Ready service or an administrator-created
InferNexServiceConfig in its fixed workspace namespace; it never accepts
arbitrary images, commands, model URLs, namespaces, or Kubernetes objects.
Deployment and deletion both require confirm=true. Inspect the resulting
service and topology before reporting a successful rollout. Use
infernex_get_change with the returned changeId to observe commit or rollback.`

const diagnosticInstructions = `
Bounded service diagnostics are enabled. infernex_diagnose_service reads only
Pod logs selected by the InferNex service owner label, redacts common credential
forms, and returns classified evidence plus a cross-node/component timeline.`

const experimentInstructions = `
Progressive experiments are explicitly enabled. A plan retains the stable
baseline, prepends exactly one administrator-approved sparse feature profile per
stage, and creates a distinct candidate. It never edits the baseline, switches
traffic, accepts raw YAML, or deletes resources without matching experiment and
change ownership. A diagnostic regression, Degraded condition, readiness loss,
or timeout rolls back only the current candidate.`

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
	Name     string `json:"name" jsonschema:"DNS-compatible name for the new InferNexService instance"`
	SourceID string `json:"sourceId" jsonschema:"Opaque sourceId returned by infernex_list_deployment_sources"`
	Confirm  bool   `json:"confirm" jsonschema:"Must be true after reviewing the discovered source and target name"`
}

type deletionInput struct {
	Name    string `json:"name" jsonschema:"Name of an Agent-owned InferNexService in the fixed workspace namespace"`
	Confirm bool   `json:"confirm" jsonschema:"Must be true after reviewing the target name"`
}

type testCatalogInput struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	CatalogID string `json:"catalogId"`
	Confirm   bool   `json:"confirm"`
}

type changeInput struct {
	ChangeID string `json:"changeId" jsonschema:"Opaque changeId returned by a deployment or deletion tool"`
}

type diagnosticInput struct {
	Namespace    string `json:"namespace" jsonschema:"Kubernetes namespace containing the InferNexService"`
	Name         string `json:"name" jsonschema:"InferNexService resource name"`
	SinceMinutes int    `json:"sinceMinutes,omitempty" jsonschema:"Bounded lookback window in minutes; defaults to 15 and must not exceed 1440"`
	MaxPods      int    `json:"maxPods,omitempty" jsonschema:"Maximum related Pods; defaults to 50 and must not exceed 100"`
	TailLines    int64  `json:"tailLines,omitempty" jsonschema:"Maximum lines per container and current/previous log stream; defaults to 200 and must not exceed 1000"`
}

type experimentInput struct {
	Namespace       string   `json:"namespace" jsonschema:"Namespace containing the stable baseline and experiment candidates"`
	BaselineName    string   `json:"baselineName" jsonschema:"Ready InferNexService whose runtime fields come from baseRefs"`
	CandidatePrefix string   `json:"candidatePrefix" jsonschema:"DNS-compatible prefix used for distinct stage candidate names"`
	FeatureProfiles []string `json:"featureProfiles" jsonschema:"Ordered administrator-approved sparse InferNexServiceConfig names; one is introduced per stage"`
	Confirm         bool     `json:"confirm" jsonschema:"Must be true after reviewing baseline, capacity, prefix, and ordered feature profiles"`
}

type experimentIDInput struct {
	ExperimentID string `json:"experimentId" jsonschema:"Opaque experimentId returned by infernex_start_experiment"`
}

type emptyInput struct{}

type allServicesOutput struct {
	Namespaces []observer.ServiceList `json:"namespaces"`
}

type experimentListOutput struct {
	Experiments []experiment.Plan `json:"experiments"`
}

type serverOptions struct {
	deployer    deployer.Deployer
	diagnoser   diagnostics.Diagnoser
	experiments experiment.Manager
	namespaces  []string
	testCatalog bool
}

func WithNamespaces(namespaces []string) Option {
	return func(options *serverOptions) {
		options.namespaces = append([]string(nil), namespaces...)
	}
}

func WithTestCatalog() Option {
	return func(options *serverOptions) {
		options.testCatalog = true
	}
}

func WithDiagnoser(diagnoser diagnostics.Diagnoser) Option {
	return func(options *serverOptions) {
		options.diagnoser = diagnoser
	}
}

func WithExperiments(manager experiment.Manager) Option {
	return func(options *serverOptions) {
		options.experiments = manager
	}
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
	if options.diagnoser != nil {
		serverInstructions += diagnosticInstructions
	}
	if options.experiments != nil {
		serverInstructions += experimentInstructions
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
		Name:        "infernex_list_all_services",
		Description: "List normalized InferNexService readiness summaries across all namespaces automatically discovered during installation.",
		Annotations: readOnly("List all discovered InferNex services"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, allServicesOutput, error) {
		output := allServicesOutput{Namespaces: make([]observer.ServiceList, 0, len(options.namespaces))}
		for _, namespace := range options.namespaces {
			services, err := domainObserver.ListServices(ctx, namespace)
			if err != nil {
				return nil, allServicesOutput{}, err
			}
			output.Namespaces = append(output.Namespaces, services)
		}
		return nil, output, nil
	})

	if options.diagnoser != nil {
		mcp.AddTool(server, &mcp.Tool{
			Name:        "infernex_diagnose_service",
			Description: "Correlate bounded, redacted Pod log evidence and Kubernetes Events across the nodes and components managed for one InferNexService.",
			Annotations: readOnly("Diagnose InferNex service"),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, input diagnosticInput) (*mcp.CallToolResult, diagnostics.Report, error) {
			output, err := options.diagnoser.Diagnose(ctx, diagnostics.Request{
				Namespace:    input.Namespace,
				Name:         input.Name,
				SinceMinutes: input.SinceMinutes,
				MaxPods:      input.MaxPods,
				TailLines:    input.TailLines,
			})
			return nil, output, err
		})
	}

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

		if options.testCatalog {
			mcp.AddTool(server, &mcp.Tool{
				Name:        "infernex_deploy_model",
				Description: "CI-only Kind fixture: deploy the fixed CPU test catalog entry.",
				Annotations: mutating("Deploy Kind test model", false),
			}, func(ctx context.Context, _ *mcp.CallToolRequest, input testCatalogInput) (*mcp.CallToolResult, deployer.Result, error) {
				output, err := options.deployer.Deploy(ctx, deployer.Request{
					Namespace: input.Namespace, Name: input.Name,
					CatalogID: input.CatalogID, Confirm: input.Confirm,
				})
				return nil, output, err
			})
			mcp.AddTool(server, &mcp.Tool{
				Name:        "infernex_delete_model",
				Description: "CI-only Kind fixture: delete an Agent-owned CPU test model.",
				Annotations: mutating("Delete Kind test model", true),
			}, func(ctx context.Context, _ *mcp.CallToolRequest, input testCatalogInput) (*mcp.CallToolResult, deployer.Result, error) {
				output, err := options.deployer.Delete(ctx, deployer.Request{
					Namespace: input.Namespace, Name: input.Name,
					CatalogID: input.CatalogID, Confirm: input.Confirm,
				})
				return nil, output, err
			})
		} else {
			mcp.AddTool(server, &mcp.Tool{
				Name:        "infernex_list_deployment_sources",
				Description: "Discover existing Ready InferNex services and administrator-created engine profiles that may be reused for a guarded deployment. No user-supplied YAML or namespace is accepted.",
				Annotations: readOnly("List safe deployment sources"),
			}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, deployer.SourceList, error) {
				output, err := options.deployer.ListSources(ctx)
				return nil, output, err
			})

			mcp.AddTool(server, &mcp.Tool{
				Name:        "infernex_deploy_model",
				Description: "Create one Agent-owned InferNexService in the fixed workspace by reusing a source returned by infernex_list_deployment_sources. Arbitrary workload fields are not accepted.",
				Annotations: mutating("Deploy model from existing InferNex source", false),
			}, func(ctx context.Context, _ *mcp.CallToolRequest, input deploymentInput) (*mcp.CallToolResult, deployer.Result, error) {
				output, err := options.deployer.Deploy(ctx, deployer.Request{
					Name: input.Name, SourceID: input.SourceID, Confirm: input.Confirm,
				})
				return nil, output, err
			})

			mcp.AddTool(server, &mcp.Tool{
				Name:        "infernex_delete_model",
				Description: "Delete one Agent-owned InferNexService from the fixed workspace. Resources without matching Agent change ownership are refused.",
				Annotations: mutating("Delete Agent-owned model", true),
			}, func(ctx context.Context, _ *mcp.CallToolRequest, input deletionInput) (*mcp.CallToolResult, deployer.Result, error) {
				output, err := options.deployer.Delete(ctx, deployer.Request{
					Name: input.Name, Confirm: input.Confirm,
				})
				return nil, output, err
			})
		}

		mcp.AddTool(server, &mcp.Tool{
			Name:        "infernex_get_change",
			Description: "Read the latest durable state of a catalog deployment change, including automatic rollback outcome.",
			Annotations: readOnly("Get deployment change state"),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, input changeInput) (*mcp.CallToolResult, changesafety.ChangeStatus, error) {
			output, err := options.deployer.GetChange(ctx, input.ChangeID)
			return nil, output, err
		})
	}

	if options.experiments != nil {
		mutating := func(title string) *mcp.ToolAnnotations {
			destructive := false
			openWorld := true
			return &mcp.ToolAnnotations{
				Title:           title,
				ReadOnlyHint:    false,
				IdempotentHint:  false,
				DestructiveHint: &destructive,
				OpenWorldHint:   &openWorld,
			}
		}
		mcp.AddTool(server, &mcp.Tool{
			Name:        "infernex_start_experiment",
			Description: "Start a durable progressive experiment from one stable baseRef-driven service. Each stage adds one approved feature profile to a distinct candidate and rolls it back on regression.",
			Annotations: mutating("Start progressive InferNex experiment"),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, input experimentInput) (*mcp.CallToolResult, experiment.Plan, error) {
			output, err := options.experiments.Create(ctx, experiment.Request{
				Namespace:       input.Namespace,
				BaselineName:    input.BaselineName,
				CandidatePrefix: input.CandidatePrefix,
				FeatureProfiles: input.FeatureProfiles,
				Confirm:         input.Confirm,
			})
			return nil, output, err
		})

		mcp.AddTool(server, &mcp.Tool{
			Name:        "infernex_get_experiment",
			Description: "Read the latest durable state, stage comparison, and rollback outcome of one progressive experiment.",
			Annotations: readOnly("Get InferNex experiment"),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, input experimentIDInput) (*mcp.CallToolResult, experiment.Plan, error) {
			output, err := options.experiments.Get(ctx, input.ExperimentID)
			return nil, output, err
		})

		mcp.AddTool(server, &mcp.Tool{
			Name:        "infernex_list_experiments",
			Description: "List the latest durable state of recent progressive experiments.",
			Annotations: readOnly("List InferNex experiments"),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, experimentListOutput, error) {
			output, err := options.experiments.List(ctx)
			return nil, experimentListOutput{Experiments: output}, err
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
