/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
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
		"--openai-timeout=2m\n" +
		"--context-window-tokens=65536\n" +
		"--max-output-tokens=4096\n" +
		"--context-compaction-threshold=75\n" +
		"--context-keep-recent-turns=6\n" +
		"--tool-result-max-tokens=2048\n"
	if err := os.WriteFile(config, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	opts, err := parseChatOptions([]string{"--config", config})
	if err != nil {
		t.Fatal(err)
	}
	if opts.baseURL != "http://model.internal:8000/v1" || opts.model != "ops-model" ||
		opts.apiKeyFile != "/run/model-key" || opts.timeout != 2*time.Minute ||
		opts.contextWindowTokens != 65536 || opts.maxOutputTokens != 4096 ||
		opts.contextThreshold != 75 || opts.keepRecentTurns != 6 ||
		opts.toolResultMaxTokens != 2048 {
		t.Fatalf("options=%#v", opts)
	}
}

func TestParseChatOptionsRejectsUnsafeContextBudget(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "agent.conf")
	payload := "--openai-base-url=http://model.internal:8000/v1\n" +
		"--openai-model=ops-model\n" +
		"--context-window-tokens=2048\n" +
		"--max-output-tokens=1800\n"
	if err := os.WriteFile(config, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseChatOptions([]string{"--config", config}); err == nil {
		t.Fatal("expected invalid context budget error")
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

func TestBufferedChatInputSupportsNonTTYAndPrintsPrompt(t *testing.T) {
	output := &bytes.Buffer{}
	input := &bufferedChatInput{
		reader: bufio.NewReader(strings.NewReader("inspect cluster\r\n")),
		output: output,
	}
	line, err := input.ReadLine("infernex> ", true)
	if err != nil {
		t.Fatal(err)
	}
	if line != "inspect cluster" || output.String() != "infernex> " {
		t.Fatalf("line=%q output=%q", line, output.String())
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
}

type scriptedChatInput struct {
	lines []string
	errs  []error
	index int
}

func (s *scriptedChatInput) ReadLine(string, bool) (string, error) {
	if s.index >= len(s.lines) {
		return "", io.EOF
	}
	line := s.lines[s.index]
	var err error
	if s.index < len(s.errs) {
		err = s.errs[s.index]
	}
	s.index++
	return line, err
}

func (s *scriptedChatInput) Close() error { return nil }

func TestInteractiveChatHelpDocumentsLineEditing(t *testing.T) {
	input := &scriptedChatInput{lines: []string{"/help", "/exit"}}
	output := &bytes.Buffer{}
	if err := interactiveChat(context.Background(), input, output, nil); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"/undo", "Backspace/Delete", "Up/Down history", "Ctrl+U"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("help does not contain %q: %s", expected, output.String())
		}
	}
}
