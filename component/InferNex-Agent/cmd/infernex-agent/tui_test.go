/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPreparePiStateUsesEnvironmentCredentialReference(t *testing.T) {
	dir := t.TempDir()
	err := preparePiState(dir, modelFileOptions{
		baseURL: "http://model.internal:8000/v1/", model: "ops-model",
		contextWindowTokens: 65536, maxOutputTokens: 8192,
	}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(filepath.Join(dir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	var models piModelsFile
	if err := json.Unmarshal(payload, &models); err != nil {
		t.Fatal(err)
	}
	provider := models.Providers["infernex"]
	if provider.BaseURL != "http://model.internal:8000/v1" || provider.APIKey != "$INFERNEX_PI_API_KEY" {
		t.Fatalf("provider=%#v", provider)
	}
	if len(provider.Models) != 1 || provider.Models[0].ContextWindow != 65536 || provider.Models[0].MaxTokens != 8192 {
		t.Fatalf("models=%#v", provider.Models)
	}
	compat := provider.Models[0].Compat
	if compat.SupportsStore || compat.SupportsStrictMode || compat.SupportsDeveloperRole || compat.SupportsReasoningEffort {
		t.Fatalf("unsafe optional OpenAI compatibility fields are enabled: %#v", compat)
	}
	if !compat.SupportsUsageStreaming || compat.MaxTokensField != "max_tokens" {
		t.Fatalf("vLLM streaming compatibility is incomplete: %#v", compat)
	}
	info, err := os.Stat(filepath.Join(dir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("models.json mode=%v", info.Mode().Perm())
	}
	artifactInfo, err := os.Stat(filepath.Join(dir, "artifacts"))
	if err != nil || !artifactInfo.IsDir() {
		t.Fatalf("artifact directory is unavailable: info=%v err=%v", artifactInfo, err)
	}
	workspaceInfo, err := os.Stat(filepath.Join(dir, "workspace"))
	if err != nil || !workspaceInfo.IsDir() {
		t.Fatalf("stable workspace directory is unavailable: info=%v err=%v", workspaceInfo, err)
	}
}

func TestPreparePiStateUsesPlaceholderForKeylessLocalEndpoint(t *testing.T) {
	dir := t.TempDir()
	if err := preparePiState(dir, modelFileOptions{
		baseURL: "http://model.internal:8000/v1", model: "ops-model",
	}, ""); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(filepath.Join(dir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	var models piModelsFile
	if err := json.Unmarshal(payload, &models); err != nil {
		t.Fatal(err)
	}
	if got := models.Providers["infernex"].APIKey; got != "infernex-local-no-auth" {
		t.Fatalf("keyless endpoint credential=%q", got)
	}
}

func TestPiOpenAIBaseURLMatchesAgentEndpointRules(t *testing.T) {
	tests := map[string]string{
		"http://model.internal:8000":                     "http://model.internal:8000/v1",
		"http://model.internal:8000/v1/":                 "http://model.internal:8000/v1",
		"http://model.internal:8000/v1/chat/completions": "http://model.internal:8000/v1",
	}
	for input, want := range tests {
		if got := piOpenAIBaseURL(input); got != want {
			t.Errorf("piOpenAIBaseURL(%q)=%q want %q", input, got, want)
		}
	}
}
