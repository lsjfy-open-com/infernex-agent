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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	StatusPlanned        = "planned"
	StatusApplied        = "applied"
	StatusCommitted      = "committed"
	StatusApplyFailed    = "apply-failed"
	StatusRolledBack     = "rolled-back"
	StatusRollbackFailed = "rollback-failed"
)

type Target struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
}

// ChangeRecord is an append-only description of one Agent mutation. Before is
// the exact source object before the mutation, or null when it did not exist.
type ChangeRecord struct {
	APIVersion string          `json:"apiVersion"`
	Kind       string          `json:"kind"`
	ID         string          `json:"id"`
	Action     string          `json:"action"`
	Status     string          `json:"status"`
	Target     Target          `json:"target"`
	Before     json.RawMessage `json:"before,omitempty"`
	Desired    json.RawMessage `json:"desired,omitempty"`
	OccurredAt time.Time       `json:"occurredAt"`
	Message    string          `json:"message,omitempty"`
}

// ChangeStatus is the non-sensitive view returned through MCP. Exact before
// and desired objects remain only in the protected local change journal.
type ChangeStatus struct {
	APIVersion string    `json:"apiVersion"`
	Kind       string    `json:"kind"`
	ID         string    `json:"id"`
	Action     string    `json:"action"`
	Status     string    `json:"status"`
	Target     Target    `json:"target"`
	OccurredAt time.Time `json:"occurredAt"`
	Message    string    `json:"message,omitempty"`
}

func PublicStatus(record ChangeRecord) ChangeStatus {
	return ChangeStatus{
		APIVersion: record.APIVersion,
		Kind:       record.Kind,
		ID:         record.ID,
		Action:     record.Action,
		Status:     record.Status,
		Target:     record.Target,
		OccurredAt: record.OccurredAt,
		Message:    record.Message,
	}
}

type Store interface {
	Append(ChangeRecord) error
	Latest(string) (ChangeRecord, error)
	Pending() ([]ChangeRecord, error)
}

type FileStore struct {
	root string
	mu   sync.Mutex
}

func NewFileStore(root string) (*FileStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("change store directory is required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create change store: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("protect change store: %w", err)
	}
	return &FileStore{root: root}, nil
}

func NewID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate change id: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func (s *FileStore) Append(record ChangeRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateRecord(record); err != nil {
		return err
	}
	changeDir := filepath.Join(s.root, record.ID)
	if err := os.MkdirAll(changeDir, 0o700); err != nil {
		return fmt.Errorf("create change directory: %w", err)
	}
	if err := os.Chmod(changeDir, 0o700); err != nil {
		return fmt.Errorf("protect change directory: %w", err)
	}
	contents, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode change record: %w", err)
	}
	contents = append(contents, '\n')
	nextSequence := 1
	existing, err := os.ReadDir(changeDir)
	if err != nil {
		return fmt.Errorf("read change directory: %w", err)
	}
	for _, entry := range existing {
		if entry.IsDir() {
			continue
		}
		var sequence int
		if _, scanErr := fmt.Sscanf(entry.Name(), "%d-", &sequence); scanErr == nil &&
			sequence >= nextSequence {
			nextSequence = sequence + 1
		}
	}
	for attempt := 0; attempt < 100; attempt++ {
		name := fmt.Sprintf(
			"%020d-%s.json",
			nextSequence+attempt,
			record.Status,
		)
		path := filepath.Join(changeDir, name)
		file, openErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(openErr, os.ErrExist) {
			continue
		}
		if openErr != nil {
			return fmt.Errorf("create change event: %w", openErr)
		}
		if _, err = file.Write(contents); err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			return fmt.Errorf("write change event: %w", err)
		}
		if closeErr != nil {
			return fmt.Errorf("close change event: %w", closeErr)
		}
		return nil
	}
	return fmt.Errorf("create unique change event for %s", record.ID)
}

func (s *FileStore) Latest(id string) (ChangeRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latestUnlocked(id)
}

func (s *FileStore) Pending() ([]ChangeRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("read change store: %w", err)
	}
	records := make([]ChangeRecord, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		record, readErr := s.latestUnlocked(entry.Name())
		if readErr != nil {
			return nil, readErr
		}
		if record.Status == StatusPlanned || record.Status == StatusApplied {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(left, right int) bool {
		return records[left].OccurredAt.Before(records[right].OccurredAt)
	})
	return records, nil
}

func (s *FileStore) latestUnlocked(id string) (ChangeRecord, error) {
	if !validID(id) {
		return ChangeRecord{}, fmt.Errorf("invalid change id %q", id)
	}
	changeDir := filepath.Join(s.root, id)
	entries, err := os.ReadDir(changeDir)
	if err != nil {
		return ChangeRecord{}, fmt.Errorf("read change %s: %w", id, err)
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
		return ChangeRecord{}, fmt.Errorf("change %s contains no events", id)
	}
	contents, err := os.ReadFile(filepath.Join(changeDir, latest.Name()))
	if err != nil {
		return ChangeRecord{}, fmt.Errorf("read change event: %w", err)
	}
	var record ChangeRecord
	if err := json.Unmarshal(contents, &record); err != nil {
		return ChangeRecord{}, fmt.Errorf("decode change event: %w", err)
	}
	if err := validateRecord(record); err != nil {
		return ChangeRecord{}, err
	}
	return record, nil
}

func validateRecord(record ChangeRecord) error {
	if !validID(record.ID) {
		return fmt.Errorf("invalid change id %q", record.ID)
	}
	if record.APIVersion != "agent.infernex.io/v1alpha1" ||
		record.Kind != "InferNexChange" {
		return fmt.Errorf("unsupported change record type %s %s", record.APIVersion, record.Kind)
	}
	switch record.Status {
	case StatusPlanned, StatusApplied, StatusCommitted, StatusApplyFailed,
		StatusRolledBack, StatusRollbackFailed:
	default:
		return fmt.Errorf("invalid change status %q", record.Status)
	}
	if record.OccurredAt.IsZero() {
		return fmt.Errorf("change event time is required")
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
