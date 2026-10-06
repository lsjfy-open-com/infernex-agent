package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domain"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domainstore"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/privateinventory"
)

type privateToolStore struct{}

func (privateToolStore) Scope() string { return "synthetic-scope" }
func (privateToolStore) Get(context.Context, string, domain.RecordRef) (domain.Record, error) {
	return nil, errors.New("not found")
}
func (privateToolStore) Verify(context.Context, string, domain.RecordRef) (domain.Record, error) {
	return nil, errors.New("not found")
}
func (privateToolStore) List(context.Context, string, domainstore.ListOptions) ([]domainstore.ListEntry, error) {
	return []domainstore.ListEntry{}, nil
}
func (privateToolStore) ListPage(context.Context, string, domainstore.ListOptions) (domainstore.ListPage, error) {
	return domainstore.ListPage{Entries: []domainstore.ListEntry{}}, nil
}
func (privateToolStore) Connection(context.Context, string, domain.RecordRef) (domainstore.Connection, error) {
	return domainstore.Connection{}, errors.New("not found")
}
func (privateToolStore) CurrentEnvironment(context.Context, string, string) (*domain.Environment, error) {
	return nil, errors.New("not found")
}
func (privateToolStore) SaveSnapshot(context.Context, string, *domain.InventorySnapshot) (domain.RecordRef, error) {
	return domain.RecordRef{}, errors.New("not found")
}

func TestPrivateInventoryToolAnnotationsAndSchemas(t *testing.T) {
	service, err := privateinventory.New(privateToolStore{})
	if err != nil {
		t.Fatal(err)
	}
	server := New(nil, "test", WithInferNexBridge(false), WithPrivateInventory(service, "uid:1000"))
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	listed, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]bool{
		"infernex_discover_private_environment": false,
		"infernex_list_domain_records":          false,
		"infernex_get_domain_record":            false,
		"infernex_verify_domain_record":         false,
		"infernex_record_domain_inventory":      false,
	}
	if len(listed.Tools) != len(expected) {
		t.Fatalf("private-only tool count = %d, want %d", len(listed.Tools), len(expected))
	}
	for _, tool := range listed.Tools {
		if _, ok := expected[tool.Name]; !ok {
			t.Fatalf("unexpected private-only tool %q", tool.Name)
		}
		expected[tool.Name] = true
		if tool.Annotations == nil || !tool.Annotations.IdempotentHint || tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint || tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Fatalf("unsafe annotations for %s: %#v", tool.Name, tool.Annotations)
		}
		wantReadOnly := tool.Name != "infernex_record_domain_inventory"
		if tool.Annotations.ReadOnlyHint != wantReadOnly {
			t.Fatalf("readOnlyHint for %s = %v", tool.Name, tool.Annotations.ReadOnlyHint)
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schemaObject struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schemaObject); err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"tenantscope", "endpoint", "credential", "filepath", "snapshot"} {
			for property := range schemaObject.Properties {
				if strings.Contains(strings.ToLower(property), forbidden) {
					t.Fatalf("schema for %s exposes forbidden %q property: %s", tool.Name, forbidden, raw)
				}
			}
		}
		schema := strings.ToLower(string(raw))
		if tool.Name == "infernex_record_domain_inventory" && (!strings.Contains(schema, "previewhandle") || !strings.Contains(schema, "digest") || len(schemaObject.Properties) != 2) {
			t.Fatalf("record schema lacks bound handle and digest: %s", raw)
		}
	}
	for name, seen := range expected {
		if !seen {
			t.Fatalf("missing private tool %s", name)
		}
	}
}

func TestPrivateListResponseBudgetNeverBreaksJSON(t *testing.T) {
	if err := ensurePrivateResponseBudget(struct {
		Value string `json:"value"`
	}{Value: strings.Repeat("x", domain.MaxToolResponseBytes)}); err == nil {
		t.Fatal("oversize response accepted")
	}
	if err := ensurePrivateResponseBudget(struct {
		Value string `json:"value"`
	}{Value: "bounded"}); err != nil {
		t.Fatal(err)
	}
}
