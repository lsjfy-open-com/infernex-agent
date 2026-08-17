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
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/changesafety"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/deployer"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/diagnostics"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/experiment"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/kubeops"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/localfiles"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/observer"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/semanticmemory"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/skills"
)

type stubObserver struct{}

type stubDeployer struct{}

type stubDiagnoser struct{}

type stubExperiments struct{}

type stubKubernetes struct{}

func (stubKubernetes) DetectEnvironment(context.Context) (kubeops.Environment, error) {
	return kubeops.Environment{
		Platform: "openfuyao", ClusterRoles: []string{"inference-business-cluster"},
		Namespaces: []string{}, Capabilities: map[string]bool{}, Evidence: []string{},
		Recommendations: []string{}, Warnings: []string{},
	}, nil
}

func (stubKubernetes) ClusterOverview(context.Context) (kubeops.ClusterOverview, error) {
	return kubeops.ClusterOverview{KubernetesVersion: "v1.33.1"}, nil
}

func (stubKubernetes) ListWorkloads(_ context.Context, request kubeops.WorkloadRequest) (kubeops.WorkloadInventory, error) {
	return kubeops.WorkloadInventory{Namespace: request.Namespace, Workloads: []kubeops.WorkloadSummary{}, Pods: []kubeops.PodSummary{}, Services: []kubeops.ServiceSummary{}}, nil
}

func (stubKubernetes) GetEvents(_ context.Context, request kubeops.EventRequest) (kubeops.EventList, error) {
	return kubeops.EventList{Namespace: request.Namespace, SinceMinutes: 60, Events: []kubeops.EventSummary{}}, nil
}

func (stubKubernetes) GetPodLogs(_ context.Context, request kubeops.PodLogRequest) (kubeops.PodLogResult, error) {
	return kubeops.PodLogResult{Namespace: request.Namespace, Pod: request.Pod, Streams: []kubeops.LogStream{}}, nil
}

func (stubKubernetes) ListHelmReleases(_ context.Context, request kubeops.HelmReleaseRequest) (kubeops.HelmReleaseList, error) {
	return kubeops.HelmReleaseList{Namespace: request.Namespace, Releases: []kubeops.HelmReleaseSummary{}}, nil
}

func (stubKubernetes) DiscoverResources(_ context.Context, request kubeops.ResourceDiscoveryRequest) (kubeops.ResourceDiscovery, error) {
	return kubeops.ResourceDiscovery{GroupVersions: []kubeops.APIGroupResources{{GroupVersion: request.GroupVersion}}}, nil
}

func (stubKubernetes) ReadResources(_ context.Context, request kubeops.ResourceReadRequest) (kubeops.ResourceReadResult, error) {
	return kubeops.ResourceReadResult{GroupVersion: request.GroupVersion, Resource: request.Resource, Objects: []map[string]any{}}, nil
}

func (stubDeployer) ListSources(context.Context) (deployer.SourceList, error) {
	return deployer.SourceList{
		TargetNamespace: "infernex-agent-workspace",
		Sources: []deployer.Source{{
			SourceID: "service:models:stable", Kind: "stable-service",
			Namespace: "models", Name: "stable", TargetNamespace: "infernex-agent-workspace",
		}},
	}, nil
}

func (stubDiagnoser) Diagnose(_ context.Context, request diagnostics.Request) (diagnostics.Report, error) {
	return diagnostics.Report{
		Service: diagnostics.ServiceReference{Namespace: request.Namespace, Name: request.Name},
		Incidents: []diagnostics.Incident{{
			ID: "incident-1", RootCategory: "npu-device-failure", Severity: diagnostics.SeverityCritical,
		}},
	}, nil
}

func (stubExperiments) Create(_ context.Context, request experiment.Request) (experiment.Plan, error) {
	return experiment.Plan{
		ID: "experiment-1", Namespace: request.Namespace, BaselineName: request.BaselineName,
		CandidatePrefix: request.CandidatePrefix, FeatureProfiles: request.FeatureProfiles,
		Status: experiment.PlanStatusPlanned,
	}, nil
}

