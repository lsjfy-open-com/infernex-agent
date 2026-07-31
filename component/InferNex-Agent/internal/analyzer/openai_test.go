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

package analyzer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/observer"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/supervisor"
)

func TestOpenAIAnalyzesNormalizedEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Errorf("request path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer internal-token" {
			t.Errorf("authorization header was not set")
		}
		payload := chatRequest{}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if payload.Model != "ops-model" || len(payload.Messages) != 2 {
			t.Errorf("request payload = %#v", payload)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{
			"model":"ops-model-v2",
			"choices":[{"message":{"role":"assistant","content":"Likely decode startup failure."}}]
		}`))
	}))
	defer server.Close()

	client, err := NewOpenAI(OpenAIConfig{
		BaseURL: server.URL + "/v1",
		Model:   "ops-model",
		APIKey:  "internal-token",
	})
	if err != nil {
		t.Fatalf("NewOpenAI returned error: %v", err)
	}
	result, err := client.Analyze(context.Background(), supervisor.AnalysisRequest{
		Service: observer.ServiceSummary{Namespace: "models", Name: "qwen", Mode: "pd"},
		Issues: []supervisor.Issue{{
			Severity: supervisor.SeverityCritical,
			Code:     "POD_NOT_READY",
			Message:  "pod is not ready: CrashLoopBackOff",
		}},
	})
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if result.Model != "ops-model-v2" || !strings.Contains(result.Content, "decode") {
		t.Fatalf("result = %#v", result)
	}
}

func TestOpenAIRejectsCredentialBearingURL(t *testing.T) {
	_, err := NewOpenAI(OpenAIConfig{
		BaseURL: "https://user:password@example.invalid/v1",
		Model:   "ops-model",
	})
	if err == nil || !strings.Contains(err.Error(), "user information") {
		t.Fatalf("error = %v, want user information rejection", err)
	}
}
