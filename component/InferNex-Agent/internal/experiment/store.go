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
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type MemoryStore struct {
	mu     sync.Mutex
	events map[string][]Plan
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{events: make(map[string][]Plan)}
}

func (s *MemoryStore) Append(plan Plan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validatePlan(plan); err != nil {
		return err
	}
	contents, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	var copy Plan
	if err := json.Unmarshal(contents, &copy); err != nil {
		return err
	}
	s.events[plan.ID] = append(s.events[plan.ID], copy)
	return nil
}

func (s *MemoryStore) Latest(id string) (Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events := s.events[id]
	if len(events) == 0 {
		return Plan{}, fmt.Errorf("experiment plan %s was not found", id)
	}
	return clonePlan(events[len(events)-1])
}

func (s *MemoryStore) Pending() ([]Plan, error) {
	plans, err := s.List()
	if err != nil {
		return nil, err
	}
	result := make([]Plan, 0)
	for _, plan := range plans {
		if plan.Status == PlanStatusPlanned || plan.Status == PlanStatusRunning {
			result = append(result, plan)
		}
	}
	return result, nil
}

func (s *MemoryStore) List() ([]Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Plan, 0, len(s.events))
	for _, events := range s.events {
		if len(events) > 0 {
			plan, err := clonePlan(events[len(events)-1])
			if err != nil {
				return nil, err
			}
			result = append(result, plan)
		}
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].CreatedAt.Before(result[right].CreatedAt)
	})
	return result, nil
}

type FileStore struct {
	root string
	mu   sync.Mutex
}

func NewFileStore(root string) (*FileStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("experiment store directory is required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create experiment store: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("protect experiment store: %w", err)
	}
	return &FileStore{root: root}, nil
}

func (s *FileStore) Append(plan Plan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validatePlan(plan); err != nil {
		return err
	}
	directory := filepath.Join(s.root, plan.ID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create experiment directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("protect experiment directory: %w", err)
	}
	contents, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("encode experiment plan: %w", err)
	}
	contents = append(contents, '\n')
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read experiment directory: %w", err)
	}
	sequence := 1
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		var existing int
		if _, scanErr := fmt.Sscanf(entry.Name(), "%d-", &existing); scanErr == nil && existing >= sequence {
			sequence = existing + 1
		}
	}
	temporary, err := os.CreateTemp(directory, ".experiment-event-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary experiment event: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect temporary experiment event: %w", err)
	}
	_, writeErr := temporary.Write(contents)
	if writeErr == nil {
		writeErr = temporary.Sync()
	}
	closeErr := temporary.Close()
	if writeErr != nil {
		return fmt.Errorf("write temporary experiment event: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close temporary experiment event: %w", closeErr)
	}
	for attempt := 0; attempt < 100; attempt++ {
		path := filepath.Join(directory, fmt.Sprintf("%020d-%s.json", sequence+attempt, plan.Status))
		if _, statErr := os.Lstat(path); statErr == nil {
			continue
		} else if !os.IsNotExist(statErr) {
			return fmt.Errorf("check experiment event path: %w", statErr)
		}
		if err := os.Rename(temporaryPath, path); err != nil {
			if os.IsExist(err) {
				continue
			}
			return fmt.Errorf("commit experiment event: %w", err)
		}
		return nil
	}
	return fmt.Errorf("create unique experiment event for %s", plan.ID)
}

func (s *FileStore) Latest(id string) (Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latestUnlocked(id)
}

func (s *FileStore) Pending() ([]Plan, error) {
	plans, err := s.List()
	if err != nil {
		return nil, err
	}
	result := make([]Plan, 0)
	for _, plan := range plans {
		if plan.Status == PlanStatusPlanned || plan.Status == PlanStatusRunning {
			result = append(result, plan)
		}
	}
	return result, nil
}

func (s *FileStore) List() ([]Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("read experiment store: %w", err)
	}
	result := make([]Plan, 0)
	for _, entry := range entries {
		if !entry.IsDir() || !validID(entry.Name()) {
			continue
		}
		plan, readErr := s.latestUnlocked(entry.Name())
		if readErr != nil {
			return nil, readErr
		}
		result = append(result, plan)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].CreatedAt.Before(result[right].CreatedAt)
	})
	return result, nil
}

func (s *FileStore) latestUnlocked(id string) (Plan, error) {
	if !validID(id) {
		return Plan{}, fmt.Errorf("invalid experiment id %q", id)
	}
	directory := filepath.Join(s.root, id)
	entries, err := os.ReadDir(directory)
	if err != nil {
		return Plan{}, fmt.Errorf("read experiment %s: %w", id, err)
	}
	var latest os.DirEntry
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if latest == nil || entry.Name() > latest.Name() {
			latest = entry
		}
	}
	if latest == nil {
		return Plan{}, fmt.Errorf("experiment %s contains no events", id)
	}
	contents, err := os.ReadFile(filepath.Join(directory, latest.Name()))
	if err != nil {
		return Plan{}, fmt.Errorf("read experiment event: %w", err)
	}
	var plan Plan
	if err := json.Unmarshal(contents, &plan); err != nil {
		return Plan{}, fmt.Errorf("decode experiment event: %w", err)
	}
	if err := validatePlan(plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func validatePlan(plan Plan) error {
	if !validID(plan.ID) {
		return fmt.Errorf("invalid experiment id %q", plan.ID)
	}
	if plan.APIVersion != "agent.infernex.io/v1alpha1" || plan.Kind != "InferNexExperiment" {
		return fmt.Errorf("unsupported experiment type %s %s", plan.APIVersion, plan.Kind)
	}
	switch plan.Status {
	case PlanStatusPlanned, PlanStatusRunning, PlanStatusCompleted, PlanStatusFailed:
	default:
		return fmt.Errorf("invalid experiment status %q", plan.Status)
	}
	if plan.CreatedAt.IsZero() || plan.UpdatedAt.IsZero() {
		return fmt.Errorf("experiment timestamps are required")
	}
	return nil
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func clonePlan(plan Plan) (Plan, error) {
	contents, err := json.Marshal(plan)
	if err != nil {
		return Plan{}, err
	}
	var cloned Plan
	if err := json.Unmarshal(contents, &cloned); err != nil {
		return Plan{}, err
	}
	return cloned, nil
}
