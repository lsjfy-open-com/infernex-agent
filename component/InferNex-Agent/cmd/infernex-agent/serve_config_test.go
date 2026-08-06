/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMergeServerConfigArgsAllowsCommandLineOverride(t *testing.T) {
	config := filepath.Join(t.TempDir(), "agent.conf")
	if err := os.WriteFile(config, []byte("--listen-address=127.0.0.1:8080\n--scan-namespaces=models\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts, err := parseServerOptions([]string{
		"--config", config,
		"--listen-address=127.0.0.1:18080",
	})
	if err != nil {
		t.Fatalf("parseServerOptions() error = %v", err)
	}
	if opts.listen != "127.0.0.1:18080" {
		t.Fatalf("listen = %q, want command-line override", opts.listen)
	}
	if opts.scanNamespaces != "models" {
		t.Fatalf("scan namespaces = %q, want models", opts.scanNamespaces)
	}
}

func TestReadAgentArgumentFileRejectsPositionalContent(t *testing.T) {
	config := filepath.Join(t.TempDir(), "agent.conf")
	if err := os.WriteFile(config, []byte("--scan-namespaces=models\nrm -rf /\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAgentArgumentFile(config); err == nil {
		t.Fatal("readAgentArgumentFile() unexpectedly accepted positional content")
	}
}
