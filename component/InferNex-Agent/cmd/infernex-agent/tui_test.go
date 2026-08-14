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
	})
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
	info, err := os.Stat(filepath.Join(dir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("models.json mode=%v", info.Mode().Perm())
	}
}
