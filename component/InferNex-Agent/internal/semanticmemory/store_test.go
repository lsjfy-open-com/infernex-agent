/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package semanticmemory

import (
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
