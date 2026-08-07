/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxModelResponseBytes = 1024 * 1024

type OpenAIConfig struct {
	BaseURL    string
	Model      string
	APIKey     string
	Timeout    time.Duration
	HTTPClient *http.Client
}

type OpenAI struct {
	endpoint string
	model    string
	apiKey   string
	client   *http.Client
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIToolCall struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function openAIToolCallFunction `json:"function"`
}

type openAIToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	Tools       []openAITool    `json:"tools,omitempty"`
	ToolChoice  string          `json:"tool_choice,omitempty"`
	Temperature float64         `json:"temperature"`
	Stream      bool            `json:"stream"`
}

type openAIResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message openAIMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

func NewOpenAI(config OpenAIConfig) (*OpenAI, error) {
	endpoint, err := chatCompletionsEndpoint(config.BaseURL)
	if err != nil {
		return nil, err
	}
	model := strings.TrimSpace(config.Model)
	if model == "" {
		return nil, fmt.Errorf("OpenAI model is required")
	}
	client := config.HTTPClient
	if client == nil {
		timeout := config.Timeout
		if timeout <= 0 {
			timeout = 60 * time.Second
		}
		client = &http.Client{Timeout: timeout}
	}
	return &OpenAI{
		endpoint: endpoint,
		model:    model,
		apiKey:   strings.TrimSpace(config.APIKey),
		client:   client,
	}, nil
}

func (o *OpenAI) Complete(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
) (ModelResponse, error) {
	requestMessages := make([]openAIMessage, 0, len(messages))
	for _, message := range messages {
		converted := openAIMessage{
			Role:       message.Role,
			Content:    message.Content,
			ToolCallID: message.ToolCallID,
		}
		for _, call := range message.ToolCalls {
			converted.ToolCalls = append(converted.ToolCalls, openAIToolCall{
				ID:   call.ID,
				Type: "function",
				Function: openAIToolCallFunction{
					Name:      call.Name,
					Arguments: call.Arguments,
				},
			})
		}
		requestMessages = append(requestMessages, converted)
	}
	requestTools := make([]openAITool, 0, len(tools))
	for _, tool := range tools {
		parameters := tool.InputSchema
		if parameters == nil {
			parameters = map[string]any{"type": "object"}
		}
		requestTools = append(requestTools, openAITool{
			Type: "function",
			Function: openAIFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  parameters,
			},
		})
	}
	requestPayload := openAIRequest{
		Model:       o.model,
		Messages:    requestMessages,
		Tools:       requestTools,
		Temperature: 0,
		Stream:      false,
	}
	if len(requestTools) > 0 {
		requestPayload.ToolChoice = "auto"
	}
	payload, err := json.Marshal(requestPayload)
	if err != nil {
		return ModelResponse{}, fmt.Errorf("encode OpenAI chat request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint, bytes.NewReader(payload))
	if err != nil {
		return ModelResponse{}, fmt.Errorf("build OpenAI chat request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if o.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+o.apiKey)
	}
	response, err := o.client.Do(request)
	if err != nil {
		return ModelResponse{}, fmt.Errorf("call OpenAI-compatible endpoint: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxModelResponseBytes+1))
	if err != nil {
		return ModelResponse{}, fmt.Errorf("read OpenAI response: %w", err)
	}
	if len(body) > maxModelResponseBytes {
		return ModelResponse{}, fmt.Errorf("OpenAI response exceeds %d bytes", maxModelResponseBytes)
	}
	decoded := openAIResponse{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return ModelResponse{}, fmt.Errorf(
			"decode OpenAI response with HTTP status %d: %w", response.StatusCode, err,
		)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message := strings.TrimSpace(response.Status)
		if decoded.Error != nil && strings.TrimSpace(decoded.Error.Message) != "" {
			message = decoded.Error.Message
		}
		return ModelResponse{}, fmt.Errorf("OpenAI endpoint returned HTTP %d: %s", response.StatusCode, bounded(message))
	}
	if decoded.Error != nil {
		return ModelResponse{}, fmt.Errorf("OpenAI endpoint returned an error: %s", bounded(decoded.Error.Message))
	}
	if len(decoded.Choices) == 0 {
		return ModelResponse{}, fmt.Errorf("OpenAI response has no choices")
	}
	choice := decoded.Choices[0].Message
	result := ModelResponse{Content: strings.TrimSpace(choice.Content)}
	for _, call := range choice.ToolCalls {
		if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Function.Name) == "" {
			return ModelResponse{}, fmt.Errorf("OpenAI response contains an invalid tool call")
		}
		result.ToolCalls = append(result.ToolCalls, FunctionCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: call.Function.Arguments,
		})
	}
	return result, nil
}

func chatCompletionsEndpoint(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("OpenAI base URL is required")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("parse OpenAI base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("OpenAI base URL must use http or https")
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("OpenAI base URL must include a host")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("OpenAI base URL must not contain user information")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	path := strings.TrimRight(parsed.Path, "/")
	switch {
	case strings.HasSuffix(path, "/chat/completions"):
	case strings.HasSuffix(path, "/v1"):
		path += "/chat/completions"
	default:
		path += "/v1/chat/completions"
	}
	parsed.Path = path
	return parsed.String(), nil
}

func bounded(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= 512 {
		return string(runes)
	}
	return string(runes[:512]) + "…"
}
