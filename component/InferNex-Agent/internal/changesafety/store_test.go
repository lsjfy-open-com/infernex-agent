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

package changesafety

import (
	"testing"
	"time"
)

func TestFileStorePersistsAppendOnlyChangeEvents(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := "00112233445566778899aabbccddeeff"
	record := ChangeRecord{
		APIVersion: "agent.infernex.io/v1alpha1",
		Kind:       "InferNexChange",
		ID:         id,
		Action:     "deploy",
		Status:     StatusPlanned,
		Target: Target{
			APIVersion: "infernex.infernex.io/v1alpha1",
			Kind:       "InferNexService",
			Namespace:  "models",
			Name:       "tiny",
		},
		OccurredAt: time.Now().UTC(),
	}
	if err := store.Append(record); err != nil {
		t.Fatal(err)
	}
	record.Status = StatusApplied
	record.OccurredAt = record.OccurredAt.Add(time.Nanosecond)
	if err := store.Append(record); err != nil {
		t.Fatal(err)
	}

	latest, err := store.Latest(id)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Status != StatusApplied {
		t.Fatalf("latest status = %q", latest.Status)
	}
	pending, err := store.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != id {
		t.Fatalf("pending = %#v", pending)
	}
}

func TestPlannedChangeIsRecoverableAfterRestart(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record := ChangeRecord{
		APIVersion: "agent.infernex.io/v1alpha1",
		Kind:       "InferNexChange",
		ID:         "ffeeddccbbaa99887766554433221100",
		Action:     "deploy",
		Status:     StatusPlanned,
		Target:     Target{Kind: "InferNexService", Namespace: "models", Name: "tiny"},
		OccurredAt: time.Now().UTC(),
	}
	if err := store.Append(record); err != nil {
		t.Fatal(err)
	}
	pending, err := store.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Status != StatusPlanned {
		t.Fatalf("pending = %#v", pending)
	}
}
