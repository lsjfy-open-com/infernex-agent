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

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/observer"
)

type stubObserver struct{}

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
