/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package collectorrun

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/diagnosticexec"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	defaultDuration       = time.Hour
	maximumDuration       = 7 * 24 * time.Hour
	defaultInterval       = time.Minute
	minimumInterval       = 10 * time.Second
	maximumInterval       = time.Hour
	defaultMaxBytes int64 = 1024 * 1024 * 1024
	maximumMaxBytes int64 = 100 * 1024 * 1024 * 1024
	maxTargets            = 20
)

var profiles = map[string]bool{
	"hccn-pfc-stats": true, "hccn-device": true, "npu-inventory": false,
	"cann-version": false, "hccl-root-info": false, "hccl-test-layout": false,
}

type StartRequest struct {
	Profile         string `json:"profile"`
	Namespace       string `json:"namespace"`
	LabelSelector   string `json:"labelSelector"`
	Container       string `json:"container,omitempty"`
	DeviceIDs       []int  `json:"deviceIds,omitempty"`
	IntervalSeconds int    `json:"intervalSeconds,omitempty"`
	DurationMinutes int    `json:"durationMinutes,omitempty"`
	MaxBytes        int64  `json:"maxBytes,omitempty"`
	Confirm         bool   `json:"confirm"`
}

type Task struct {
	ID              string     `json:"id"`
	Profile         string     `json:"profile"`
	Namespace       string     `json:"namespace"`
	LabelSelector   string     `json:"labelSelector"`
	Container       string     `json:"container,omitempty"`
	DeviceIDs       []int      `json:"deviceIds,omitempty"`
	IntervalSeconds int        `json:"intervalSeconds"`
	Status          string     `json:"status"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	Deadline        time.Time  `json:"deadline"`
	StoppedAt       *time.Time `json:"stoppedAt,omitempty"`
	MaxBytes        int64      `json:"maxBytes"`
	CapturedBytes   int64      `json:"capturedBytes"`
	Samples         int        `json:"samples"`
	TargetsObserved int        `json:"targetsObserved"`
	EvidenceRoot    string     `json:"evidenceRoot"`
	LastError       string     `json:"lastError,omitempty"`
}

type TaskList struct {
	Tasks []Task `json:"tasks"`
}

type sample struct {
	CapturedAt time.Time             `json:"capturedAt"`
	Target     Target                `json:"target"`
	DeviceID   *int                  `json:"deviceId,omitempty"`
	Result     diagnosticexec.Result `json:"result"`
	Error      string                `json:"error,omitempty"`
}

type Manager struct {
	mu                    sync.Mutex
	source                Source
	stateDir, evidenceDir string
	tasks                 map[string]*Task
	ctx                   context.Context
}

func NewManager(source Source, stateDir, evidenceDir string) (*Manager, error) {
	if source == nil {
		return nil, fmt.Errorf("collector source is required")
	}
	var err error
	if stateDir, err = filepath.Abs(stateDir); err != nil {
		return nil, err
	}
	if evidenceDir, err = filepath.Abs(evidenceDir); err != nil {
		return nil, err
	}
	for _, directory := range []string{stateDir, evidenceDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("create collector directory: %w", err)
		}
	}
	m := &Manager{source: source, stateDir: stateDir, evidenceDir: evidenceDir, tasks: map[string]*Task{}}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
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
		return Task{}, fmt.Errorf("confirm must be true after approving targets, profile, duration, interval, and storage budget")
	}
	profile := strings.ToLower(strings.TrimSpace(request.Profile))
	usesDevice, ok := profiles[profile]
	if !ok {
		return Task{}, fmt.Errorf("unsupported collector profile %q", profile)
	}
	namespace, selector := strings.TrimSpace(request.Namespace), strings.TrimSpace(request.LabelSelector)
	if namespace == "" || selector == "" {
		return Task{}, fmt.Errorf("namespace and a non-empty labelSelector are required")
	}
	if _, err := labels.Parse(selector); err != nil {
		return Task{}, fmt.Errorf("invalid labelSelector: %w", err)
	}
	for _, deviceID := range request.DeviceIDs {
		if deviceID < 0 || deviceID > 63 {
			return Task{}, fmt.Errorf("deviceIds must contain values between 0 and 63")
		}
	}
	devices := normalizeDevices(request.DeviceIDs, usesDevice)
	if usesDevice && len(devices) == 0 {
		return Task{}, fmt.Errorf("deviceIds must contain values between 0 and 63")
	}
	interval := time.Duration(request.IntervalSeconds) * time.Second
	if interval == 0 {
		interval = defaultInterval
	}
	if interval < minimumInterval || interval > maximumInterval {
		return Task{}, fmt.Errorf("intervalSeconds must be between 10 and 3600")
	}
	duration := time.Duration(request.DurationMinutes) * time.Minute
	if duration == 0 {
		duration = defaultDuration
	}
	if duration < time.Minute || duration > maximumDuration {
		return Task{}, fmt.Errorf("durationMinutes must be between 1 and 10080")
	}
	maxBytes := request.MaxBytes
	if maxBytes == 0 {
		maxBytes = defaultMaxBytes
	}
	if maxBytes < 1024*1024 || maxBytes > maximumMaxBytes {
		return Task{}, fmt.Errorf("maxBytes must be between 1 MiB and 100 GiB")
	}
	now := time.Now().UTC()
	task := &Task{ID: newID(), Profile: profile, Namespace: namespace, LabelSelector: selector, Container: strings.TrimSpace(request.Container), DeviceIDs: devices, IntervalSeconds: int(interval / time.Second), Status: "running", CreatedAt: now, UpdatedAt: now, Deadline: now.Add(duration), MaxBytes: maxBytes, EvidenceRoot: m.evidenceDir}
	m.mu.Lock()
	for _, existing := range m.tasks {
		if existing.Status == "running" && existing.Profile == task.Profile && existing.Namespace == task.Namespace && existing.LabelSelector == task.LabelSelector && existing.Container == task.Container {
			m.mu.Unlock()
			return Task{}, fmt.Errorf("an equivalent collector task is already running: %s", existing.ID)
		}
	}
	m.tasks[task.ID] = task
	ctx := m.ctx
	snapshot := clone(task)
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

func (m *Manager) Get(id string) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[strings.TrimSpace(id)]
	if !ok {
		return Task{}, fmt.Errorf("collector task not found")
	}
	return clone(task), nil
}

func (m *Manager) List() TaskList {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := TaskList{Tasks: make([]Task, 0, len(m.tasks))}
	for _, task := range m.tasks {
		result.Tasks = append(result.Tasks, clone(task))
	}
	sort.Slice(result.Tasks, func(i, j int) bool { return result.Tasks[i].CreatedAt.After(result.Tasks[j].CreatedAt) })
	return result
}

func (m *Manager) Stop(id string, confirm bool) (Task, error) {
	if !confirm {
		return Task{}, fmt.Errorf("confirm must be true after reviewing the collector task")
	}
	m.mu.Lock()
	task, ok := m.tasks[strings.TrimSpace(id)]
	if !ok {
		m.mu.Unlock()
		return Task{}, fmt.Errorf("collector task not found")
	}
	if task.Status == "running" {
		now := time.Now().UTC()
		task.Status, task.StoppedAt, task.UpdatedAt = "stopped", &now, now
	}
	snapshot := clone(task)
	m.mu.Unlock()
	return snapshot, m.persist(snapshot)
}

func (m *Manager) run(ctx context.Context, id string) {
	for {
		if !m.poll(ctx, id) {
			return
		}
		m.mu.Lock()
		task, ok := m.tasks[id]
		interval := defaultInterval
		if ok {
			interval = time.Duration(task.IntervalSeconds) * time.Second
		}
		m.mu.Unlock()
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
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
		snapshot := clone(task)
		m.mu.Unlock()
		_ = m.persist(snapshot)
		return false
	}
	snapshot := clone(task)
	m.mu.Unlock()
	targets, err := m.source.ListTargets(ctx, snapshot.Namespace, snapshot.LabelSelector, snapshot.Container)
	if err != nil {
		m.recordError(id, err)
		return true
	}
	if len(targets) == 0 {
		m.recordError(id, fmt.Errorf("no matching Running Pod/container is currently visible"))
		return true
	}
	if len(targets) > maxTargets {
		targets = targets[:maxTargets]
	}
	devices := snapshot.DeviceIDs
	if len(devices) == 0 {
		devices = []int{0}
	}
	var lastCollectError error
	for _, target := range targets {
		for _, deviceID := range devices {
			result, collectErr := m.source.Collect(ctx, target, snapshot.Profile, deviceID)
			entry := sample{CapturedAt: time.Now().UTC(), Target: target, Result: result}
			if profiles[snapshot.Profile] {
				value := deviceID
				entry.DeviceID = &value
			}
			if collectErr != nil {
				entry.Error = bounded(collectErr.Error(), 2048)
				lastCollectError = collectErr
			}
			if !m.append(id, entry) {
				return false
			}
		}
	}
	m.mu.Lock()
	if current := m.tasks[id]; current != nil {
		current.TargetsObserved = len(targets)
		if lastCollectError == nil {
			current.LastError = ""
		} else {
			current.LastError = bounded(lastCollectError.Error(), 2048)
		}
		current.UpdatedAt = time.Now().UTC()
		snapshot = clone(current)
	}
	m.mu.Unlock()
	_ = m.persist(snapshot)
	return true
}

func (m *Manager) append(id string, entry sample) bool {
	payload, err := json.Marshal(entry)
	if err != nil {
		m.recordError(id, err)
		return true
	}
	payload = append(payload, '\n')
	m.mu.Lock()
	task := m.tasks[id]
	if task == nil || task.Status != "running" {
		m.mu.Unlock()
		return false
	}
	remaining := task.MaxBytes - task.CapturedBytes
	m.mu.Unlock()
	if int64(len(payload)) > remaining {
		m.finishCapacity(id)
		return false
	}
	directory := filepath.Join(m.evidenceDir, safe(id))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		m.recordError(id, err)
		return true
	}
	path := filepath.Join(directory, "samples.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		m.recordError(id, err)
		return true
	}
	_, writeErr := file.Write(payload)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		m.recordError(id, fmt.Errorf("append collector evidence: %v %v", writeErr, closeErr))
		return true
	}
	m.mu.Lock()
	if task = m.tasks[id]; task != nil {
		task.CapturedBytes += int64(len(payload))
		task.Samples++
		task.UpdatedAt = time.Now().UTC()
	}
	m.mu.Unlock()
	return true
}

func (m *Manager) finishCapacity(id string) {
	m.mu.Lock()
	if task := m.tasks[id]; task != nil && task.Status == "running" {
		now := time.Now().UTC()
		task.Status, task.StoppedAt, task.UpdatedAt = "capacity-reached", &now, now
		snapshot := clone(task)
		m.mu.Unlock()
		_ = m.persist(snapshot)
		return
	}
	m.mu.Unlock()
}
func (m *Manager) recordError(id string, err error) {
	m.mu.Lock()
	if task := m.tasks[id]; task != nil {
		task.LastError = bounded(err.Error(), 2048)
		task.UpdatedAt = time.Now().UTC()
		snapshot := clone(task)
		m.mu.Unlock()
		_ = m.persist(snapshot)
		return
	}
	m.mu.Unlock()
}
func (m *Manager) persist(task Task) error {
	payload, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return err
	}
	target := filepath.Join(m.stateDir, safe(task.ID)+".json")
	temporary := target + ".tmp"
	if err := os.WriteFile(temporary, append(payload, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, target)
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
		payload, err := os.ReadFile(filepath.Join(m.stateDir, entry.Name()))
		if err != nil {
			return err
		}
		var task Task
		if err := json.Unmarshal(payload, &task); err != nil {
			return err
		}
		if task.ID == "" || safe(task.ID)+".json" != entry.Name() {
			return fmt.Errorf("invalid collector task identity in %s", entry.Name())
		}
		m.tasks[task.ID] = &task
	}
	return nil
}
func normalizeDevices(values []int, required bool) []int {
	if !required {
		return nil
	}
	result := []int{}
	seen := map[int]bool{}
	for _, value := range values {
		if value < 0 || value > 63 || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Ints(result)
	return result
}
func clone(task *Task) Task {
	copy := *task
	copy.DeviceIDs = append([]int(nil), task.DeviceIDs...)
	return copy
}
func newID() string {
	value := make([]byte, 8)
	_, _ = rand.Read(value)
	return time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(value)
}
func safe(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
func bounded(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
