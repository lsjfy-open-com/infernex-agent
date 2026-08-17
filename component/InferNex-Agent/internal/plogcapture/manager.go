/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package plogcapture

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/labels"
)

const (
	defaultCaptureBytes    int64 = 1024 * 1024 * 1024
	maximumCaptureBytes    int64 = 100 * 1024 * 1024 * 1024
	defaultCaptureDuration       = time.Hour
	maximumCaptureDuration       = 7 * 24 * time.Hour
	defaultPollInterval          = 10 * time.Second
	maxTargetsPerPoll            = 20
	maxFilesPerPoll              = 200
)

type StartRequest struct {
	Namespace       string `json:"namespace"`
	LabelSelector   string `json:"labelSelector"`
	Container       string `json:"container,omitempty"`
	MaxBytes        int64  `json:"maxBytes,omitempty"`
	DurationMinutes int    `json:"durationMinutes,omitempty"`
	Confirm         bool   `json:"confirm"`
}

type Task struct {
	ID            string            `json:"id"`
	Namespace     string            `json:"namespace"`
	LabelSelector string            `json:"labelSelector"`
	Container     string            `json:"container,omitempty"`
	Status        string            `json:"status"`
	CreatedAt     time.Time         `json:"createdAt"`
	UpdatedAt     time.Time         `json:"updatedAt"`
	Deadline      time.Time         `json:"deadline"`
	StoppedAt     *time.Time        `json:"stoppedAt,omitempty"`
	MaxBytes      int64             `json:"maxBytes"`
	CapturedBytes int64             `json:"capturedBytes"`
	Segments      int               `json:"segments"`
	LastError     string            `json:"lastError,omitempty"`
	EvidenceRoot  string            `json:"evidenceRoot"`
	Offsets       map[string]int64  `json:"offsets,omitempty"`
	SegmentFiles  map[string]string `json:"segmentFiles,omitempty"`
}

type TaskList struct {
	Tasks []Task `json:"tasks"`
}

type Manager struct {
	mu           sync.Mutex
	source       Source
	stateDir     string
	evidenceDir  string
	pollInterval time.Duration
	tasks        map[string]*Task
	ctx          context.Context
}

func NewManager(source Source, stateDir, evidenceDir string) (*Manager, error) {
	if source == nil {
		return nil, fmt.Errorf("plog source is required")
	}
	stateDir, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, fmt.Errorf("resolve plog state directory: %w", err)
	}
	evidenceDir, err = filepath.Abs(evidenceDir)
	if err != nil {
		return nil, fmt.Errorf("resolve plog evidence directory: %w", err)
	}
	for _, directory := range []string{stateDir, evidenceDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("create plog directory: %w", err)
		}
	}
	manager := &Manager{source: source, stateDir: stateDir, evidenceDir: evidenceDir, pollInterval: defaultPollInterval, tasks: map[string]*Task{}}
	if err := manager.load(); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *Manager) StartBackground(ctx context.Context) {
	m.mu.Lock()
	m.ctx = ctx
	ids := []string{}
	for id, task := range m.tasks {
		if task.Status == "running" {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	for _, id := range ids {
		go m.run(ctx, id)
	}
}

func (m *Manager) Create(request StartRequest) (Task, error) {
	if !request.Confirm {
		return Task{}, fmt.Errorf("confirm must be true after approving target, duration, and storage budget")
	}
	namespace := strings.TrimSpace(request.Namespace)
	selector := strings.TrimSpace(request.LabelSelector)
	if namespace == "" || selector == "" {
		return Task{}, fmt.Errorf("namespace and a non-empty labelSelector are required")
	}
	if _, err := labels.Parse(selector); err != nil {
		return Task{}, fmt.Errorf("invalid labelSelector: %w", err)
	}
	maxBytes := request.MaxBytes
	if maxBytes == 0 {
		maxBytes = defaultCaptureBytes
	}
	if maxBytes < 1024*1024 || maxBytes > maximumCaptureBytes {
		return Task{}, fmt.Errorf("maxBytes must be between 1 MiB and 100 GiB")
	}
	duration := time.Duration(request.DurationMinutes) * time.Minute
	if duration == 0 {
		duration = defaultCaptureDuration
	}
	if duration < time.Minute || duration > maximumCaptureDuration {
		return Task{}, fmt.Errorf("durationMinutes must be between 1 and 10080")
	}
	now := time.Now().UTC()
	task := &Task{ID: newTaskID(), Namespace: namespace, LabelSelector: selector, Container: strings.TrimSpace(request.Container), Status: "running", CreatedAt: now, UpdatedAt: now, Deadline: now.Add(duration), MaxBytes: maxBytes, EvidenceRoot: m.evidenceDir, Offsets: map[string]int64{}, SegmentFiles: map[string]string{}}
	m.mu.Lock()
	for _, existing := range m.tasks {
		if existing.Status == "running" && existing.Namespace == task.Namespace && existing.LabelSelector == task.LabelSelector && existing.Container == task.Container {
			m.mu.Unlock()
			return Task{}, fmt.Errorf("an equivalent plog capture task is already running: %s", existing.ID)
		}
	}
	m.tasks[task.ID] = task
	ctx := m.ctx
	snapshot := cloneTask(task)
	m.mu.Unlock()
	if err := m.persist(snapshot); err != nil {
		m.mu.Lock()
		delete(m.tasks, task.ID)
		m.mu.Unlock()
		return Task{}, err
	}
	if ctx != nil {
		go m.run(ctx, task.ID)
	}
	return snapshot, nil
}

func (m *Manager) Stop(id string, confirm bool) (Task, error) {
	if !confirm {
		return Task{}, fmt.Errorf("confirm must be true after reviewing the capture task")
	}
	m.mu.Lock()
	task, ok := m.tasks[strings.TrimSpace(id)]
	if !ok {
		m.mu.Unlock()
		return Task{}, fmt.Errorf("plog capture task not found")
	}
	if task.Status == "running" {
		now := time.Now().UTC()
		task.Status, task.StoppedAt, task.UpdatedAt = "stopped", &now, now
	}
	snapshot := cloneTask(task)
	m.mu.Unlock()
	return snapshot, m.persist(snapshot)
}

func (m *Manager) Get(id string) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[strings.TrimSpace(id)]
	if !ok {
		return Task{}, fmt.Errorf("plog capture task not found")
	}
	return cloneTask(task), nil
}

func (m *Manager) List() TaskList {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := TaskList{Tasks: make([]Task, 0, len(m.tasks))}
	for _, task := range m.tasks {
		result.Tasks = append(result.Tasks, cloneTask(task))
	}
	sort.Slice(result.Tasks, func(i, j int) bool { return result.Tasks[i].CreatedAt.After(result.Tasks[j].CreatedAt) })
	return result
}

func (m *Manager) run(ctx context.Context, id string) {
	m.poll(ctx, id)
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !m.poll(ctx, id) {
				return
			}
		}
	}
}

