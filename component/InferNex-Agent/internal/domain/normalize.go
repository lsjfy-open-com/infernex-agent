package domain

import (
	"encoding/json"
	"fmt"
	"sort"
)

// NormalizeSnapshot is only for fresh collector output. It rejects a snapshot
// that already has a digest so verification never repairs signed bytes.
func NormalizeSnapshot(s *InventorySnapshot) error {
	if s == nil {
		return fmt.Errorf("snapshot is nil")
	}
	if s.Digest != "" {
		return fmt.Errorf("signed snapshot cannot be normalized")
	}
	if len(s.Spec.Entities) > MaxEntities || len(s.Spec.Relations) > MaxRelations || len(s.Spec.Issues) > MaxIssues {
		return fmt.Errorf("snapshot collection count exceeds limit")
	}
	sort.Slice(s.Spec.Entities, func(i, j int) bool { return s.Spec.Entities[i].EntityID < s.Spec.Entities[j].EntityID })
	for i := range s.Spec.Entities {
		if i > 0 && s.Spec.Entities[i-1].EntityID == s.Spec.Entities[i].EntityID {
			return fmt.Errorf("duplicate entityId")
		}
		facts, err := normalizeFacts(s.Spec.Entities[i].Facts)
		if err != nil {
			return fmt.Errorf("entity facts: %w", err)
		}
		s.Spec.Entities[i].Facts = facts
	}
	sort.Slice(s.Spec.Relations, func(i, j int) bool {
		a, b := s.Spec.Relations[i], s.Spec.Relations[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.FromEntityID != b.FromEntityID {
			return a.FromEntityID < b.FromEntityID
		}
		return a.ToEntityID < b.ToEntityID
	})
	for i := 1; i < len(s.Spec.Relations); i++ {
		a, b := s.Spec.Relations[i-1], s.Spec.Relations[i]
		if a.Type == b.Type && a.FromEntityID == b.FromEntityID && a.ToEntityID == b.ToEntityID {
			return fmt.Errorf("duplicate relation key")
		}
	}
	sort.Slice(s.Spec.Coverage, func(i, j int) bool {
		a, b := s.Spec.Coverage[i], s.Spec.Coverage[j]
		if a.ResourceKind != b.ResourceKind {
			return a.ResourceKind < b.ResourceKind
		}
		return a.Namespace < b.Namespace
	})
	for i := 1; i < len(s.Spec.Coverage); i++ {
		a, b := s.Spec.Coverage[i-1], s.Spec.Coverage[i]
		if a.ResourceKind == b.ResourceKind && a.Namespace == b.Namespace {
			return fmt.Errorf("duplicate coverage key")
		}
	}
	type issueWithKey struct {
		issue Issue
		key   string
	}
	items := make([]issueWithKey, 0, len(s.Spec.Issues))
	seen := map[string]bool{}
	for _, issue := range s.Spec.Issues {
		k, err := canonicalValue(issue)
		if err != nil {
			return err
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		items = append(items, issueWithKey{issue, k})
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i].issue, items[j].issue
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.Stage != b.Stage {
			return a.Stage < b.Stage
		}
		ak, _ := canonicalValue(a.ObjectRef)
		bk, _ := canonicalValue(b.ObjectRef)
		if ak != bk {
			return ak < bk
		}
		return a.Description < b.Description
	})
	s.Spec.Issues = s.Spec.Issues[:0]
	for _, item := range items {
		s.Spec.Issues = append(s.Spec.Issues, item.issue)
	}
	return nil
}

func normalizeFacts(facts []Fact) ([]Fact, error) {
	type keyed struct {
		fact              Fact
		base, value, full string
	}
	items := make([]keyed, 0, len(facts))
	seen := map[string]bool{}
	values := map[string]map[string]bool{}
	for _, f := range facts {
		src, err := canonicalValue(f.Source)
		if err != nil {
			return nil, err
		}
		value, err := canonicalValue(f.Value)
		if err != nil {
			return nil, err
		}
		full, err := canonicalValue(f)
		if err != nil {
			return nil, err
		}
		if seen[full] {
			continue
		}
		seen[full] = true
		base := f.Field + "\x00" + src
		items = append(items, keyed{f, base, value, full})
		if values[base] == nil {
			values[base] = map[string]bool{}
		}
		values[base][value] = true
	}
	for i := range items {
		if len(values[items[i].base]) > 1 {
			items[i].fact.Status = StatusConflict
		}
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.base != b.base {
			return a.base < b.base
		}
		if a.fact.Status != b.fact.Status {
			return a.fact.Status < b.fact.Status
		}
		if a.value != b.value {
			return a.value < b.value
		}
		return a.full < b.full
	})
	out := make([]Fact, len(items))
	for i := range items {
		out[i] = items[i].fact
	}
	return out, nil
}

func canonicalValue(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	b, err := CanonicalizeJSON(raw, CanonicalOptions{})
	if err != nil {
		return "", err
	}
	return string(b), nil
}
