/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	defaultPiBinary         = "/opt/infernex-agent/pi-runtime/pi"
	defaultPiExtension      = "/opt/infernex-agent/pi/infernex.ts"
	defaultPiStateDir       = "/var/lib/infernex-agent/pi"
	defaultReasoningDisplay = "hidden"
)

type tuiOptions struct {
	configPath       string
	mcpURL           string
	piBinary         string
	extension        string
	stateDir         string
	checkOnly        bool
	reasoningDisplay string
	piArgs           []string
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
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Reasoning     bool           `json:"reasoning"`
	ContextWindow int            `json:"contextWindow"`
	MaxTokens     int            `json:"maxTokens"`
	Cost          map[string]int `json:"cost"`
	Compat        piModelCompat  `json:"compat"`
}

// piModelCompat deliberately uses conservative OpenAI Chat Completions
// parameters. vLLM and vLLM-Ascend expose an OpenAI-compatible endpoint, but
// optional OpenAI cloud fields are not uniformly implemented across versions.
// Tool calls and streaming remain enabled; only optional request fields and
// strict schema mode are disabled.
type piModelCompat struct {
	SupportsDeveloperRole   bool   `json:"supportsDeveloperRole"`
	SupportsReasoningEffort bool   `json:"supportsReasoningEffort"`
	SupportsStore           bool   `json:"supportsStore"`
	SupportsUsageStreaming  bool   `json:"supportsUsageInStreaming"`
	SupportsStrictMode      bool   `json:"supportsStrictMode"`
	MaxTokensField          string `json:"maxTokensField"`
}

func runTUI(args []string) error {
	opts, modelOpts, apiKey, err := parseTUIOptions(args)
	if err != nil {
		return err
	}
	if err := preparePiState(opts.stateDir, modelOpts, apiKey, opts.reasoningDisplay); err != nil {
		return err
	}
	workspaceDir := filepath.Join(opts.stateDir, "workspace")
	piEnv := append(os.Environ(),
		"PI_CODING_AGENT_DIR="+opts.stateDir,
		"INFERNEX_PI_API_KEY="+apiKey,
		"INFERNEX_MCP_URL="+opts.mcpURL,
		"INFERNEX_ARTIFACT_DIR="+filepath.Join(opts.stateDir, "artifacts"),
	)
	if err := checkPiModelConfiguration(opts, modelOpts, piEnv); err != nil {
		return err
	}
	if opts.checkOnly {
		fmt.Println("InferNex Pi model configuration: ready")
		return nil
	}

	piArgs := []string{
		"--provider", "infernex",
		"--model", modelOpts.model,
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
	command.Env = piEnv
	// Pi records its process working directory in every session. Do not inherit
	// the caller's package/extraction directory: it may disappear after an
	// upgrade and make an otherwise valid session impossible to resume.
	command.Dir = workspaceDir
	if err := command.Run(); err != nil {
		return fmt.Errorf("Pi TUI stopped: %w", err)
	}
	return nil
}

func checkPiModelConfiguration(opts tuiOptions, modelOpts modelFileOptions, environment []string) error {
	command := exec.Command(opts.piBinary, "auth", "check",
		"--provider", "infernex", "--model", modelOpts.model,
		"--json", "--no-refresh")
	command.Env = environment
	command.Dir = filepath.Join(opts.stateDir, "workspace")
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(output.String())
		if len(message) > 2048 {
			message = message[:2048] + "..."
		}
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("Pi could not use the model configured during InferNex setup; no Pi login is required: %s", message)
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
	flags.BoolVar(&opts.checkOnly, "check", false, "validate the migrated InferNex model configuration without opening the TUI")
	flags.StringVar(&opts.reasoningDisplay, "reasoning-display", "", "reasoning block display: hidden (default) or visible")
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
	if opts.reasoningDisplay == "" {
		opts.reasoningDisplay = modelOpts.reasoningDisplay
	}
	opts.reasoningDisplay, err = normalizeReasoningDisplay(opts.reasoningDisplay)
	if err != nil {
		return tuiOptions{}, modelFileOptions{}, "", err
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

func preparePiState(stateDir string, modelOpts modelFileOptions, apiKey, reasoningDisplay string) error {
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o700); err != nil {
		return fmt.Errorf("create Pi state directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, "artifacts"), 0o700); err != nil {
		return fmt.Errorf("create Pi artifact directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, "workspace"), 0o700); err != nil {
		return fmt.Errorf("create Pi workspace directory: %w", err)
	}
	reasoningDisplay, err := normalizeReasoningDisplay(reasoningDisplay)
	if err != nil {
		return err
	}
	contextWindow := modelOpts.contextWindowTokens
	if contextWindow <= 0 {
		contextWindow = 32768
	}
	maxTokens := modelOpts.maxOutputTokens
	if maxTokens <= 0 {
		maxTokens = 8192
	}
	piAPIKey := "$INFERNEX_PI_API_KEY"
	if strings.TrimSpace(apiKey) == "" {
		// Pi intentionally requires an auth value even for keyless local OpenAI
		// servers. A non-secret placeholder prevents an irrelevant /login flow.
		piAPIKey = "infernex-local-no-auth"
	}
	payload := piModelsFile{Providers: map[string]piProvider{
		"infernex": {
			BaseURL: piOpenAIBaseURL(modelOpts.baseURL),
			API:     "openai-completions",
			APIKey:  piAPIKey,
			Models: []piModel{{
				ID: modelOpts.model, Name: modelOpts.model, Reasoning: true,
				ContextWindow: contextWindow, MaxTokens: maxTokens,
				Cost: map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0},
				Compat: piModelCompat{
					SupportsDeveloperRole:   false,
					SupportsReasoningEffort: false,
					SupportsStore:           false,
					SupportsUsageStreaming:  true,
					SupportsStrictMode:      false,
					MaxTokensField:          "max_tokens",
				},
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
	return writePiDisplaySettings(stateDir, reasoningDisplay)
}

func normalizeReasoningDisplay(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return defaultReasoningDisplay, nil
	}
	if value != "hidden" && value != "visible" {
		return "", fmt.Errorf("reasoning display must be hidden or visible")
	}
	return value, nil
}

func writePiDisplaySettings(stateDir, reasoningDisplay string) error {
	path := filepath.Join(stateDir, "settings.json")
	settings := map[string]any{}
	payload, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(payload, &settings); err != nil {
			return fmt.Errorf("decode existing Pi settings: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read existing Pi settings: %w", err)
	}
	settings["hideThinkingBlock"] = reasoningDisplay == "hidden"
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Pi settings: %w", err)
	}
	encoded = append(encoded, '\n')
	temporary, err := os.CreateTemp(stateDir, ".settings.json.*")
	if err != nil {
		return fmt.Errorf("create temporary Pi settings: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write Pi settings: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close Pi settings: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("activate Pi settings: %w", err)
	}
	return nil
}

func piOpenAIBaseURL(value string) string {
	baseURL := strings.TrimRight(strings.TrimSpace(value), "/")
	if strings.HasSuffix(baseURL, "/chat/completions") {
		return strings.TrimSuffix(baseURL, "/chat/completions")
	}
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL
	}
	return baseURL + "/v1"
}