func (m *Manager) poll(ctx context.Context, id string) bool {
	m.mu.Lock()
	task, ok := m.tasks[id]
	if !ok || task.Status != "running" {
		m.mu.Unlock()
		return false
	}
	if !time.Now().UTC().Before(task.Deadline) {
		now := time.Now().UTC()
		task.Status, task.StoppedAt, task.UpdatedAt = "completed", &now, now
		snapshot := cloneTask(task)
		m.mu.Unlock()
		_ = m.persist(snapshot)
		return false
	}
	namespace, selector, container := task.Namespace, task.LabelSelector, task.Container
	m.mu.Unlock()

	targets, err := m.source.ListTargets(ctx, namespace, selector, container)
	if err != nil {
		m.recordError(id, err)
		return true
	}
	if len(targets) == 0 {
		m.recordError(id, fmt.Errorf("no matching Running Pod/container is currently visible"))
		return true
	}
	observedFiles, processedFiles := false, 0
	if len(targets) > maxTargetsPerPoll {
		targets = targets[:maxTargetsPerPoll]
	}
captureTargets:
	for _, target := range targets {
		files, err := m.source.ListFiles(ctx, target)
		if err != nil {
			m.recordError(id, err)
			continue
		}
		for _, sourcePath := range files {
			if processedFiles >= maxFilesPerPoll {
				break captureTargets
			}
			processedFiles++
			observedFiles = true
			if !m.captureFile(ctx, id, target, sourcePath) {
				return false
			}
		}
	}
	if !observedFiles {
		m.recordError(id, fmt.Errorf("matching containers expose no files under the fixed CANN plog roots"))
		return true
	}
	m.mu.Lock()
	if current, exists := m.tasks[id]; exists {
		current.LastError = ""
		current.UpdatedAt = time.Now().UTC()
		taskSnapshot := cloneTask(current)
		m.mu.Unlock()
		_ = m.persist(taskSnapshot)
	} else {
		m.mu.Unlock()
	}
	return true
}

