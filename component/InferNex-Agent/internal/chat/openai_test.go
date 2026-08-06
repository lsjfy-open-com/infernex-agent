/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAICompleteSendsToolsAndParsesToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Errorf("path=%s", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization=%q", request.Header.Get("Authorization"))
		}
		var body openAIRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "ops-model" || len(body.Tools) != 1 || body.Tools[0].Function.Name != "scan" {
			t.Errorf("unexpected request: %#v", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"scan","arguments":"{\"namespace\":\"models\"}"}}]}}]}`))
	}))
	defer server.Close()

	model, err := NewOpenAI(OpenAIConfig{BaseURL: server.URL, Model: "ops-model", APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Complete(context.Background(), []Message{{Role: "user", Content: "scan"}}, []ToolDefinition{{
		Name: "scan", InputSchema: map[string]any{"type": "object"}, ReadOnly: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].Name != "scan" {
		t.Fatalf("response=%#v", response)
	}
}

func TestOpenAICompleteReturnsBoundedServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte(`{"error":{"message":"model unavailable","type":"upstream"}}`))
	}))
	defer server.Close()
	model, err := NewOpenAI(OpenAIConfig{BaseURL: server.URL + "/v1", Model: "ops-model"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Complete(context.Background(), []Message{{Role: "user", Content: "hello"}}, nil); err == nil {
		t.Fatal("expected endpoint error")
	}
}