func (stubExperiments) Get(_ context.Context, id string) (experiment.Plan, error) {
	return experiment.Plan{ID: id, Status: experiment.PlanStatusRunning}, nil
}

func (stubExperiments) List(context.Context) ([]experiment.Plan, error) {
	return []experiment.Plan{{ID: "experiment-1", Status: experiment.PlanStatusCompleted}}, nil
}

func (stubDeployer) Deploy(_ context.Context, request deployer.Request) (deployer.Result, error) {
	return deployer.Result{
		Namespace:    request.Namespace,
		Name:         request.Name,
		SourceID:     request.SourceID,
		Operation:    "created",
		ResourceKind: "InferNexService",
	}, nil
}

func (stubDeployer) Delete(_ context.Context, request deployer.Request) (deployer.Result, error) {
	return deployer.Result{
		Namespace:    request.Namespace,
		Name:         request.Name,
		SourceID:     request.SourceID,
		Operation:    "deleted",
		ResourceKind: "InferNexService",
	}, nil
}

func (stubDeployer) GetChange(
	_ context.Context,
	changeID string,
) (changesafety.ChangeStatus, error) {
	return changesafety.ChangeStatus{
		APIVersion: "agent.infernex.io/v1alpha1",
		Kind:       "InferNexChange",
		ID:         changeID,
		Status:     changesafety.StatusCommitted,
		OccurredAt: time.Now().UTC(),
	}, nil
}

func (stubObserver) ListServices(_ context.Context, namespace string) (observer.ServiceList, error) {
	return observer.ServiceList{
		Namespace:     namespace,
		TotalServices: 1,
		Services: []observer.ServiceSummary{{
			Namespace: namespace,
			Name:      "llama",
			Mode:      "pd",
			Ready:     true,
		}},
	}, nil
}

func (stubObserver) InspectService(
	_ context.Context,
	namespace string,
	name string,
) (observer.ServiceDetail, error) {
	return observer.ServiceDetail{Service: observer.ServiceSummary{
		Namespace: namespace,
		Name:      name,
		Ready:     true,
	}}, nil
}

func (stubObserver) GetTopology(
	_ context.Context,
	namespace string,
	name string,
) (observer.Topology, error) {
	return observer.Topology{Service: observer.ServiceSummary{
		Namespace: namespace,
		Name:      name,
		Ready:     true,
	}}, nil
}

func (stubObserver) GetEvents(
	_ context.Context,
	namespace string,
	name string,
	sinceMinutes int,
	_ int,
) (observer.EventEvidence, error) {
	if sinceMinutes == 0 {
		sinceMinutes = 60
	}
	return observer.EventEvidence{
		Service:      observer.ServiceReference{Namespace: namespace, Name: name},
		SinceMinutes: sinceMinutes,
		Events:       []observer.EventSummary{},
	}, nil
}

