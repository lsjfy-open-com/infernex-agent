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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	infernexchat "gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/chat"
)

const maxAPIKeyBytes = 64 * 1024

type chatOptions struct {
	configPath          string
	mcpURL              string
	baseURL             string
	model               string
	apiKeyFile          string
	timeout             time.Duration
	ask                 string
	maxToolRounds       int
	contextWindowTokens int
	maxOutputTokens     int
	contextThreshold    int
	keepRecentTurns     int
	toolResultMaxTokens int
	verbose             bool
}

type modelFileOptions struct {
	baseURL             string
	model               string
	apiKeyFile          string
	timeout             time.Duration
	contextWindowTokens int
	maxOutputTokens     int
	contextThreshold    int
	keepRecentTurns     int
	toolResultMaxTokens int
}

func runChat(args []string) error {
	opts, err := parseChatOptions(args)
	if err != nil {
		return err
	}
	apiKey, err := readAPIKey(opts.apiKeyFile)
	if err != nil {
		return err
	}
	model, err := infernexchat.NewOpenAI(infernexchat.OpenAIConfig{
		BaseURL:         opts.baseURL,
		Model:           opts.model,
		APIKey:          apiKey,
		Timeout:         opts.timeout,
		MaxOutputTokens: opts.maxOutputTokens,
	})
	if err != nil {
		return fmt.Errorf("configure interactive model: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	tools, err := infernexchat.NewMCPClient(ctx, opts.mcpURL, version)
	if err != nil {
		return err
	}
	reader := bufio.NewReader(os.Stdin)
	approver := interactiveApprover(reader, os.Stdout)
	if opts.ask != "" {
		// One-shot mode is suitable for scripts, so it never grants a write action.
		approver = nil
	}
	conversation, err := infernexchat.NewConversation(ctx, infernexchat.Config{
		Model:         model,
		Tools:         tools,
		Approver:      approver,
		Progress:      terminalProgress(os.Stderr, opts.verbose),
		MaxToolRounds: opts.maxToolRounds,
		Context: infernexchat.ContextConfig{
			WindowTokens:               opts.contextWindowTokens,
			MaxOutputTokens:            opts.maxOutputTokens,
			CompactionThresholdPercent: opts.contextThreshold,
			KeepRecentTurns:            opts.keepRecentTurns,
			ToolResultMaxTokens:        opts.toolResultMaxTokens,
		},
	})
	if err != nil {
		_ = tools.Close()
		return err
	}
	defer conversation.Close()

	if opts.ask != "" {
		answer, askErr := conversation.Ask(ctx, opts.ask)
		if askErr != nil {
			return askErr
		}
		fmt.Fprintln(os.Stdout, answer)
		return nil
	}
	return interactiveChat(ctx, reader, os.Stdout, conversation)
}

func parseChatOptions(args []string) (chatOptions, error) {
	opts := chatOptions{}
	flags := flag.NewFlagSet("infernex-agent chat", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&opts.configPath, "config", "/etc/infernex-agent/agent.conf", "host Agent configuration file")
	flags.StringVar(&opts.mcpURL, "mcp-url", "http://127.0.0.1:8080/mcp", "InferNex MCP endpoint")
	flags.StringVar(&opts.baseURL, "base-url", "", "OpenAI-compatible base URL (overrides config)")
	flags.StringVar(&opts.model, "model", "", "OpenAI-compatible model name (overrides config)")
	flags.StringVar(&opts.apiKeyFile, "api-key-file", "", "API key file (overrides config)")
	flags.DurationVar(&opts.timeout, "timeout", 3*time.Minute, "per-attempt model request timeout")
	flags.StringVar(&opts.ask, "ask", "", "ask once and exit; write tools are denied")
	flags.IntVar(&opts.maxToolRounds, "max-tool-rounds", 8, "maximum model/tool rounds per question")
	flags.IntVar(&opts.contextWindowTokens, "context-window-tokens", infernexchat.DefaultContextWindowTokens, "model context window token budget")
	flags.IntVar(&opts.maxOutputTokens, "max-output-tokens", 0, "output tokens reserved and requested per model call; default is derived from the context window")
	flags.IntVar(&opts.contextThreshold, "context-compaction-threshold", infernexchat.DefaultCompactionThresholdPercent, "context usage percent that triggers compaction")
	flags.IntVar(&opts.keepRecentTurns, "context-keep-recent-turns", infernexchat.DefaultKeepRecentTurns, "recent user turns retained verbatim during compaction")
	flags.IntVar(&opts.toolResultMaxTokens, "tool-result-max-tokens", 0, "approximate token cap for one tool result; default is 15% of the window up to 4096")
	flags.BoolVar(&opts.verbose, "verbose", false, "print bounded tool results to stderr")
	if err := flags.Parse(args); err != nil {
		return chatOptions{}, err
	}
	if flags.NArg() != 0 {
		return chatOptions{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if opts.maxToolRounds < 1 || opts.maxToolRounds > 32 {
		return chatOptions{}, fmt.Errorf("--max-tool-rounds must be between 1 and 32")
	}

	explicit := map[string]bool{}
	flags.Visit(func(item *flag.Flag) { explicit[item.Name] = true })
	fileOpts, err := readModelFileOptions(opts.configPath)
	if err != nil {
		return chatOptions{}, err
	}
	if !explicit["base-url"] {
		opts.baseURL = fileOpts.baseURL
	}
	if !explicit["model"] {
		opts.model = fileOpts.model
	}
	if !explicit["api-key-file"] {
		opts.apiKeyFile = fileOpts.apiKeyFile
	}
	if !explicit["timeout"] && fileOpts.timeout > 0 {
		opts.timeout = fileOpts.timeout
	}
	if !explicit["context-window-tokens"] && fileOpts.contextWindowTokens > 0 {
		opts.contextWindowTokens = fileOpts.contextWindowTokens
	}
	if !explicit["max-output-tokens"] && fileOpts.maxOutputTokens > 0 {
		opts.maxOutputTokens = fileOpts.maxOutputTokens
	}
	if !explicit["context-compaction-threshold"] && fileOpts.contextThreshold > 0 {
		opts.contextThreshold = fileOpts.contextThreshold
	}
	if !explicit["context-keep-recent-turns"] && fileOpts.keepRecentTurns > 0 {
		opts.keepRecentTurns = fileOpts.keepRecentTurns
	}
	if !explicit["tool-result-max-tokens"] && fileOpts.toolResultMaxTokens > 0 {
		opts.toolResultMaxTokens = fileOpts.toolResultMaxTokens
	}
	if strings.TrimSpace(opts.baseURL) == "" || strings.TrimSpace(opts.model) == "" {
		return chatOptions{}, fmt.Errorf(
			"interactive model is not configured; run sudo /opt/infernex-agent/bin/configure-model.sh --base-url <URL> --model <MODEL> --api-key-file <FILE> --test-tools",
		)
	}
	resolved, err := infernexchat.ResolveContextConfig(infernexchat.ContextConfig{
		WindowTokens: opts.contextWindowTokens, MaxOutputTokens: opts.maxOutputTokens,
		CompactionThresholdPercent: opts.contextThreshold, KeepRecentTurns: opts.keepRecentTurns,
		ToolResultMaxTokens: opts.toolResultMaxTokens,
	})
	if err != nil {
		return chatOptions{}, fmt.Errorf("invalid chat context configuration: %w", err)
	}
	opts.contextWindowTokens = resolved.WindowTokens
	opts.maxOutputTokens = resolved.MaxOutputTokens
	opts.contextThreshold = resolved.CompactionThresholdPercent
	opts.keepRecentTurns = resolved.KeepRecentTurns
	opts.toolResultMaxTokens = resolved.ToolResultMaxTokens
	return opts, nil
}

func readModelFileOptions(path string) (modelFileOptions, error) {
	result := modelFileOptions{}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("open Agent configuration %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(io.LimitReader(file, 1024*1024))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "--openai-base-url="):
			result.baseURL = strings.TrimPrefix(line, "--openai-base-url=")
		case strings.HasPrefix(line, "--openai-model="):
			result.model = strings.TrimPrefix(line, "--openai-model=")
		case strings.HasPrefix(line, "--openai-api-key-file="):
			result.apiKeyFile = strings.TrimPrefix(line, "--openai-api-key-file=")
		case strings.HasPrefix(line, "--openai-timeout="):
			value := strings.TrimPrefix(line, "--openai-timeout=")
			result.timeout, err = time.ParseDuration(value)
			if err != nil {
				return modelFileOptions{}, fmt.Errorf("parse --openai-timeout in %s: %w", path, err)
			}
		case strings.HasPrefix(line, "--context-window-tokens="):
			result.contextWindowTokens, err = parsePositiveConfigInt(line, "--context-window-tokens=")
		case strings.HasPrefix(line, "--max-output-tokens="):
			result.maxOutputTokens, err = parsePositiveConfigInt(line, "--max-output-tokens=")
		case strings.HasPrefix(line, "--context-compaction-threshold="):
			result.contextThreshold, err = parsePositiveConfigInt(line, "--context-compaction-threshold=")
		case strings.HasPrefix(line, "--context-keep-recent-turns="):
			result.keepRecentTurns, err = parsePositiveConfigInt(line, "--context-keep-recent-turns=")
		case strings.HasPrefix(line, "--tool-result-max-tokens="):
			result.toolResultMaxTokens, err = parsePositiveConfigInt(line, "--tool-result-max-tokens=")
		}
		if err != nil {
			return modelFileOptions{}, err
		}
	}
	if err := scanner.Err(); err != nil {
		return modelFileOptions{}, fmt.Errorf("read Agent configuration %s: %w", path, err)
	}
	return result, nil
}

func parsePositiveConfigInt(line, prefix string) (int, error) {
	value := strings.TrimPrefix(line, prefix)
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("parse %s in Agent configuration: expected a positive integer", strings.TrimSuffix(prefix, "="))
	}
	return parsed, nil
}

func readAPIKey(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open model API key file %s: %w", path, err)
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, maxAPIKeyBytes+1))
	if err != nil {
		return "", fmt.Errorf("read model API key file %s: %w", path, err)
	}
	if len(payload) > maxAPIKeyBytes {
		return "", fmt.Errorf("model API key file %s exceeds %d bytes", path, maxAPIKeyBytes)
	}
	return strings.TrimSpace(string(payload)), nil
}

