/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseChatOptionsLoadsModelConfiguration(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "agent.conf")
	payload := "--scan-namespaces=models\n" +
		"--openai-base-url=http://model.internal:8000/v1\n" +
		"--openai-model=ops-model\n" +
		"--openai-api-key-file=/run/model-key\n" +
		"--openai-timeout=2m\n"
	if err := os.WriteFile(config, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	opts, err := parseChatOptions([]string{"--config", config})
	if err != nil {
		t.Fatal(err)
	}
	if opts.baseURL != "http://model.internal:8000/v1" || opts.model != "ops-model" ||
		opts.apiKeyFile != "/run/model-key" || opts.timeout != 2*time.Minute {
		t.Fatalf("options=%#v", opts)
	}
}

func TestParseChatOptionsExplicitValuesOverrideConfiguration(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "agent.conf")
	if err := os.WriteFile(config, []byte("--openai-base-url=http://old\n--openai-model=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts, err := parseChatOptions([]string{
		"--config", config,
		"--base-url", "http://new",
		"--model", "new",
		"--timeout", "30s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.baseURL != "http://new" || opts.model != "new" || opts.timeout != 30*time.Second {
		t.Fatalf("options=%#v", opts)
	}
}

func TestParseChatOptionsRequiresConfiguredModel(t *testing.T) {
	if _, err := parseChatOptions([]string{"--config", filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("expected missing model error")
	}
}

func TestBoundedTerminalTextRemovesControlAndBidiCharacters(t *testing.T) {
	got := boundedTerminalText("safe\x1b[31m\u202etext", 100)
	if got != "safe[31mtext" {
		t.Fatalf("terminal text=%q", got)
	}
}