func TestServerPublishesOnlyReadOnlyDomainTools(t *testing.T) {
	ctx := context.Background()
	server := New(stubObserver{}, "test")
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	defer serverSession.Close()

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	clientSession, err := mcpClient.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	defer clientSession.Close()

	list, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(list.Tools) != 5 {
		t.Fatalf("tool count = %d, want 5", len(list.Tools))
	}
	for _, tool := range list.Tools {
		if tool.Annotations == nil ||
			!tool.Annotations.ReadOnlyHint ||
			!tool.Annotations.IdempotentHint ||
			tool.Annotations.OpenWorldHint == nil ||
			*tool.Annotations.OpenWorldHint {
			t.Fatalf("unsafe annotations for %q: %#v", tool.Name, tool.Annotations)
		}
	}

	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "infernex_list_services",
		Arguments: map[string]any{"namespace": "models"},
	})
	if err != nil {
		t.Fatalf("call list tool: %v", err)
	}
	if result.IsError {
		t.Fatalf("list tool returned MCP error: %#v", result.Content)
	}
	payload, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured result: %v", err)
	}
	var serviceList observer.ServiceList
	if err := json.Unmarshal(payload, &serviceList); err != nil {
		t.Fatalf("unmarshal structured result: %v", err)
	}
	if serviceList.Namespace != "models" ||
		serviceList.TotalServices != 1 ||
		len(serviceList.Services) != 1 ||
		serviceList.Services[0].Name != "llama" {
		t.Fatalf("structured result = %#v", serviceList)
	}

	eventResult, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "infernex_get_events",
		Arguments: map[string]any{
			"namespace": "models",
			"name":      "llama",
		},
	})
	if err != nil {
		t.Fatalf("call events tool: %v", err)
	}
	if eventResult.IsError {
		t.Fatalf("events tool returned MCP error: %#v", eventResult.Content)
	}
	eventPayload, err := json.Marshal(eventResult.StructuredContent)
	if err != nil {
		t.Fatalf("marshal event result: %v", err)
	}
	var eventEvidence observer.EventEvidence
	if err := json.Unmarshal(eventPayload, &eventEvidence); err != nil {
		t.Fatalf("unmarshal event result: %v", err)
	}
	if eventEvidence.Service.Name != "llama" ||
		eventEvidence.SinceMinutes != 60 ||
		eventEvidence.Events == nil {
		t.Fatalf("structured event result = %#v", eventEvidence)
	}
}

func TestServerPublishesGeneralKubernetesAndHelmToolsWhenEnabled(t *testing.T) {
	ctx := context.Background()
	server := New(stubObserver{}, "test", WithKubernetes(stubKubernetes{}), WithInferNexBridge(false))
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	defer serverSession.Close()

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	clientSession, err := mcpClient.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	defer clientSession.Close()

	list, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(list.Tools) != 8 {
		t.Fatalf("tool count = %d, want 8", len(list.Tools))
	}
	want := map[string]bool{
		"openfuyao_detect_environment": false,
		"k8s_cluster_overview":         false,
		"k8s_list_workloads":           false,
		"k8s_discover_api_resources":   false,
		"k8s_read_resources":           false,
		"k8s_get_events":               false,
		"k8s_get_pod_logs":             false,
		"helm_list_releases":           false,
	}
	for _, tool := range list.Tools {
		if _, ok := want[tool.Name]; !ok {
			t.Fatalf("unexpected tool in Bridge-less mode: %q", tool.Name)
		}
		want[tool.Name] = true
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint {
			t.Fatalf("general tool %q is not safely annotated: %#v", tool.Name, tool.Annotations)
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("general tool %q missing", name)
		}
	}

	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "openfuyao_detect_environment", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("detect environment call failed: err=%v result=%#v", err, result)
	}
	payload, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal environment: %v", err)
	}
	var environment kubeops.Environment
	if err := json.Unmarshal(payload, &environment); err != nil {
		t.Fatalf("unmarshal environment: %v", err)
	}
	if environment.Platform != "openfuyao" {
		t.Fatalf("environment = %#v", environment)
	}
}

