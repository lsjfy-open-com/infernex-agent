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

package experiment

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileStoreReopensLatestPendingPlan(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 6, 8, 0, 0, 0, time.UTC)
	plan := Plan{
		APIVersion: "agent.infernex.io/v1alpha1",
		Kind:       "InferNexExperiment",
		ID:         "00112233445566778899aabbccddeeff",
		Status:     PlanStatusPlanned,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := store.Append(plan); err != nil {
		t.Fatal(err)
	}
	plan.Status = PlanStatusRunning
	plan.UpdatedAt = now.Add(time.Second)
	if err := store.Append(plan); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := reopened.Latest(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Status != PlanStatusRunning || !latest.UpdatedAt.Equal(plan.UpdatedAt) {
		t.Fatalf("latest plan = %#v", latest)
	}
	pending, err := reopened.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != plan.ID {
		t.Fatalf("pending plans = %#v", pending)
	}
	entries, err := os.ReadDir(filepath.Join(root, plan.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("event entries = %d, want 2", len(entries))
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			t.Fatalf("non-atomic event artifact remained: %s", entry.Name())
		}
	}
}

func TestMemoryStoreReturnsIndependentPlanCopies(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 6, 8, 0, 0, 0, time.UTC)
	plan := Plan{
		APIVersion: "agent.infernex.io/v1alpha1",
		Kind:       "InferNexExperiment",
		ID:         "ffeeddccbbaa99887766554433221100",
		Status:     PlanStatusRunning,
		CreatedAt:  now,
		UpdatedAt:  now,
		Stages:     []Stage{{CandidateName: "candidate", Status: StageStatusWaiting}},
	}
	store := NewMemoryStore()
	if err := store.Append(plan); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Latest(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Stages[0].Status = StageStatusPassed
	again, err := store.Latest(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Stages[0].Status != StageStatusWaiting {
		t.Fatalf("stored event was mutated through a returned copy: %#v", again.Stages[0])
	}
}
