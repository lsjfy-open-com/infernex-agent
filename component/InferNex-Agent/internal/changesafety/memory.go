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
	"fmt"
	"sort"
	"sync"
)

// MemoryStore is intended for unit tests. Production entry points always wire
// a FileStore so an Agent restart cannot erase an in-flight rollback.
type MemoryStore struct {
	mu      sync.Mutex
	records map[string][]ChangeRecord
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: make(map[string][]ChangeRecord)}
}

func (s *MemoryStore) Append(record ChangeRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateRecord(record); err != nil {
		return err
	}
	s.records[record.ID] = append(s.records[record.ID], record)
	return nil
}

func (s *MemoryStore) Latest(id string) (ChangeRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := s.records[id]
	if len(records) == 0 {
		return ChangeRecord{}, fmt.Errorf("change %s was not found", id)
	}
	return records[len(records)-1], nil
}

func (s *MemoryStore) Pending() ([]ChangeRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := make([]ChangeRecord, 0)
	for _, records := range s.records {
		if records[len(records)-1].Status == StatusPlanned ||
			records[len(records)-1].Status == StatusApplied {
			pending = append(pending, records[len(records)-1])
		}
	}
	sort.Slice(pending, func(left, right int) bool {
		return pending[left].OccurredAt.Before(pending[right].OccurredAt)
	})
	return pending, nil
}