func TestServerPublishesConstrainedDeploymentToolsOnlyWhenEnabled(t *testing.T) {
	ctx := context.Background()
	server := New(stubObserver{}, "test", WithDeployer(stubDeployer{}))
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	defer serverSession.Close()

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	clientSession, err := mcpClient.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	defer clientSession.Close()

	list, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(list.Tools) != 9 {
		t.Fatalf("tool count = %d, want 9", len(list.Tools))
	}
	tools := make(map[string]*mcp.Tool, len(list.Tools))
	for _, tool := range list.Tools {
		tools[tool.Name] = tool
	}
	deployTool := tools["infernex_deploy_model"]
	deleteTool := tools["infernex_delete_model"]
	changeTool := tools["infernex_get_change"]
	sourcesTool := tools["infernex_list_deployment_sources"]
	if deployTool == nil || deleteTool == nil || changeTool == nil || sourcesTool == nil {
		t.Fatalf("deployment tools missing: %#v", tools)
	}
	if deployTool.Annotations == nil ||
		deployTool.Annotations.ReadOnlyHint ||
		!deployTool.Annotations.IdempotentHint ||
		deployTool.Annotations.DestructiveHint == nil ||
		*deployTool.Annotations.DestructiveHint {
		t.Fatalf("unsafe deploy annotations: %#v", deployTool.Annotations)
	}
	if deleteTool.Annotations == nil ||
		deleteTool.Annotations.DestructiveHint == nil ||
		!*deleteTool.Annotations.DestructiveHint {
		t.Fatalf("unsafe delete annotations: %#v", deleteTool.Annotations)
	}
	if changeTool.Annotations == nil || !changeTool.Annotations.ReadOnlyHint {
		t.Fatalf("unsafe change annotations: %#v", changeTool.Annotations)
	}

	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "infernex_deploy_model",
		Arguments: map[string]any{
			"name":     "qwen-copy",
			"sourceId": "service:models:stable",
			"confirm":  true,
		},
	})
	if err != nil {
		t.Fatalf("call deploy tool: %v", err)
	}
	if result.IsError {
		t.Fatalf("deploy tool returned MCP error: %#v", result.Content)
	}
	payload, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal deploy result: %v", err)
	}
	var deployment deployer.Result
	if err := json.Unmarshal(payload, &deployment); err != nil {
		t.Fatalf("unmarshal deploy result: %v", err)
	}
	if deployment.Operation != "created" || deployment.Name != "qwen-copy" {
		t.Fatalf("deploy result = %#v", deployment)
	}
}

func TestServerPublishesDiagnosticsAndExperimentToolsOnlyWhenEnabled(t *testing.T) {
	ctx := context.Background()
	server := New(
		stubObserver{},
		"test",
		WithDiagnoser(stubDiagnoser{}),
		WithExperiments(stubExperiments{}),
	)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	defer serverSession.Close()

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	clientSession, err := mcpClient.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	defer clientSession.Close()

	list, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(list.Tools) != 9 {
		t.Fatalf("tool count = %d, want 9", len(list.Tools))
	}
	tools := make(map[string]*mcp.Tool, len(list.Tools))
	for _, tool := range list.Tools {
		tools[tool.Name] = tool
	}
	diagnoseTool := tools["infernex_diagnose_service"]
	startTool := tools["infernex_start_experiment"]
	getTool := tools["infernex_get_experiment"]
	listTool := tools["infernex_list_experiments"]
	if diagnoseTool == nil || startTool == nil || getTool == nil || listTool == nil {
		t.Fatalf("optional tools missing: %#v", tools)
	}
	if diagnoseTool.Annotations == nil || !diagnoseTool.Annotations.ReadOnlyHint {
		t.Fatalf("diagnostic tool must be read-only: %#v", diagnoseTool.Annotations)
	}
	if startTool.Annotations == nil || startTool.Annotations.ReadOnlyHint || startTool.Annotations.IdempotentHint {
		t.Fatalf("start experiment annotations = %#v", startTool.Annotations)
	}
	if getTool.Annotations == nil || !getTool.Annotations.ReadOnlyHint ||
		listTool.Annotations == nil || !listTool.Annotations.ReadOnlyHint {
		t.Fatal("experiment query tools must be read-only")
	}

	diagnosticResult, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "infernex_diagnose_service",
		Arguments: map[string]any{
			"namespace": "models", "name": "qwen-pd", "sinceMinutes": 10,
		},
	})
	if err != nil || diagnosticResult.IsError {
		t.Fatalf("diagnose call failed: err=%v result=%#v", err, diagnosticResult)
	}
	payload, err := json.Marshal(diagnosticResult.StructuredContent)
	if err != nil {
		t.Fatalf("marshal diagnostics: %v", err)
	}
	var report diagnostics.Report
	if err := json.Unmarshal(payload, &report); err != nil {
		t.Fatalf("decode diagnostics: %v", err)
	}
	if report.Service.Name != "qwen-pd" || len(report.Incidents) != 1 {
		t.Fatalf("diagnostics = %#v", report)
	}

	experimentResult, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "infernex_start_experiment",
		Arguments: map[string]any{
			"namespace": "models", "baselineName": "stable", "candidatePrefix": "trial",
			"featureProfiles": []string{"enable-mooncake"}, "confirm": true,
		},
	})
	if err != nil || experimentResult.IsError {
		t.Fatalf("experiment call failed: err=%v result=%#v", err, experimentResult)
	}
	payload, err = json.Marshal(experimentResult.StructuredContent)
	if err != nil {
		t.Fatalf("marshal experiment: %v", err)
	}
	var plan experiment.Plan
	if err := json.Unmarshal(payload, &plan); err != nil {
		t.Fatalf("decode experiment: %v", err)
	}
	if plan.ID != "experiment-1" || len(plan.FeatureProfiles) != 1 {
		t.Fatalf("experiment = %#v", plan)
	}
}

