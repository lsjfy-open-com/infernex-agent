/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package semanticmemory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileStorePersistsSearchesAndForgetsClusterMemory(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root, "cluster-a")
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Put(PutRequest{
		Scope: "cluster", Type: "configuration-baseline", Subject: "Qwen PD 稳定配置",
		Summary: "models 命名空间的 stable-qwen 已通过 warmup。", Tags: []string{"PD分离", "qwen"},
		Evidence: []string{"sha256:abc"}, Source: "tool-verified",
	})
	if err != nil {
		t.Fatal(err)
	}

	reopened, err := NewFileStore(root, "cluster-a")
	if err != nil {
		t.Fatal(err)
	}
	result, err := reopened.Search(SearchRequest{Query: "Qwen 稳定基线", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 1 || result.Records[0].ID != record.ID || result.Records[0].ClusterID != "cluster-a" {
		t.Fatalf("search result = %#v", result)
	}

	otherCluster, err := NewFileStore(root, "cluster-b")
	if err != nil {
		t.Fatal(err)
	}
	result, err = otherCluster.Search(SearchRequest{Query: "Qwen", Limit: 5})
	if err != nil || len(result.Records) != 0 {
		t.Fatalf("cross-cluster memory leaked: result=%#v err=%v", result, err)
	}

	forgotten, err := reopened.Forget(record.ID)
	if err != nil || forgotten.DeletedAt == nil {
		t.Fatalf("forget result=%#v err=%v", forgotten, err)
	}
	result, err = reopened.Search(SearchRequest{Query: "Qwen", Limit: 5})
	if err != nil || len(result.Records) != 0 {
		t.Fatalf("forgotten memory returned: result=%#v err=%v", result, err)
	}
}

func TestFileStoreRejectsUnverifiedAndExpiredMemory(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), "cluster-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(PutRequest{
		Scope: "cluster", Type: "fact", Subject: "guess", Summary: "model guessed this",
		Source: "model-inferred",
	}); err == nil {
		t.Fatal("unverified model memory was accepted")
	}
	expired := time.Now().UTC().Add(-time.Minute)
	if _, err := store.Put(PutRequest{
		Scope: "cluster", Type: "fact", Subject: "old", Summary: "expired",
		Source: "user-confirmed", ExpiresAt: &expired,
	}); err == nil {
		t.Fatal("expired memory was accepted")
	}
}

func TestReadableMemoryLegacyAndIndexRecovery(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root, "cluster-a")
	if err != nil {
		t.Fatal(err)
	}
	req := PutRequest{Scope: "cluster", Type: "incident", Subject: "Mooncake prefix hit 超时", Summary: "tool confirmed timeout", Source: "tool-verified"}
	first, err := store.Put(req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Put(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ID) != 64 || first.ID == second.ID || first.Path == second.Path || !strings.HasPrefix(filepath.Base(first.Path), "mooncake-prefix-hit-超时_") {
		t.Fatal(first, second)
	}
	reopened, err := NewFileStore(root, "cluster-a")
	if err != nil {
		t.Fatal(err)
	}
	// Exact hash lookup must not scan/decode unrelated files.
	broken := filepath.Join(root, "unrelated.json")
	if err := os.WriteFile(broken, []byte("invalid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := reopened.Search(SearchRequest{Query: first.ID})
	if err != nil || len(result.Records) != 1 || result.Records[0].Name != first.Name {
		t.Fatal(result, err)
	}
	if err := os.Remove(broken); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, ".index", first.ID)); err != nil {
		t.Fatal(err)
	}
	result, err = reopened.Search(SearchRequest{Query: first.ID})
	if err != nil || len(result.Records) != 1 {
		t.Fatal(result, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".index", first.ID)); err != nil {
		t.Fatal(err)
	}
	result, err = reopened.Search(SearchRequest{Query: strings.Repeat("0", 64)})
	if err != nil || len(result.Records) != 0 {
		t.Fatal(result, err)
	}
	result, err = reopened.Search(SearchRequest{Query: first.CreatedAt.UTC().Format("2006-01-02")})
	if err != nil || len(result.Records) != 2 {
		t.Fatal(result, err)
	}
	// Simulate an alpha.14 JSON record with a 32-character ID and no new fields.
	legacy := first
	legacy.ID = strings.Repeat("a", 32)
	legacy.Name = ""
	legacy.Path = ""
	data, _ := json.Marshal(legacy)
	legacyPath := filepath.Join(root, legacy.ID+".json")
	if err := os.WriteFile(legacyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err = reopened.Search(SearchRequest{Query: legacy.ID})
	if err != nil || len(result.Records) != 1 || result.Records[0].Name == "" {
		t.Fatal(result, err)
	}
	deleted, err := reopened.Forget(legacy.ID)
	if err != nil || deleted.DeletedAt == nil {
		t.Fatal(deleted, err)
	}
	result, err = reopened.Search(SearchRequest{Query: legacy.ID})
	if err != nil || len(result.Records) != 0 {
		t.Fatal(result, err)
	}
	deleted, err = reopened.Forget(first.ID)
	if err != nil || deleted.Path != first.Path || deleted.Name != first.Name {
		t.Fatal(deleted, err)
	}
	other, _ := NewFileStore(root, "cluster-b")
	result, err = other.Search(SearchRequest{Query: second.ID})
	if err != nil || len(result.Records) != 0 {
		t.Fatal(result, err)
	}
	if _, err := other.Forget(second.ID); err == nil {
		t.Fatal("cross-cluster forget allowed")
	}
}
