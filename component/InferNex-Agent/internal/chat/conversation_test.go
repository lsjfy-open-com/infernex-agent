/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type fakeModel struct {
	responses []ModelResponse
	messages  [][]Message
}

func (f *fakeModel) Complete(_ context.Context, messages []Message, _ []ToolDefinition) (ModelResponse, error) {
	f.messages = append(f.messages, append([]Message(nil), messages...))
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response, nil
}

type fakeTools struct {
	definitions []ToolDefinition
	calls       []FunctionCall
	result      string
}

func (f *fakeTools) ListTools(context.Context) ([]ToolDefinition, error) {
	return f.definitions, nil
}

func (f *fakeTools) CallTool(_ context.Context, name string, arguments map[string]any) (ToolResult, error) {
	f.calls = append(f.calls, FunctionCall{Name: name, Arguments: mustJSON(arguments)})
	result := f.result
	if result == "" {
		result = `{"healthy":true}`
	}
	return ToolResult{Content: result}, nil
}

func (f *fakeTools) Close() error { return nil }

func mustJSON(value any) string {
	payload, _ := json.Marshal(value)
	return string(payload)
}

func TestConversationRunsReadOnlyToolWithoutApproval(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{
		{ToolCalls: []FunctionCall{{ID: "call-1", Name: "scan", Arguments: `{"namespace":"models"}`}}},
		{Content: "集群健康。"},
	}}
	tools := &fakeTools{definitions: []ToolDefinition{{Name: "scan", ReadOnly: true}}}
	conversation, err := NewConversation(context.Background(), Config{Model: model, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	answer, err := conversation.Ask(context.Background(), "检查集群")
	if err != nil {
		t.Fatal(err)
	}
	if answer != "集群健康。" || len(tools.calls) != 1 {
		t.Fatalf("answer=%q calls=%v", answer, tools.calls)
	}
	lastMessages := model.messages[1]
	if got := lastMessages[len(lastMessages)-1].Content; got != `{"healthy":true}` {
		t.Fatalf("tool result not forwarded to model: %s", got)
	}
}

func TestConversationDeniesWriteWithoutCallingTool(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{
		{ToolCalls: []FunctionCall{{ID: "call-1", Name: "deploy", Arguments: `{}`}}},
		{Content: "操作已被拒绝。"},
	}}
	tools := &fakeTools{definitions: []ToolDefinition{{Name: "deploy", ReadOnly: false}}}
	conversation, err := NewConversation(context.Background(), Config{
		Model: model,
		Tools: tools,
		Approver: func(context.Context, ApprovalRequest) (bool, error) {
			return false, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.Ask(context.Background(), "部署服务"); err != nil {
		t.Fatal(err)
	}
	if len(tools.calls) != 0 {
		t.Fatalf("denied write called tool: %v", tools.calls)
	}
	lastMessages := model.messages[1]
	if got := lastMessages[len(lastMessages)-1].Content; !strings.Contains(got, "local operator denied") {
		t.Fatalf("denial not visible to model: %s", got)
	}
}

func TestConversationRejectsTrailingToolArguments(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{
		{ToolCalls: []FunctionCall{{ID: "call-1", Name: "scan", Arguments: `{} {}`}}},
		{Content: "参数非法。"},
	}}
	tools := &fakeTools{definitions: []ToolDefinition{{Name: "scan", ReadOnly: true}}}
	conversation, err := NewConversation(context.Background(), Config{Model: model, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.Ask(context.Background(), "检查"); err != nil {
		t.Fatal(err)
	}
	if len(tools.calls) != 0 {
		t.Fatalf("invalid arguments called tool: %v", tools.calls)
	}
}

func TestConversationCapsSingleToolResult(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{
		{ToolCalls: []FunctionCall{{ID: "call-1", Name: "logs", Arguments: `{}`}}},
		{Content: "diagnosed"},
	}}
	tools := &fakeTools{
		definitions: []ToolDefinition{{Name: "logs", ReadOnly: true}},
		result:      strings.Repeat("log-line ", 1000),
	}
	conversation, err := NewConversation(context.Background(), Config{
		Model: model,
		Tools: tools,
		Context: ContextConfig{
			WindowTokens: 4096, MaxOutputTokens: 128,
			CompactionThresholdPercent: 95, KeepRecentTurns: 4, ToolResultMaxTokens: 128,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.Ask(context.Background(), "inspect logs"); err != nil {
		t.Fatal(err)
	}
	toolResult := model.messages[1][len(model.messages[1])-1].Content
	if !strings.Contains(toolResult, "tool result capped") {
		t.Fatalf("tool result was not capped: %q", toolResult)
	}
	if estimateTextTokens(toolResult) > 180 {
		t.Fatalf("capped tool result is unexpectedly large: %d tokens", estimateTextTokens(toolResult))
	}
}

func TestConversationStoresLargeToolResultAndReadsItProgressively(t *testing.T) {
	largeResult := "first line\n" + strings.Repeat("ordinary log line\n", 200) + "fatal marker\n"
	model := &fakeModel{responses: []ModelResponse{
		{ToolCalls: []FunctionCall{{ID: "call-1", Name: "logs", Arguments: `{}`}}},
		{Content: "found the failure"},
	}}
	tools := &fakeTools{
		definitions: []ToolDefinition{{Name: "logs", ReadOnly: true}}, result: largeResult,
	}
	conversation, err := NewConversation(context.Background(), Config{
		Model: model, Tools: tools, Artifacts: ArtifactConfig{Directory: t.TempDir()},
		Context: ContextConfig{
			WindowTokens: 4096, MaxOutputTokens: 128,
			CompactionThresholdPercent: 95, KeepRecentTurns: 4, ToolResultMaxTokens: 128,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	answer, err := conversation.Ask(context.Background(), "inspect logs")
	if err != nil {
		t.Fatal(err)
	}
	if answer != "found the failure" {
		t.Fatalf("answer=%q", answer)
	}
	if len(model.messages) < 2 {
		t.Fatalf("expected a model call after the artifact was stored, got %d", len(model.messages))
	}
	envelope := model.messages[1][len(model.messages[1])-1].Content
	if !strings.Contains(envelope, `"artifact_id":"sha256:`) || strings.Contains(envelope, largeResult) {
		t.Fatalf("large result was not externalized: %s", envelope)
	}
	var metadata struct {
		ArtifactID string `json:"artifact_id"`
	}
	if err := json.Unmarshal([]byte(envelope), &metadata); err != nil || metadata.ArtifactID == "" {
		t.Fatalf("decode artifact envelope: id=%q err=%v", metadata.ArtifactID, err)
	}
	readResult, err := conversation.artifacts.read(map[string]any{
		"artifact_id": metadata.ArtifactID, "contains": "fatal", "max_lines": float64(10),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readResult, "fatal marker") || strings.Contains(readResult, "ordinary log line") {
		t.Fatalf("artifact filter did not return a bounded match: %s", readResult)
	}
}

func TestConversationSummarizesWhenToolRoundBudgetIsExhausted(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{
		{ToolCalls: []FunctionCall{{ID: "call-1", Name: "scan", Arguments: `{}`}}},
		{ToolCalls: []FunctionCall{{ID: "call-2", Name: "scan", Arguments: `{"page":2}`}}},
		{Content: "partial conclusion from collected evidence"},
	}}
	tools := &fakeTools{definitions: []ToolDefinition{{Name: "scan", ReadOnly: true}}}
	conversation, err := NewConversation(context.Background(), Config{
		Model: model, Tools: tools, MaxToolRounds: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	answer, err := conversation.Ask(context.Background(), "inspect everything")
	if err != nil {
		t.Fatal(err)
	}
	if answer != "partial conclusion from collected evidence" || len(tools.calls) != 1 {
		t.Fatalf("answer=%q calls=%v", answer, tools.calls)
	}
}

func TestConversationBlocksRepeatedIdenticalToolLoop(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{
		{ToolCalls: []FunctionCall{{ID: "call-1", Name: "scan", Arguments: `{"namespace":"models"}`}}},
		{ToolCalls: []FunctionCall{{ID: "call-2", Name: "scan", Arguments: `{ "namespace": "models" }`}}},
		{ToolCalls: []FunctionCall{{ID: "call-3", Name: "scan", Arguments: `{"namespace":"models"}`}}},
		{Content: "stopped the loop and summarized two observations"},
	}}
	tools := &fakeTools{definitions: []ToolDefinition{{Name: "scan", ReadOnly: true}}}
	conversation, err := NewConversation(context.Background(), Config{Model: model, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	answer, err := conversation.Ask(context.Background(), "inspect namespace")
	if err != nil {
		t.Fatal(err)
	}
	if answer != "stopped the loop and summarized two observations" || len(tools.calls) != 2 {
		t.Fatalf("answer=%q calls=%v", answer, tools.calls)
	}
}

func TestConversationRetriesOneEmptyModelResponseAndTracksUsage(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{
		{Usage: TokenUsage{PromptTokens: 10, CompletionTokens: 1, TotalTokens: 11}},
		{Content: "recovered", Usage: TokenUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}},
	}}
	conversation, err := NewConversation(context.Background(), Config{
		Model: model, Tools: &fakeTools{definitions: []ToolDefinition{{Name: "scan", ReadOnly: true}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	answer, err := conversation.Ask(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	stats := conversation.ContextStats()
	if answer != "recovered" || stats.ModelCalls != 2 || stats.ReportedTotalTokens != 23 {
		t.Fatalf("answer=%q stats=%#v", answer, stats)
	}
}

type contextAwareModel struct {
	compactions int
}

func (m *contextAwareModel) Complete(_ context.Context, _ []Message, tools []ToolDefinition) (ModelResponse, error) {
	if tools == nil {
		m.compactions++
		return ModelResponse{Content: "durable facts and unresolved work"}, nil
	}
	return ModelResponse{Content: "acknowledged"}, nil
}

func TestConversationCompactsOldTurnsBeforeModelCall(t *testing.T) {
	model := &contextAwareModel{}
	conversation, err := NewConversation(context.Background(), Config{
		Model: model,
		Tools: &fakeTools{definitions: []ToolDefinition{{Name: "scan", ReadOnly: true}}},
		Context: ContextConfig{
			WindowTokens: 2048, MaxOutputTokens: 128,
			CompactionThresholdPercent: 50, KeepRecentTurns: 1, ToolResultMaxTokens: 128,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	question := strings.Repeat("inspect namespace state and remember evidence; ", 30)
	for index := 0; index < 4 && model.compactions == 0; index++ {
		if _, err := conversation.Ask(context.Background(), question); err != nil {
			t.Fatal(err)
		}
	}
	stats := conversation.ContextStats()
	if model.compactions == 0 || stats.Compactions == 0 {
		t.Fatalf("expected automatic compaction, model=%d stats=%#v", model.compactions, stats)
	}
	if len(conversation.messages) < 2 || !strings.HasPrefix(conversation.messages[1].Content, memoryPrefix) {
		t.Fatalf("compacted memory not installed: %#v", conversation.messages)
	}
}

func TestConversationRejectsRequestBeyondHardWindow(t *testing.T) {
	conversation, err := NewConversation(context.Background(), Config{
		Model: &contextAwareModel{},
		Tools: &fakeTools{definitions: []ToolDefinition{{Name: "scan", ReadOnly: true}}},
		Context: ContextConfig{
			WindowTokens: 2048, MaxOutputTokens: 128,
			CompactionThresholdPercent: 50, KeepRecentTurns: 1, ToolResultMaxTokens: 128,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = conversation.Ask(context.Background(), strings.Repeat("集", 3000))
	if err == nil || !strings.Contains(err.Error(), "exceeding configured context window") {
		t.Fatalf("expected context window error, got %v", err)
	}
	if _, err := conversation.Ask(context.Background(), "short retry"); err != nil {
		t.Fatalf("oversized request poisoned the conversation: %v", err)
	}
}

func TestConversationUndoRemovesLastTurnAndAllowsCorrection(t *testing.T) {
	model := &fakeModel{responses: []ModelResponse{
		{Content: "first answer"},
		{Content: "wrong answer"},
		{Content: "corrected answer"},
	}}
	conversation, err := NewConversation(context.Background(), Config{
		Model: model,
		Tools: &fakeTools{definitions: []ToolDefinition{{Name: "scan", ReadOnly: true}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.Ask(context.Background(), "first request"); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.Ask(context.Background(), "mistaken request"); err != nil {
		t.Fatal(err)
	}
	if !conversation.UndoLastTurn() {
		t.Fatal("expected the last turn to be removed")
	}
	if _, err := conversation.Ask(context.Background(), "corrected request"); err != nil {
		t.Fatal(err)
	}
	lastCall := model.messages[len(model.messages)-1]
	joined := ""
	for _, message := range lastCall {
		joined += "\n" + message.Content
	}
	if strings.Contains(joined, "mistaken request") || strings.Contains(joined, "wrong answer") {
		t.Fatalf("undone turn remained in model context: %s", joined)
	}
	if !strings.Contains(joined, "first request") || !strings.Contains(joined, "corrected request") {
		t.Fatalf("expected retained and corrected turns: %s", joined)
	}
}
