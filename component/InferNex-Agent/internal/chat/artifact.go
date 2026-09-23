/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package chat

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	artifactReadToolName       = "infernex_read_artifact"
	defaultArtifactSessionSize = int64(128 * 1024 * 1024)
	defaultArtifactReadLines   = 120
	maxArtifactReadLines       = 500
	maxArtifactReadBytes       = 64 * 1024
)

type ArtifactConfig struct {
	Directory       string
	MaxSessionBytes int64
}

type artifactRecord struct {
	ID     string
	SHA256 string
	Path   string
	Bytes  int64
	Lines  int
}

type artifactStore struct {
	mu              sync.RWMutex
	directory       string
	maxSessionBytes int64
	usedBytes       int64
	records         map[string]artifactRecord
}

func newArtifactStore(config ArtifactConfig) (*artifactStore, error) {
	root := strings.TrimSpace(config.Directory)
	if root == "" {
		return nil, nil
	}
	limit := config.MaxSessionBytes
	if limit <= 0 {
		limit = defaultArtifactSessionSize
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return nil, fmt.Errorf("create artifact session identifier: %w", err)
	}
	session := time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(suffix)
	directory := filepath.Join(root, session)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create chat artifact directory %s: %w", directory, err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, fmt.Errorf("protect chat artifact directory %s: %w", directory, err)
	}
	return &artifactStore{
		directory: directory, maxSessionBytes: limit, records: map[string]artifactRecord{},
	}, nil
}

func artifactToolDefinition() ToolDefinition {
	return ToolDefinition{
		Name: artifactReadToolName,
		Description: "Read a bounded line range from a large tool result saved by this chat. " +
			"Use the artifact_id returned in a tool result; paths are never accepted.",
		ReadOnly: true,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"artifact_id": map[string]any{"type": "string"},
				"start_line":  map[string]any{"type": "integer", "minimum": 1},
				"max_lines":   map[string]any{"type": "integer", "minimum": 1, "maximum": maxArtifactReadLines},
				"contains": map[string]any{
					"type": "string", "description": "Optional case-insensitive literal filter",
				},
			},
			"required":             []string{"artifact_id"},
			"additionalProperties": false,
		},
	}
}

func (s *artifactStore) put(content string) (artifactRecord, error) {
	payload := []byte(content)
	digest := sha256.Sum256(payload)
	hexDigest := hex.EncodeToString(digest[:])
	id := "sha256:" + hexDigest

	s.mu.Lock()
	defer s.mu.Unlock()
	if record, ok := s.records[id]; ok {
		return record, nil
	}
	if int64(len(payload)) > s.maxSessionBytes-s.usedBytes {
		return artifactRecord{}, fmt.Errorf(
			"artifact session limit exceeded: used=%d new=%d limit=%d bytes",
			s.usedBytes, len(payload), s.maxSessionBytes,
		)
	}
	path := filepath.Join(s.directory, hexDigest+".log")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return artifactRecord{}, fmt.Errorf("write chat artifact: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return artifactRecord{}, fmt.Errorf("protect chat artifact: %w", err)
	}
	lines := bytes.Count(payload, []byte{'\n'})
	if len(payload) > 0 && payload[len(payload)-1] != '\n' {
		lines++
	}
	record := artifactRecord{
		ID: id, SHA256: hexDigest, Path: path, Bytes: int64(len(payload)), Lines: lines,
	}
	s.records[id] = record
	s.usedBytes += int64(len(payload))
	return record, nil
}

func (s *artifactStore) read(arguments map[string]any) (string, error) {
	id, ok := arguments["artifact_id"].(string)
	id = strings.TrimSpace(id)
	if !ok || id == "" {
		return "", fmt.Errorf("artifact_id is required")
	}
	startLine, err := artifactInteger(arguments, "start_line", 1, 1, int(^uint(0)>>1))
	if err != nil {
		return "", err
	}
	maxLines, err := artifactInteger(
		arguments, "max_lines", defaultArtifactReadLines, 1, maxArtifactReadLines,
	)
	if err != nil {
		return "", err
	}
	contains, _ := arguments["contains"].(string)
	contains = strings.ToLower(strings.TrimSpace(contains))

	s.mu.RLock()
	record, found := s.records[id]
	s.mu.RUnlock()
	if !found {
		return "", fmt.Errorf("unknown artifact_id; only artifacts from this chat may be read")
	}
	file, err := os.Open(record.Path)
	if err != nil {
		return "", fmt.Errorf("open chat artifact: %w", err)
	}
	defer file.Close()

	var output strings.Builder
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxArtifactReadBytes)
	lineNumber := 0
	selected := 0
	truncated := false
	for scanner.Scan() {
		lineNumber++
		if lineNumber < startLine {
			continue
		}
		line := scanner.Text()
		if contains != "" && !strings.Contains(strings.ToLower(line), contains) {
			continue
		}
		candidate := fmt.Sprintf("%d: %s\n", lineNumber, line)
		if output.Len()+len(candidate) > maxArtifactReadBytes {
			truncated = true
			break
		}
		output.WriteString(candidate)
		selected++
		if selected >= maxLines {
			truncated = lineNumber < record.Lines
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan chat artifact: %w", err)
	}
	result := map[string]any{
		"artifact_id": id, "sha256": record.SHA256, "total_lines": record.Lines,
		"start_line": startLine, "returned_lines": selected, "content": output.String(),
		"truncated": truncated,
	}
	encoded, _ := json.Marshal(result)
	return string(encoded), nil
}

func artifactInteger(
	arguments map[string]any, name string, defaultValue, minimum, maximum int,
) (int, error) {
	value, exists := arguments[name]
	if !exists {
		return defaultValue, nil
	}
	number, ok := value.(float64)
	if !ok || math.Trunc(number) != number || number < float64(minimum) || number > float64(maximum) {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", name, minimum, maximum)
	}
	return int(number), nil
}
