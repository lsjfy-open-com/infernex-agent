/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCandidateApplyAndRollbackWithoutRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("atomic replacement semantics are exercised by Linux CI")
	}
	root := t.TempDir()
	target := filepath.Join(root, "bin", "infernex-agent")
	candidate := filepath.Join(root, "candidate")
	stateDir := filepath.Join(root, "state")
	writeExecutable(t, target, "previous")
	writeExecutable(t, candidate, "candidate")
	candidateSHA, err := fileSHA256(candidate)
	if err != nil {
		t.Fatal(err)
	}

	record, err := applyCandidate(context.Background(), candidateMetadata{
		Path:   candidate,
		SHA256: candidateSHA,
		Build:  versionInfo{Version: "test-candidate"},
	}, target, stateDir, candidateActivation{})
	if err != nil {
		t.Fatalf("applyCandidate() error = %v", err)
	}
	if record.Status != "staged" {
		t.Fatalf("status = %q, want staged", record.Status)
	}
	assertFileContents(t, target, "candidate")

	record, err = rollbackCandidate(context.Background(), target, stateDir, candidateActivation{})
	if err != nil {
		t.Fatalf("rollbackCandidate() error = %v", err)
	}
	if record.Status != "rolled_back" {
		t.Fatalf("status = %q, want rolled_back", record.Status)
	}
	assertFileContents(t, target, "previous")
}

func TestCandidateActivationFailureRestoresPreviousBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("atomic replacement semantics are exercised by Linux CI")
	}
	root := t.TempDir()
	target := filepath.Join(root, "bin", "infernex-agent")
	candidate := filepath.Join(root, "candidate")
	stateDir := filepath.Join(root, "state")
	writeExecutable(t, target, "previous")
	writeExecutable(t, candidate, "candidate")
	candidateSHA, err := fileSHA256(candidate)
	if err != nil {
		t.Fatal(err)
	}

	originalRestart := candidateRestartAndWait
	t.Cleanup(func() { candidateRestartAndWait = originalRestart })
	calls := 0
	candidateRestartAndWait = func(context.Context, candidateActivation) error {
		calls++
		if calls == 1 {
			return errors.New("candidate did not become healthy")
		}
		return nil
	}

	record, err := applyCandidate(context.Background(), candidateMetadata{
		Path:   candidate,
		SHA256: candidateSHA,
		Build:  versionInfo{Version: "broken-candidate"},
	}, target, stateDir, candidateActivation{restart: true})
	if err == nil {
		t.Fatal("applyCandidate() unexpectedly succeeded")
	}
	if record.Status != "rolled_back" {
		t.Fatalf("status = %q, want rolled_back", record.Status)
	}
	if calls != 2 {
		t.Fatalf("restart calls = %d, want candidate plus restored baseline", calls)
	}
	assertFileContents(t, target, "previous")
}

func TestPrepareCandidatePathsRejectsBackupInsideTarget(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	target := filepath.Join(state, "infernex-agent")
	if _, _, err := prepareCandidatePaths(target, state); err == nil {
		t.Fatal("prepareCandidatePaths() unexpectedly accepted target in state directory")
	}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}

func assertFileContents(t *testing.T, path, expected string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != expected {
		t.Fatalf("contents = %q, want %q", contents, expected)
	}
}
