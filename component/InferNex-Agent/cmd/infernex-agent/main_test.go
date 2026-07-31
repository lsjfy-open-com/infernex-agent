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

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenAIAPIKeyFromEnvironment(t *testing.T) {
	t.Setenv("INFERNEX_OPENAI_API_KEY", "environment-key")

	key, err := openAIAPIKey("")
	if err != nil {
		t.Fatalf("openAIAPIKey() error = %v", err)
	}
	if key != "environment-key" {
		t.Fatalf("openAIAPIKey() = %q, want environment-key", key)
	}
}

func TestOpenAIAPIKeyFromFile(t *testing.T) {
	t.Setenv("INFERNEX_OPENAI_API_KEY", "environment-key")
	path := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(path, []byte("file-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	key, err := openAIAPIKey(path)
	if err != nil {
		t.Fatalf("openAIAPIKey() error = %v", err)
	}
	if key != "file-key" {
		t.Fatalf("openAIAPIKey() = %q, want file-key", key)
	}
}

func TestOpenAIAPIKeyRejectsMultipleLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(path, []byte("first\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := openAIAPIKey(path)
	if err == nil || !strings.Contains(err.Error(), "one text line") {
		t.Fatalf("openAIAPIKey() error = %v, want one-line validation", err)
	}
}

func TestOpenAIAPIKeyRejectsLargeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 64*1024+1)), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := openAIAPIKey(path)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("openAIAPIKey() error = %v, want size validation", err)
	}
}