func TestServerPublishesDurableSemanticMemoryWithWriteAnnotations(t *testing.T) {
	ctx := context.Background()
	store, err := semanticmemory.NewFileStore(t.TempDir(), "cluster-test")
	if err != nil {
		t.Fatal(err)
	}
	server := New(stubObserver{}, "test", WithInferNexBridge(false), WithSemanticMemory(store))
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	defer serverSession.Close()
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	clientSession, err := mcpClient.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	defer clientSession.Close()

	list, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]*mcp.Tool{}
	for _, tool := range list.Tools {
		tools[tool.Name] = tool
	}
	if tools["infernex_search_memory"] == nil || tools["infernex_remember"] == nil || tools["infernex_forget_memory"] == nil {
		t.Fatalf("semantic memory tools missing: %#v", tools)
	}
	if !tools["infernex_search_memory"].Annotations.ReadOnlyHint || tools["infernex_remember"].Annotations.ReadOnlyHint ||
		!*tools["infernex_forget_memory"].Annotations.DestructiveHint {
		t.Fatal("semantic memory tool annotations do not enforce read/write boundaries")
	}

	remembered, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "infernex_remember", Arguments: map[string]any{
			"scope": "cluster", "type": "decision", "subject": "变更窗口",
			"summary": "工作日白天只进行只读探测。", "source": "user-confirmed", "confirm": true,
		},
	})
	if err != nil || remembered.IsError {
		t.Fatalf("remember failed: err=%v result=%#v", err, remembered)
	}
	searched, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "infernex_search_memory", Arguments: map[string]any{"query": "变更窗口"},
	})
	if err != nil || searched.IsError {
		t.Fatalf("search failed: err=%v result=%#v", err, searched)
	}
	payload, _ := json.Marshal(searched.StructuredContent)
	var result semanticmemory.SearchResult
	if err := json.Unmarshal(payload, &result); err != nil || len(result.Records) != 1 {
		t.Fatalf("semantic memory search=%#v err=%v", result, err)
	}
}

