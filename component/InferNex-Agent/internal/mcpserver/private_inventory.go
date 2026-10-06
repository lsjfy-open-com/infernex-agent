package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domain"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domainstore"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/privateinventory"
)

type privateDiscoverInput struct {
	EnvironmentID string   `json:"environmentId" jsonschema:"Registered Environment UUID"`
	Revision      int64    `json:"revision" jsonschema:"Exact registered Environment revision"`
	ResourceKinds []string `json:"resourceKinds,omitempty" jsonschema:"Optional registered-runtime resource subset"`
	Namespaces    []string `json:"namespaces,omitempty" jsonschema:"Optional subset of registered Kubernetes namespaces"`
	IncludeNodes  bool     `json:"includeNodes,omitempty" jsonschema:"Read authorized Kubernetes Node summaries"`
	CursorHandle  string   `json:"cursorHandle,omitempty" jsonschema:"Opaque Kubernetes continuation handle from a prior preview"`
}

type privateListInput struct {
	Kind         domain.Kind `json:"kind,omitempty" jsonschema:"Optional Environment or InventorySnapshot filter for the first page"`
	Limit        int         `json:"limit,omitempty" jsonschema:"First-page maximum; defaults to 20 and cannot exceed 100"`
	CursorHandle string      `json:"cursorHandle,omitempty" jsonschema:"Opaque continuation handle; send without kind or limit"`
}

type privateRecordInput struct {
	PreviewHandle string `json:"previewHandle" jsonschema:"Opaque handle returned by the current trusted stdio server"`
	Digest        string `json:"digest" jsonschema:"Exact snapshot digest returned with the preview handle"`
}

type privateRefInput struct {
	Kind     domain.Kind `json:"kind" jsonschema:"Environment or InventorySnapshot"`
	ID       string      `json:"id" jsonschema:"Record UUID"`
	Revision int64       `json:"revision" jsonschema:"Exact immutable record revision"`
}

type privateListOutput struct {
	Records      []domainstore.ListEntry `json:"records"`
	Returned     int                     `json:"returned"`
	Truncated    bool                    `json:"truncated"`
	CursorHandle string                  `json:"cursorHandle,omitempty"`
}

type privateGetOutput struct {
	Record domain.Record `json:"record"`
}

type privateVerifyOutput struct {
	Reference domain.RecordRef `json:"reference"`
	Valid     bool             `json:"valid"`
}

func registerPrivateInventoryTools(server *mcp.Server, service *privateinventory.Service, principal string) {
	readOnly := func(title string) *mcp.ToolAnnotations {
		no := false
		closed := false
		return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, IdempotentHint: true, DestructiveHint: &no, OpenWorldHint: &closed}
	}
	localRecord := func(title string) *mcp.ToolAnnotations {
		no := false
		closed := false
		return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: false, IdempotentHint: true, DestructiveHint: &no, OpenWorldHint: &closed}
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "infernex_discover_private_environment", Description: "Read a registered private Docker or Kubernetes environment into a bounded, redacted in-memory preview. The request cannot supply endpoints, credentials, tenant scope, commands, or snapshot content.",
		Annotations: readOnly("Discover registered private environment"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input privateDiscoverInput) (*mcp.CallToolResult, privateinventory.Preview, error) {
		result, err := service.Discover(ctx, privateinventory.DiscoverRequest{
			Principal: principal, EnvironmentID: input.EnvironmentID, Revision: input.Revision,
			ResourceKinds: input.ResourceKinds, Namespaces: input.Namespaces,
			IncludeNodes: input.IncludeNodes, CursorHandle: input.CursorHandle,
		})
		if err != nil {
			return nil, privateinventory.Preview{}, err
		}
		return nil, result.Preview, ensurePrivateResponseBudget(result.Preview)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "infernex_list_domain_records", Description: "List bounded immutable private inventory record summaries in the server-bound local scope.",
		Annotations: readOnly("List private inventory records"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input privateListInput) (*mcp.CallToolResult, privateListOutput, error) {
		page, err := service.ListPage(ctx, privateinventory.ListRequest{
			Principal: principal, Kind: input.Kind, Limit: input.Limit, CursorHandle: input.CursorHandle,
		})
		if err != nil {
			return nil, privateListOutput{}, err
		}
		output := privateListOutput{Records: page.Entries, Returned: len(page.Entries), Truncated: page.Truncated, CursorHandle: page.CursorHandle}
		if err := ensurePrivateResponseBudget(output); err != nil {
			return nil, privateListOutput{}, err
		}
		return nil, output, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "infernex_get_domain_record", Description: "Get one exact immutable private inventory record after scope and integrity checks. Connection paths and credentials are never returned.",
		Annotations: readOnly("Get private inventory record"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input privateRefInput) (*mcp.CallToolResult, privateGetOutput, error) {
		record, err := service.Get(ctx, domain.RecordRef{Kind: input.Kind, ID: input.ID, Revision: input.Revision})
		if err != nil {
			return nil, privateGetOutput{}, err
		}
		output := privateGetOutput{Record: record}
		if err := ensurePrivateResponseBudget(output); err != nil {
			return nil, privateGetOutput{}, err
		}
		return nil, output, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "infernex_verify_domain_record", Description: "Verify one exact immutable private inventory record and return its scoped reference without rewriting it.",
		Annotations: readOnly("Verify private inventory record"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input privateRefInput) (*mcp.CallToolResult, privateVerifyOutput, error) {
		record, err := service.Verify(ctx, domain.RecordRef{Kind: input.Kind, ID: input.ID, Revision: input.Revision})
		if err != nil {
			return nil, privateVerifyOutput{}, err
		}
		return nil, privateVerifyOutput{Reference: record.Reference(), Valid: true}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "infernex_record_domain_inventory", Description: "Explicitly record a server-held private inventory preview. Accepts only previewHandle and digest; scope, subject, Environment revision, and TTL are rechecked locally.",
		Annotations: localRecord("Record private inventory preview"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input privateRecordInput) (*mcp.CallToolResult, domain.RecordRef, error) {
		ref, err := service.Record(ctx, principal, input.PreviewHandle, input.Digest)
		return nil, ref, err
	})
}

func ensurePrivateResponseBudget(value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode private inventory response: %w", err)
	}
	if len(raw) > domain.MaxToolResponseBytes {
		return fmt.Errorf("private inventory response exceeds %d bytes", domain.MaxToolResponseBytes)
	}
	return nil
}
