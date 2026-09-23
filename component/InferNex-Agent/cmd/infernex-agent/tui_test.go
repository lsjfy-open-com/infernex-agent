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
	}, "secret", "hidden")
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
	settingsPayload, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(settingsPayload, &settings); err != nil {
		t.Fatal(err)
	}
	if hidden, ok := settings["hideThinkingBlock"].(bool); !ok || !hidden {
		t.Fatalf("hideThinkingBlock=%#v, want true", settings["hideThinkingBlock"])
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
	}, "", "visible"); err != nil {
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
	if got := models.Providers["infernex"].Models[0].MaxTokens; got != 8192 {
		t.Fatalf("default Pi maxTokens=%d, want 8192", got)
	}
	settingsPayload, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(settingsPayload, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["hideThinkingBlock"] != false {
		t.Fatalf("visible display settings=%#v", settings)
	}
}

func TestPreparePiStatePreservesUnrelatedSettings(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{\"theme\":\"light\",\"hideThinkingBlock\":false}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := preparePiState(dir, modelFileOptions{baseURL: "http://model.internal:8000/v1", model: "ops-model"}, "", "hidden"); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(payload, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["theme"] != "light" || settings["hideThinkingBlock"] != true {
		t.Fatalf("settings=%#v", settings)
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

func TestNormalizeReasoningDisplay(t *testing.T) {
	if got, err := normalizeReasoningDisplay(""); err != nil || got != "hidden" {
		t.Fatalf("default=%q err=%v", got, err)
	}
	if got, err := normalizeReasoningDisplay(" Visible "); err != nil || got != "visible" {
		t.Fatalf("visible=%q err=%v", got, err)
	}
	if _, err := normalizeReasoningDisplay("full-chain"); err == nil {
		t.Fatal("invalid reasoning display accepted")
	}
}

func TestResolveTUIWorkspaceDefaultsToCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	got, err := resolveTUIWorkspace("")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(dir)
	if got != want {
		t.Fatalf("workspace=%q want %q", got, want)
	}
}

func TestResolveTUIWorkspaceRejectsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveTUIWorkspace(path); err == nil {
		t.Fatal("file workspace accepted")
	}
}

func TestPrependToolPathUsesBundledDirectory(t *testing.T) {
	dir := t.TempDir()
	environment := []string{"HOME=/tmp", "PATH=/usr/local/bin:/usr/bin"}
	got := prependToolPath(environment, dir)
	want := "PATH=" + dir + string(os.PathListSeparator) + "/usr/local/bin:/usr/bin"
	if got[1] != want {
		t.Fatalf("PATH=%q want %q", got[1], want)
	}
}

func TestPrependToolPathIgnoresMissingDirectory(t *testing.T) {
	environment := []string{"PATH=/usr/bin"}
	got := prependToolPath(environment, filepath.Join(t.TempDir(), "missing"))
	if got[0] != environment[0] {
		t.Fatalf("PATH changed for missing tool directory: %q", got[0])
	}
}