func TestServerPublishesBoundedLocalEvidenceAndReportTools(t *testing.T) {
	ctx := context.Background()
	evidenceRoot := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(evidenceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evidenceRoot, "vllm.log"), []byte("GET /metrics 200\nERROR worker timeout\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := localfiles.New([]string{evidenceRoot}, filepath.Join(t.TempDir(), "reports"))
	if err != nil {
		t.Fatal(err)
	}
	server := New(stubObserver{}, "test", WithInferNexBridge(false), WithLocalFiles(workspace))
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	list, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]*mcp.Tool{}
	for _, tool := range list.Tools {
		tools[tool.Name] = tool
	}
	for _, name := range []string{"infernex_list_evidence_roots", "infernex_find_evidence_files", "infernex_grep_evidence_files", "infernex_read_evidence_file", "infernex_list_reports", "infernex_read_report", "infernex_create_markdown_report"} {
		if tools[name] == nil {
			t.Fatalf("missing local evidence tool %s", name)
		}
	}
	if !tools["infernex_grep_evidence_files"].Annotations.ReadOnlyHint || tools["infernex_create_markdown_report"].Annotations.ReadOnlyHint {
		t.Fatal("local evidence annotations do not enforce read/write boundary")
	}
	rootID := workspace.Roots()[0].ID
	grep, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "infernex_grep_evidence_files", Arguments: map[string]any{"rootId": rootID, "pattern": ".", "recursive": true}})
	if err != nil || grep.IsError {
		t.Fatalf("grep failed: err=%v result=%#v", err, grep)
	}
	payload, _ := json.Marshal(grep.StructuredContent)
	var grepResult localfiles.GrepResult
	if err := json.Unmarshal(payload, &grepResult); err != nil || len(grepResult.Matches) != 1 || grepResult.FilteredLines != 1 {
		t.Fatalf("grep result=%#v err=%v", grepResult, err)
	}
	report, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "infernex_create_markdown_report", Arguments: map[string]any{"title": "worker timeout", "markdown": "## Finding\n\nWorker timed out.", "sources": []map[string]any{{"rootId": rootID, "path": "vllm.log"}}, "confirm": true}})
	if err != nil || report.IsError {
		t.Fatalf("report failed: err=%v result=%#v", err, report)
	}
}

func TestServerPublishesProgressiveDiagnosticSkills(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	directory := filepath.Join(root, "hixl-diagnosis")
	if err := os.MkdirAll(filepath.Join(directory, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte("---\nname: hixl-diagnosis\ndescription: Diagnose HiXL timeouts\n---\nUse evidence first."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "references", "timeouts.md"), []byte("# Timeouts\nCheck both peers."), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := skills.NewRegistry([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	server := New(stubObserver{}, "test", WithInferNexBridge(false), WithSkills(registry))
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	list, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]*mcp.Tool{}
	for _, tool := range list.Tools {
		tools[tool.Name] = tool
	}
	for _, name := range []string{"infernex_list_skills", "infernex_read_skill", "infernex_read_skill_reference"} {
		if tools[name] == nil || !tools[name].Annotations.ReadOnlyHint {
			t.Fatalf("missing read-only Skill tool %s", name)
		}
	}
	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "infernex_read_skill_reference", Arguments: map[string]any{"name": "hixl-diagnosis", "reference": "timeouts.md"}})
	if err != nil || result.IsError {
		t.Fatalf("read reference failed: %v %#v", err, result)
	}
	payload, _ := json.Marshal(result.StructuredContent)
	var reference skills.ReferenceContent
	if err := json.Unmarshal(payload, &reference); err != nil || reference.Content == "" {
		t.Fatalf("decode reference: %#v %v", reference, err)
	}
}

func TestStreamableHTTPHandlerSupportsStatelessJSONToolCalls(t *testing.T) {
	server := New(stubObserver{}, "test")
	handler := StreamableHTTPHandler(server)
	request := httptest.NewRequest(
		http.MethodPost,
		"http://infernex-agent.example/mcp",
		bytes.NewBufferString(`{
			"jsonrpc": "2.0",
			"id": 1,
			"method": "tools/call",
			"params": {
				"name": "infernex_list_services",
				"arguments": {"namespace": "models"}
			}
		}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-06-18")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("HTTP status = %d, body = %s", response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", contentType)
	}
	var payload struct {
		Result struct {
			StructuredContent observer.ServiceList `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode HTTP MCP response: %v", err)
	}
	if payload.Result.StructuredContent.TotalServices != 1 ||
		payload.Result.StructuredContent.Services[0].Name != "llama" {
		t.Fatalf("HTTP MCP response = %#v", payload)
	}
}
