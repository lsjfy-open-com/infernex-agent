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

package supervisor

import (
	"context"
	"sync"
	"time"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/observer"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/remediator"
)

type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

type Issue struct {
	Severity  Severity `json:"severity"`
	Code      string   `json:"code"`
	Message   string   `json:"message"`
	Component string   `json:"component,omitempty"`
	Resource  string   `json:"resource,omitempty"`
}

type Analysis struct {
	Status      string    `json:"status"`
	Provider    string    `json:"provider,omitempty"`
	Model       string    `json:"model,omitempty"`
	Content     string    `json:"content,omitempty"`
	Error       string    `json:"error,omitempty"`
	GeneratedAt time.Time `json:"generatedAt,omitempty"`
	Cached      bool      `json:"cached,omitempty"`
}

type ServiceSnapshot struct {
	Detail      observer.ServiceDetail `json:"detail"`
	Topology    observer.Topology      `json:"topology"`
	Events      observer.EventEvidence `json:"events"`
	Issues      []Issue                `json:"issues"`
	Analysis    *Analysis              `json:"analysis,omitempty"`
	Remediation *Remediation           `json:"remediation,omitempty"`
}

type Remediation struct {
	Status       string `json:"status"`
	Profile      string `json:"profile,omitempty"`
	Namespace    string `json:"namespace,omitempty"`
	Name         string `json:"name,omitempty"`
	FailureScans int    `json:"failureScans"`
	Message      string `json:"message,omitempty"`
	Error        string `json:"error,omitempty"`
}

type NamespaceSnapshot struct {
	Name       string            `json:"name"`
	ScannedAt  time.Time         `json:"scannedAt"`
	Error      string            `json:"error,omitempty"`
	Truncated  bool              `json:"truncated,omitempty"`
	Total      int               `json:"total"`
	Services   []ServiceSnapshot `json:"services"`
	ScanMillis int64             `json:"scanMillis"`
}

type Summary struct {
	Namespaces       int `json:"namespaces"`
	Services         int `json:"services"`
	ReadyServices    int `json:"readyServices"`
	DegradedServices int `json:"degradedServices"`
	Issues           int `json:"issues"`
	CriticalIssues   int `json:"criticalIssues"`
	WarningIssues    int `json:"warningIssues"`
	AnalyzedServices int `json:"analyzedServices"`
}

type Snapshot struct {
	Version         string              `json:"version"`
	StartedAt       time.Time           `json:"startedAt"`
	GeneratedAt     time.Time           `json:"generatedAt,omitempty"`
	ScanInterval    string              `json:"scanInterval"`
	ScanDurationMs  int64               `json:"scanDurationMs"`
	Ready           bool                `json:"ready"`
	AnalyzerEnabled bool                `json:"analyzerEnabled"`
	Namespaces      []NamespaceSnapshot `json:"namespaces"`
	Summary         Summary             `json:"summary"`
}

type AnalysisRequest struct {
	Service    observer.ServiceSummary     `json:"service"`
	BaseRefs   []string                    `json:"baseRefs,omitempty"`
	Components []observer.ComponentSummary `json:"components,omitempty"`
	Workloads  []observer.WorkloadSummary  `json:"workloads,omitempty"`
	Pods       []AnalysisPod               `json:"pods,omitempty"`
	Events     []AnalysisEvent             `json:"events,omitempty"`
	Issues     []Issue                     `json:"issues"`
}

type AnalysisPod struct {
	Name      string `json:"name"`
	Component string `json:"component,omitempty"`
	Phase     string `json:"phase"`
	Ready     bool   `json:"ready"`
	Restarts  int32  `json:"restarts"`
	Reason    string `json:"reason,omitempty"`
}

type AnalysisEvent struct {
	Type      string `json:"type,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Count     int32  `json:"count"`
	Kind      string `json:"kind,omitempty"`
	Name      string `json:"name,omitempty"`
	Component string `json:"component,omitempty"`
}

type AnalysisResult struct {
	Provider string
	Model    string
	Content  string
}

type Analyzer interface {
	Analyze(context.Context, AnalysisRequest) (AnalysisResult, error)
}

type Remediator interface {
	EnsureRecovery(context.Context, remediator.Request) (remediator.Result, error)
}

type SnapshotStore struct {
	mu       sync.RWMutex
	snapshot Snapshot
}

func NewSnapshotStore(version string, interval time.Duration, analyzerEnabled bool) *SnapshotStore {
	return &SnapshotStore{snapshot: Snapshot{
		Version:         version,
		StartedAt:       time.Now().UTC(),
		ScanInterval:    interval.String(),
		AnalyzerEnabled: analyzerEnabled,
		Namespaces:      make([]NamespaceSnapshot, 0),
	}}
}

func (s *SnapshotStore) Load() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

func (s *SnapshotStore) Store(snapshot Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot.Version = s.snapshot.Version
	snapshot.StartedAt = s.snapshot.StartedAt
	snapshot.ScanInterval = s.snapshot.ScanInterval
	snapshot.AnalyzerEnabled = s.snapshot.AnalyzerEnabled
	s.snapshot = snapshot
}
