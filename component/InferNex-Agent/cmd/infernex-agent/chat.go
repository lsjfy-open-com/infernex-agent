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
	"strings"
	"syscall"
	"time"

	infernexchat "gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/chat"
)

const maxAPIKeyBytes = 64 * 1024

type chatOptions struct {
	configPath    string
	mcpURL        string
	baseURL       string
	model         string
	apiKeyFile    string
	timeout       time.Duration
	ask           string
	maxToolRounds int
	verbose       bool
}

type modelFileOptions struct {
	baseURL    string
	model      string
	apiKeyFile string
	timeout    time.Duration
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
		BaseURL: opts.baseURL,
		Model:   opts.model,
		APIKey:  apiKey,
		Timeout: opts.timeout,
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
	flags.DurationVar(&opts.timeout, "timeout", time.Minute, "model request timeout")
	flags.StringVar(&opts.ask, "ask", "", "ask once and exit; write tools are denied")
	flags.IntVar(&opts.maxToolRounds, "max-tool-rounds", 8, "maximum model/tool rounds per question")
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
	if strings.TrimSpace(opts.baseURL) == "" || strings.TrimSpace(opts.model) == "" {
		return chatOptions{}, fmt.Errorf(
			"interactive model is not configured; run sudo /opt/infernex-agent/bin/configure-model.sh --base-url <URL> --model <MODEL> --api-key-file <FILE> --test-tools",
		)
	}
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
		}
	}
	if err := scanner.Err(); err != nil {
		return modelFileOptions{}, fmt.Errorf("read Agent configuration %s: %w", path, err)
	}
	return result, nil
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
		case "/help":
			fmt.Fprintln(output, "Commands: /help, /clear, /exit. Read-only tools run automatically; every write asks for exact 'yes'.")
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