func (m *Manager) captureFile(ctx context.Context, id string, target PodContainer, sourcePath string) bool {
	key := target.UID + "\x00" + target.Container + "\x00" + sourcePath
	m.mu.Lock()
	task := m.tasks[id]
	offset := task.Offsets[key]
	remaining := task.MaxBytes - task.CapturedBytes
	m.mu.Unlock()
	if remaining <= 0 {
		m.finishCapacity(id)
		return false
	}
	size, err := m.source.FileSize(ctx, target, sourcePath)
	if err != nil {
		m.recordError(id, err)
		return true
	}
	rotated := size < offset
	if rotated {
		offset = 0
	}
	if size <= offset {
		return true
	}
	limit := size - offset
	if limit > maxChunkBytes {
		limit = maxChunkBytes
	}
	if limit > remaining {
		limit = remaining
	}
	chunk, err := m.source.ReadChunk(ctx, target, sourcePath, offset, limit)
	if err != nil {
		m.recordError(id, err)
		return true
	}
	if len(chunk) == 0 {
		return true
	}
	destination, newSegment, err := m.segmentPath(id, target, sourcePath)
	if err != nil {
		m.recordError(id, err)
		return true
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		m.recordError(id, err)
		return true
	}
	if rotated {
		_, _ = file.WriteString("\n--- infernex-agent: source file truncated or rotated; offset reset ---\n")
	}
	_, writeErr := file.Write(chunk)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		m.recordError(id, fmt.Errorf("append plog evidence: %v %v", writeErr, closeErr))
		return true
	}
	m.mu.Lock()
	task = m.tasks[id]
	task.Offsets[key] = offset + int64(len(chunk))
	task.SegmentFiles[key] = destination
	task.CapturedBytes += int64(len(chunk))
	if newSegment {
		task.Segments++
	}
	task.UpdatedAt = time.Now().UTC()
	reached := task.CapturedBytes >= task.MaxBytes
	m.mu.Unlock()
	if reached {
		m.finishCapacity(id)
		return false
	}
	return true
}

func (m *Manager) segmentPath(id string, target PodContainer, sourcePath string) (string, bool, error) {
	digest := sha256.Sum256([]byte(sourcePath))
	directory := filepath.Join(m.evidenceDir, safeName(id), safeName(target.UID), safeName(target.Container))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", false, err
	}
	destination := filepath.Join(directory, hex.EncodeToString(digest[:8])+".plog")
	_, err := os.Stat(destination)
	return destination, os.IsNotExist(err), nil
}

func (m *Manager) finishCapacity(id string) {
	m.mu.Lock()
	if task, ok := m.tasks[id]; ok && task.Status == "running" {
		now := time.Now().UTC()
		task.Status, task.StoppedAt, task.UpdatedAt = "capacity-reached", &now, now
		snapshot := cloneTask(task)
		m.mu.Unlock()
		_ = m.persist(snapshot)
		return
	}
	m.mu.Unlock()
}

func (m *Manager) recordError(id string, err error) {
	m.mu.Lock()
	if task, ok := m.tasks[id]; ok {
		task.LastError = boundedError(err)
		task.UpdatedAt = time.Now().UTC()
		snapshot := cloneTask(task)
		m.mu.Unlock()
		_ = m.persist(snapshot)
		return
	}
	m.mu.Unlock()
}

func (m *Manager) persist(task Task) error {
	contents, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return err
	}
	target := filepath.Join(m.stateDir, safeName(task.ID)+".json")
	temporary := target + ".tmp"
	if err := os.WriteFile(temporary, append(contents, '\n'), 0o600); err != nil {
		return fmt.Errorf("write plog task: %w", err)
	}
	if err := os.Rename(temporary, target); err != nil {
		return fmt.Errorf("commit plog task: %w", err)
	}
	return nil
}

func (m *Manager) load() error {
	entries, err := os.ReadDir(m.stateDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(m.stateDir, entry.Name()))
		if err != nil {
			return fmt.Errorf("read plog task: %w", err)
		}
		var task Task
		if err := json.Unmarshal(contents, &task); err != nil {
			return fmt.Errorf("decode plog task %s: %w", entry.Name(), err)
		}
		if task.ID == "" || safeName(task.ID)+".json" != entry.Name() {
			return fmt.Errorf("invalid plog task identity in %s", entry.Name())
		}
		if task.Offsets == nil {
			task.Offsets = map[string]int64{}
		}
		if task.SegmentFiles == nil {
			task.SegmentFiles = map[string]string{}
		}
		m.tasks[task.ID] = &task
	}
	return nil
}

func cloneTask(task *Task) Task {
	copy := *task
	copy.Offsets = map[string]int64{}
	for key, value := range task.Offsets {
		copy.Offsets[key] = value
	}
	copy.SegmentFiles = map[string]string{}
	for key, value := range task.SegmentFiles {
		copy.SegmentFiles[key] = value
	}
	return copy
}

func newTaskID() string {
	value := make([]byte, 8)
	_, _ = rand.Read(value)
	return time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(value)
}

func safeName(value string) string {
	var result strings.Builder
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.' {
			result.WriteRune(character)
		} else {
			result.WriteByte('_')
		}
	}
	return result.String()
}

func boundedError(err error) string {
	value := err.Error()
	if len(value) > 2048 {
		value = value[:2048]
	}
	return value
}
