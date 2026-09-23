/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunInstallDiagnoseUsesConfiguredModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "address already in use") {
			t.Fatalf("request does not contain evidence: %s", body)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"model":"test","choices":[{"message":{"role":"assistant","content":"Port 8081 is occupied."}}]}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	config := filepath.Join(dir, "agent.conf")
	evidence := filepath.Join(dir, "evidence.txt")
	if err := os.WriteFile(config, []byte("--openai-base-url="+server.URL+"/v1\n--openai-model=test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidence, []byte("listen tcp 127.0.0.1:8081: bind: address already in use\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runInstallDiagnose([]string{"--config", config, "--evidence", evidence, "--timeout", "5s"}); err != nil {
		t.Fatal(err)
	}
}

func TestReadInstallEvidenceRejectsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readInstallEvidence(path); err == nil {
		t.Fatal("expected empty evidence error")
	}
}
