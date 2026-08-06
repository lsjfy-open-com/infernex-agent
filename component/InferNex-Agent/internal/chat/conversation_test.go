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
}

func (f *fakeTools) ListTools(context.Context) ([]ToolDefinition, error) {
	return f.definitions, nil
}

func (f *fakeTools) CallTool(_ context.Context, name string, arguments map[string]any) (ToolResult, error) {
	f.calls = append(f.calls, FunctionCall{Name: name, Arguments: mustJSON(arguments)})
	return ToolResult{Content: `{"healthy":true}`}, nil
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
