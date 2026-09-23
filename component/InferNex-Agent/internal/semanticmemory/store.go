/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package semanticmemory

import (
	"crypto/rand"
	"crypto/sha256"
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
	"unicode"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/namedfiles"
)

const schemaVersion = "agent.infernex.io/v1alpha1"

var allowedKinds = map[string]bool{
	"fact": true, "decision": true, "preference": true, "procedure": true,
	"incident": true, "configuration-baseline": true,
}

var allowedSources = map[string]bool{
	"user-confirmed": true, "tool-verified": true, "operator-authored": true,
}

type Record struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Path       string     `json:"path,omitempty"`
	ClusterID  string     `json:"clusterId,omitempty"`
	Scope      string     `json:"scope"`
	Type       string     `json:"type"`
	Subject    string     `json:"subject"`
	Summary    string     `json:"summary"`
	Tags       []string   `json:"tags,omitempty"`
	Evidence   []string   `json:"evidenceIds,omitempty"`
	Source     string     `json:"source"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	DeletedAt  *time.Time `json:"deletedAt,omitempty"`
}

type PutRequest struct {
	Scope     string
	Type      string
	Subject   string
	Summary   string
	Tags      []string
	Evidence  []string
	Source    string
	ExpiresAt *time.Time
}

type SearchRequest struct {
	Query string
	Types []string
	Limit int
}

type SearchResult struct {
	Query   string   `json:"query"`
	Records []Record `json:"records"`
}

type Store interface {
	Put(PutRequest) (Record, error)
	Search(SearchRequest) (SearchResult, error)
	Forget(string) (Record, error)
}

type FileStore struct {
	root      string
	clusterID string
	mu        sync.Mutex
}

func NewFileStore(root, clusterID string) (*FileStore, error) {
	root = strings.TrimSpace(root)
	clusterID = strings.TrimSpace(clusterID)
	if root == "" || clusterID == "" {
		return nil, fmt.Errorf("memory directory and cluster identity are required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create semantic memory directory: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("protect semantic memory directory: %w", err)
	}
	return &FileStore{root: root, clusterID: clusterID}, nil
}

func (s *FileStore) Put(request PutRequest) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request.Scope = strings.ToLower(strings.TrimSpace(request.Scope))
	request.Type = strings.ToLower(strings.TrimSpace(request.Type))
	request.Subject = bounded(strings.TrimSpace(request.Subject), 256)
	request.Summary = bounded(strings.TrimSpace(request.Summary), 4096)
	request.Source = strings.ToLower(strings.TrimSpace(request.Source))
	if request.Scope != "cluster" && request.Scope != "global" {
		return Record{}, fmt.Errorf("memory scope must be cluster or global")
	}
	if !allowedKinds[request.Type] {
		return Record{}, fmt.Errorf("unsupported memory type %q", request.Type)
	}
	if !allowedSources[request.Source] {
		return Record{}, fmt.Errorf("memory source must be user-confirmed, tool-verified, or operator-authored")
	}
	if request.Subject == "" || request.Summary == "" {
		return Record{}, fmt.Errorf("memory subject and summary are required")
	}
	if request.ExpiresAt != nil && !request.ExpiresAt.After(time.Now().UTC()) {
		return Record{}, fmt.Errorf("memory expiry must be in the future")
	}
	id, err := newID()
	if err != nil {
		return Record{}, err
	}
	record := Record{
		APIVersion: schemaVersion, Kind: "InferNexSemanticMemory", ID: id,
		Scope: request.Scope, Type: request.Type, Subject: request.Subject,
		Summary: request.Summary, Tags: normalized(request.Tags, 32, 64),
		Evidence: normalized(request.Evidence, 64, 256), Source: request.Source,
		CreatedAt: time.Now().UTC(), ExpiresAt: request.ExpiresAt,
	}
	if record.Scope == "cluster" {
		record.ClusterID = s.clusterID
	}
	record.Name = namedfiles.Name(record.Subject, record.CreatedAt)
	contents, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return Record{}, err
	}
	record.Path, err = namedfiles.Create(s.root, record.Name, ".json", record.ID, append(contents, '\n'))
	if err != nil {
		return Record{}, err
	}
	actualName := strings.TrimSuffix(filepath.Base(record.Path), ".json")
	if actualName != record.Name {
		record.Name = actualName
		if err := s.write(record); err != nil {
			return Record{}, err
		}
	}
	return record, nil
}

func (s *FileStore) Search(request SearchRequest) (SearchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	query := bounded(strings.TrimSpace(request.Query), 1024)
	limit := request.Limit
	if limit == 0 {
		limit = 10
	}
	if limit < 1 || limit > 50 {
		return SearchResult{}, fmt.Errorf("memory result limit must be between 1 and 50")
	}
	types := map[string]bool{}
	for _, value := range request.Types {
		value = strings.ToLower(strings.TrimSpace(value))
		if !allowedKinds[value] {
			return SearchResult{}, fmt.Errorf("unsupported memory type %q", value)
		}
		types[value] = true
	}
	var paths []string
	if validID(query) {
		filename, err := s.lookup(query)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return SearchResult{}, err
		}
		if err == nil {
			paths = append(paths, filename)
		}
	} else {
		entries, err := os.ReadDir(s.root)
		if err != nil {
			return SearchResult{}, fmt.Errorf("read semantic memory directory: %w", err)
		}
		for _, entry := range entries {
			if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
				paths = append(paths, filepath.Join(s.root, entry.Name()))
			}
		}
	}
	type ranked struct {
		record Record
		score  int
	}
	now := time.Now().UTC()
	rankedRecords := make([]ranked, 0)
	for _, filename := range paths {
		record, readErr := s.read(filename)
		if readErr != nil {
			return SearchResult{}, readErr
		}
		if validID(query) && record.ID != query {
			return SearchResult{}, fmt.Errorf("memory index id mismatch")
		}
		if record.DeletedAt != nil || record.ExpiresAt != nil && !record.ExpiresAt.After(now) {
			continue
		}
		if record.Scope == "cluster" && record.ClusterID != s.clusterID {
			continue
		}
		if len(types) > 0 && !types[record.Type] {
			continue
		}
		score := relevance(query, record)
		if query != "" && score == 0 {
			continue
		}
		rankedRecords = append(rankedRecords, ranked{record: record, score: score})
	}
	sort.Slice(rankedRecords, func(i, j int) bool {
		if rankedRecords[i].score != rankedRecords[j].score {
			return rankedRecords[i].score > rankedRecords[j].score
		}
		return rankedRecords[i].record.CreatedAt.After(rankedRecords[j].record.CreatedAt)
	})
	if len(rankedRecords) > limit {
		rankedRecords = rankedRecords[:limit]
	}
	result := SearchResult{Query: query, Records: make([]Record, 0, len(rankedRecords))}
	for _, item := range rankedRecords {
		result.Records = append(result.Records, item.record)
	}
	return result, nil
}

func (s *FileStore) Forget(id string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validID(id) {
		return Record{}, fmt.Errorf("invalid memory id %q", id)
	}
	path, err := s.lookup(id)
	if err != nil {
		return Record{}, err
	}
	record, err := s.read(path)
	if err == nil && record.ID != id {
		return Record{}, fmt.Errorf("memory index id mismatch")
	}
	if err != nil {
		return Record{}, err
	}
	if record.Scope == "cluster" && record.ClusterID != s.clusterID {
		return Record{}, fmt.Errorf("memory %s belongs to another cluster", id)
	}
	if record.DeletedAt == nil {
		now := time.Now().UTC()
		record.DeletedAt = &now
		if err := s.write(record); err != nil {
			return Record{}, err
		}
	}
	return record, nil
}

func (s *FileStore) lookup(id string) (string, error) {
	filename, err := namedfiles.Lookup(s.root, id)
	if err == nil || !os.IsNotExist(err) {
		return filename, err
	}
	// Old records and a missing/rebuildable index remain readable after upgrade.
	legacy := filepath.Join(s.root, id+".json")
	if _, err := os.Lstat(legacy); err == nil {
		return legacy, nil
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		filename := filepath.Join(s.root, entry.Name())
		record, err := s.read(filename)
		if err != nil {
			return "", err
		}
		if record.ID == id {
			if err := namedfiles.Index(s.root, id, entry.Name()); err != nil {
				return "", err
			}
			return filename, nil
		}
	}
	return "", fmt.Errorf("memory %s: %w", id, os.ErrNotExist)
}

func (s *FileStore) write(record Record) error {
	target, err := s.lookup(record.ID)
	if err != nil {
		return err
	}
	record.Path = "" // Stored records remain relocatable.
	contents, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return namedfiles.Replace(target, append(contents, '\n'))
}

func (s *FileStore) read(path string) (Record, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Record{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 128*1024 {
		return Record{}, fmt.Errorf("memory must be a bounded regular file")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return Record{}, fmt.Errorf("read semantic memory: %w", err)
	}
	var record Record
	if err := json.Unmarshal(contents, &record); err != nil {
		return Record{}, fmt.Errorf("decode semantic memory: %w", err)
	}
	if record.APIVersion != schemaVersion || record.Kind != "InferNexSemanticMemory" || !validID(record.ID) {
		return Record{}, fmt.Errorf("invalid semantic memory record %s", path)
	}
	record.Path = path
	record.Name = namedfiles.Name(record.Subject, record.CreatedAt)
	if filepath.Base(path) != record.ID+".json" {
		record.Name = strings.TrimSuffix(filepath.Base(path), ".json")
	}
	return record, nil
}

func relevance(query string, record Record) int {
	if query == "" || query == record.ID {
		return 1
	}
	q := terms(query)
	fields := []struct {
		value  string
		weight int
	}{{record.Name, 8}, {record.Subject, 8}, {strings.Join(record.Tags, " "), 5}, {record.Summary, 3}, {record.Type, 2}}
	score := 0
	for _, field := range fields {
		candidate := terms(field.value)
		for term := range q {
			if candidate[term] {
				score += field.weight
			}
		}
	}
	return score
}

func terms(value string) map[string]bool {
	value = strings.ToLower(strings.TrimSpace(value))
	result := map[string]bool{}
	for _, field := range strings.FieldsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) }) {
		runes := []rune(field)
		if len(runes) <= 2 {
			result[field] = true
			continue
		}
		result[field] = true
		for index := 0; index+1 < len(runes); index++ {
			result[string(runes[index:index+2])] = true
		}
	}
	return result
}

func normalized(values []string, limit, maxRunes int) []string {
	unique := map[string]bool{}
	result := make([]string, 0, min(len(values), limit))
	for _, value := range values {
		value = bounded(strings.TrimSpace(value), maxRunes)
		if value == "" || unique[value] {
			continue
		}
		unique[value] = true
		result = append(result, value)
		if len(result) == limit {
			break
		}
	}
	sort.Strings(result)
	return result
}

func bounded(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func newID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate memory id: %w", err)
	}
	digest := sha256.Sum256(value[:])
	return hex.EncodeToString(digest[:]), nil
}

func validID(id string) bool {
	if len(id) != 32 && len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
