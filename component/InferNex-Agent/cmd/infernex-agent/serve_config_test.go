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

func TestParseServerOptionsValidatesExecutionModeAndSSHPair(t *testing.T) {
	if _, err := parseServerOptions([]string{"--execution-mode=god-mode"}); err == nil {
		t.Fatal("invalid execution mode was accepted")
	}
	if _, err := parseServerOptions([]string{"--execution-mode=diagnose", "--diagnostic-ssh-targets=node-01"}); err == nil {
		t.Fatal("SSH targets without operator config were accepted")
	}
	opts, err := parseServerOptions([]string{"--execution-mode=diagnose", "--diagnostic-ssh-config=/etc/infernex-agent/ssh.conf", "--diagnostic-ssh-targets=node-01"})
	if err != nil {
		t.Fatalf("valid diagnostic options: %v", err)
	}
	if opts.executionMode != "diagnose" || opts.sshTargets != "node-01" {
		t.Fatalf("unexpected options: %#v", opts)
	}
}

func TestParseServerOptionsValidatesDiagnosticDelegation(t *testing.T) {
	if _, err := parseServerOptions([]string{"--execution-mode=diagnose", "--diagnostic-subagent-listen-address=127.0.0.1:18082"}); err == nil {
		t.Fatal("diagnostic delegation without a token file was accepted")
	}
	if _, err := parseServerOptions([]string{"--execution-mode=detect", "--diagnostic-subagent-listen-address=127.0.0.1:18082", "--diagnostic-subagent-token-file=/tmp/token"}); err == nil {
		t.Fatal("diagnostic delegation in detect mode was accepted")
	}
	opts, err := parseServerOptions([]string{
		"--execution-mode=diagnose",
		"--diagnostic-subagent-listen-address=127.0.0.1:18082",
		"--diagnostic-subagent-token-file=/etc/infernex-agent/diagnostic-subagent-token",
		"--diagnostic-subagent-max-concurrency=3",
	})
	if err != nil {
		t.Fatalf("valid diagnostic delegation options: %v", err)
	}
	if opts.diagnosticDelegateConcurrent != 3 || opts.diagnosticDelegateListen != "127.0.0.1:18082" {
		t.Fatalf("unexpected diagnostic delegation options: %#v", opts)
	}
}
