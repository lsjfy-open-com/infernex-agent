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

package analyzer

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

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/supervisor"
)

const (
	maxResponseBytes = 1024 * 1024
	systemPrompt     = `You are the read-only diagnostic analyst for an InferNex inference cluster.
Use only the supplied normalized evidence. Identify the likely cause, cite the evidence,
and provide prioritized verification and remediation advice. Do not claim that an action
was executed. Do not output Kubernetes YAML, shell commands that mutate the cluster,
secrets, credentials, or arbitrary workload specifications. Prefer existing InferNex
components: Bridge for lifecycle, PD-Orchestrator for scaling, Hermes for routing,
Eagle-Eye and infernex-checker for hardware and network diagnosis. Be concise.`
)

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

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	Stream      bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message chatMessage `json:"message"`
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

func (o *OpenAI) Analyze(
	ctx context.Context,
	evidence supervisor.AnalysisRequest,
) (supervisor.AnalysisResult, error) {
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return supervisor.AnalysisResult{}, fmt.Errorf("encode normalized evidence: %w", err)
	}
	payload, err := json.Marshal(chatRequest{
		Model: o.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: "Analyze this InferNex evidence:\n" + string(evidenceJSON)},
		},
		Temperature: 0,
		Stream:      false,
	})
	if err != nil {
		return supervisor.AnalysisResult{}, fmt.Errorf("encode OpenAI request: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint, bytes.NewReader(payload))
	if err != nil {
		return supervisor.AnalysisResult{}, fmt.Errorf("build OpenAI request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if o.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	response, err := o.client.Do(request)
	if err != nil {
		return supervisor.AnalysisResult{}, fmt.Errorf("call OpenAI-compatible endpoint: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return supervisor.AnalysisResult{}, fmt.Errorf("read OpenAI response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return supervisor.AnalysisResult{}, fmt.Errorf("OpenAI response exceeds %d bytes", maxResponseBytes)
	}

	decoded := chatResponse{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return supervisor.AnalysisResult{}, fmt.Errorf(
			"decode OpenAI response with HTTP status %d: %w",
			response.StatusCode,
			err,
		)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message := strings.TrimSpace(response.Status)
		if decoded.Error != nil && strings.TrimSpace(decoded.Error.Message) != "" {
			message = decoded.Error.Message
		}
		return supervisor.AnalysisResult{}, fmt.Errorf(
			"OpenAI endpoint returned HTTP %d: %s",
			response.StatusCode,
			boundedError(message),
		)
	}
	if decoded.Error != nil {
		return supervisor.AnalysisResult{}, fmt.Errorf(
			"OpenAI endpoint returned an error: %s",
			boundedError(decoded.Error.Message),
		)
	}
	if len(decoded.Choices) == 0 {
		return supervisor.AnalysisResult{}, fmt.Errorf("OpenAI response has no choices")
	}
	content := strings.TrimSpace(decoded.Choices[0].Message.Content)
	if content == "" {
		return supervisor.AnalysisResult{}, fmt.Errorf("OpenAI response content is empty")
	}
	model := strings.TrimSpace(decoded.Model)
	if model == "" {
		model = o.model
	}
	return supervisor.AnalysisResult{
		Provider: "openai-compatible",
		Model:    model,
		Content:  content,
	}, nil
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

func boundedError(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= 512 {
		return value
	}
	return string(runes[:512]) + "…"
}