func interactiveChat(
	ctx context.Context,
	reader *bufio.Reader,
	output io.Writer,
	conversation *infernexchat.Conversation,
) error {
	fmt.Fprintln(output, "InferNex Agent interactive terminal. Enter /help for commands.")
	for {
		fmt.Fprint(output, "infernex> ")
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read terminal input: %w", err)
		}
		input := strings.TrimSpace(line)
		switch input {
		case "":
		case "/exit", "/quit", "exit", "quit":
			return nil
		case "/clear":
			conversation.Reset()
			fmt.Fprintln(output, "Conversation cleared.")
		case "/compact":
			if compactErr := conversation.Compact(ctx); compactErr != nil {
				fmt.Fprintf(output, "error: %v\n", compactErr)
			} else {
				fmt.Fprintln(output, "Conversation context compacted.")
			}
		case "/context":
			stats := conversation.ContextStats()
			fmt.Fprintf(output, "Context: estimated=%d + output-reserve=%d / window=%d tokens; threshold=%d; messages=%d; compactions=%d; pruned-tool-results=%d\n",
				stats.EstimatedInputTokens, stats.MaxOutputTokens, stats.WindowTokens, stats.ThresholdTokens,
				stats.MessageCount, stats.Compactions, stats.PrunedToolResults)
		case "/help":
			fmt.Fprintln(output, "Commands: /help, /context, /compact, /clear, /exit. Read-only tools run automatically; every write asks for exact 'yes'.")
		default:
			answer, askErr := conversation.Ask(ctx, input)
			if askErr != nil {
				if ctx.Err() != nil {
					return nil
				}
				fmt.Fprintf(output, "error: %v\n", askErr)
			} else {
				fmt.Fprintln(output, answer)
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

func interactiveApprover(reader *bufio.Reader, output io.Writer) infernexchat.Approver {
	return func(_ context.Context, request infernexchat.ApprovalRequest) (bool, error) {
		arguments, err := json.MarshalIndent(request.Arguments, "", "  ")
		if err != nil {
			return false, fmt.Errorf("format write arguments: %w", err)
		}
		fmt.Fprintf(
			output,
			"\nWRITE approval required\ntool: %s\narguments: %s\nType yes to continue: ",
			boundedTerminalText(request.Tool, 256),
			boundedTerminalText(string(arguments), 4096),
		)
		answer, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		return strings.TrimSpace(answer) == "yes", nil
	}
}

func terminalProgress(output io.Writer, verbose bool) infernexchat.Progress {
	return func(event infernexchat.ProgressEvent) {
		switch event.Kind {
		case "tool-call":
			mode := "read"
			if !event.ReadOnly {
				mode = "write"
			}
			fmt.Fprintf(output, "[%s tool] %s\n", mode, boundedTerminalText(event.Tool, 256))
		case "tool-denied":
			fmt.Fprintf(output, "[write denied] %s\n", boundedTerminalText(event.Tool, 256))
		case "tool-result":
			if verbose {
				fmt.Fprintf(
					output,
					"[tool result] %s: %s\n",
					boundedTerminalText(event.Tool, 256),
					boundedTerminalText(event.Message, 4096),
				)
			}
		case "context-compaction":
			fmt.Fprintf(output, "[context] %s\n", boundedTerminalText(event.Message, 512))
		}
	}
}

func boundedTerminalText(value string, limit int) string {
	value = strings.Map(func(character rune) rune {
		switch {
		case character == '\n' || character == '\t':
			return character
		case character < 0x20 || character == 0x7f:
			return -1
		case character >= 0x202a && character <= 0x202e:
			return -1
		case character >= 0x2066 && character <= 0x2069:
			return -1
		default:
			return character
		}
	}, value)
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "...[truncated]"
}
