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
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/changesafety"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/deployer"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/diagnostics"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/experiment"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/observer"
)

type stubObserver struct{}

type stubDeployer struct{}

type stubDiagnoser struct{}

type stubExperiments struct{}

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
		CatalogID:    request.CatalogID,
		Operation:    "created",
		ResourceKind: "InferNexService",
	}, nil
}

func (stubDeployer) Delete(_ context.Context, request deployer.Request) (deployer.Result, error) {
	return deployer.Result{
		Namespace:    request.Namespace,
		Name:         request.Name,
		CatalogID:    request.CatalogID,
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
	if len(list.Tools) != 4 {
		t.Fatalf("tool count = %d, want 4", len(list.Tools))
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
	if len(list.Tools) != 7 {
		t.Fatalf("tool count = %d, want 7", len(list.Tools))
	}
	tools := make(map[string]*mcp.Tool, len(list.Tools))
	for _, tool := range list.Tools {
		tools[tool.Name] = tool
	}
	deployTool := tools["infernex_deploy_model"]
	deleteTool := tools["infernex_delete_model"]
	changeTool := tools["infernex_get_change"]
	if deployTool == nil || deleteTool == nil || changeTool == nil {
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
			"namespace": "models",
			"name":      "tiny",
			"catalogId": deployer.TinyModelCatalogID,
			"confirm":   true,
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
	if deployment.Operation != "created" || deployment.Name != "tiny" {
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
	if len(list.Tools) != 8 {
		t.Fatalf("tool count = %d, want 8", len(list.Tools))
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
