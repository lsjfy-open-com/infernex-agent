/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxToolResultRunes = 512 * 1024

type MCPClient struct {
	session *mcp.ClientSession
}

func NewMCPClient(ctx context.Context, endpoint, version string) (*MCPClient, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("MCP endpoint is required")
	}
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "infernex-agent-chat",
		Version: version,
	}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to InferNex MCP endpoint %s: %w", endpoint, err)
	}
	return &MCPClient{session: session}, nil
}

func (c *MCPClient) ListTools(ctx context.Context) ([]ToolDefinition, error) {
	definitions := make([]ToolDefinition, 0)
	cursor := ""
	for {
		result, err := c.session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		for _, tool := range result.Tools {
			readOnly := tool.Annotations != nil && tool.Annotations.ReadOnlyHint
			definitions = append(definitions, ToolDefinition{
				Name:        tool.Name,
				Description: tool.Description,
				InputSchema: tool.InputSchema,
				ReadOnly:    readOnly,
			})
		}
		if result.NextCursor == "" {
			break
		}
		cursor = result.NextCursor
	}
	return definitions, nil
}

func (c *MCPClient) CallTool(
	ctx context.Context,
	name string,
	arguments map[string]any,
) (ToolResult, error) {
	result, err := c.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return ToolResult{}, err
	}
	content := ""
	if result.StructuredContent != nil {
		encoded, encodeErr := json.Marshal(result.StructuredContent)
		if encodeErr != nil {
			return ToolResult{}, fmt.Errorf("encode structured MCP result: %w", encodeErr)
		}
		content = string(encoded)
	} else {
		parts := make([]string, 0, len(result.Content))
		for _, item := range result.Content {
			if text, ok := item.(*mcp.TextContent); ok {
				parts = append(parts, text.Text)
				continue
			}
			encoded, encodeErr := item.MarshalJSON()
			if encodeErr != nil {
				return ToolResult{}, fmt.Errorf("encode MCP content: %w", encodeErr)
			}
			parts = append(parts, string(encoded))
		}
		content = strings.Join(parts, "\n")
	}
	runes := []rune(content)
	if len(runes) > maxToolResultRunes {
		content = string(runes[:maxToolResultRunes]) + "\n…[tool result truncated]"
	}
	return ToolResult{Content: content, IsError: result.IsError}, nil
}

func (c *MCPClient) Close() error {
	return c.session.Close()
}
