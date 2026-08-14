/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	defaultPiBinary    = "/opt/infernex-agent/pi-runtime/pi"
	defaultPiExtension = "/opt/infernex-agent/pi/infernex.ts"
	defaultPiStateDir  = "/var/lib/infernex-agent/pi"
)

type tuiOptions struct {
	configPath string
	mcpURL     string
	piBinary   string
	extension  string
	stateDir   string
	piArgs     []string
}

type piModelsFile struct {
	Providers map[string]piProvider `json:"providers"`
}

type piProvider struct {
	BaseURL string    `json:"baseUrl"`
	API     string    `json:"api"`
	APIKey  string    `json:"apiKey"`
	Models  []piModel `json:"models"`
}

type piModel struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Reasoning     bool            `json:"reasoning"`
	ContextWindow int             `json:"contextWindow"`
	MaxTokens     int             `json:"maxTokens"`
	Cost          map[string]int  `json:"cost"`
	Compat        map[string]bool `json:"compat"`
}

func runTUI(args []string) error {
	opts, modelOpts, apiKey, err := parseTUIOptions(args)
	if err != nil {
		return err
	}
	if err := preparePiState(opts.stateDir, modelOpts); err != nil {
		return err
	}

	piArgs := []string{
		"--provider", "infernex",
		"--model", "infernex/" + modelOpts.model,
		"--no-builtin-tools",
		"--no-extensions",
		"--extension", opts.extension,
		"--no-skills",
		"--no-prompt-templates",
		"--no-context-files",
		"--no-approve",
		"--offline",
		"--session-dir", filepath.Join(opts.stateDir, "sessions"),
	}
	piArgs = append(piArgs, opts.piArgs...)
	command := exec.Command(opts.piBinary, piArgs...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Env = append(os.Environ(),
		"PI_CODING_AGENT_DIR="+opts.stateDir,
		"INFERNEX_PI_API_KEY="+apiKey,
		"INFERNEX_MCP_URL="+opts.mcpURL,
		"INFERNEX_ARTIFACT_DIR="+filepath.Join(opts.stateDir, "artifacts"),
	)
	if err := command.Run(); err != nil {
		return fmt.Errorf("Pi TUI stopped: %w", err)
	}
	return nil
}

func parseTUIOptions(args []string) (tuiOptions, modelFileOptions, string, error) {
	opts := tuiOptions{}
	flags := flag.NewFlagSet("infernex-agent tui", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&opts.configPath, "config", "/etc/infernex-agent/agent.conf", "host Agent configuration file")
	flags.StringVar(&opts.mcpURL, "mcp-url", "http://127.0.0.1:8080/mcp", "InferNex MCP endpoint")
	flags.StringVar(&opts.piBinary, "pi-binary", defaultPiBinary, "pinned Pi standalone binary")
	flags.StringVar(&opts.extension, "extension", defaultPiExtension, "InferNex Pi extension")
	flags.StringVar(&opts.stateDir, "state-dir", defaultPiStateDir, "Pi configuration and session directory")
	if err := flags.Parse(args); err != nil {
		return tuiOptions{}, modelFileOptions{}, "", err
	}
	opts.piArgs = flags.Args()
	modelOpts, err := readModelFileOptions(opts.configPath)
	if err != nil {
		return tuiOptions{}, modelFileOptions{}, "", err
	}
	if strings.TrimSpace(modelOpts.baseURL) == "" || strings.TrimSpace(modelOpts.model) == "" {
		return tuiOptions{}, modelFileOptions{}, "", fmt.Errorf("interactive model is not configured; run configure-model.sh first")
	}
	if _, err := os.Stat(opts.piBinary); err != nil {
		return tuiOptions{}, modelFileOptions{}, "", fmt.Errorf("Pi binary is unavailable at %s: %w", opts.piBinary, err)
	}
	if _, err := os.Stat(opts.extension); err != nil {
		return tuiOptions{}, modelFileOptions{}, "", fmt.Errorf("InferNex Pi extension is unavailable at %s: %w", opts.extension, err)
	}
	apiKey, err := readAPIKey(modelOpts.apiKeyFile)
	if err != nil {
		return tuiOptions{}, modelFileOptions{}, "", err
	}
	return opts, modelOpts, apiKey, nil
}

func preparePiState(stateDir string, modelOpts modelFileOptions) error {
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o700); err != nil {
		return fmt.Errorf("create Pi state directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, "artifacts"), 0o700); err != nil {
		return fmt.Errorf("create Pi artifact directory: %w", err)
	}
	contextWindow := modelOpts.contextWindowTokens
	if contextWindow <= 0 {
		contextWindow = 32768
	}
	maxTokens := modelOpts.maxOutputTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	payload := piModelsFile{Providers: map[string]piProvider{
		"infernex": {
			BaseURL: strings.TrimRight(modelOpts.baseURL, "/"),
			API:     "openai-completions",
			APIKey:  "$INFERNEX_PI_API_KEY",
			Models: []piModel{{
				ID: modelOpts.model, Name: modelOpts.model, Reasoning: true,
				ContextWindow: contextWindow, MaxTokens: maxTokens,
				Cost:   map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0},
				Compat: map[string]bool{"supportsDeveloperRole": false, "supportsReasoningEffort": false},
			}},
		},
	}}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Pi model configuration: %w", err)
	}
	encoded = append(encoded, '\n')
	temporary, err := os.CreateTemp(stateDir, ".models.json.*")
	if err != nil {
		return fmt.Errorf("create temporary Pi model configuration: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write Pi model configuration: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close Pi model configuration: %w", err)
	}
	if err := os.Rename(temporaryPath, filepath.Join(stateDir, "models.json")); err != nil {
		return fmt.Errorf("activate Pi model configuration: %w", err)
	}
	return nil
}
