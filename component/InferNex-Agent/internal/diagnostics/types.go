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

package diagnostics

import (
	"context"
	"time"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/observer"
)

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Request is intentionally bounded to one InferNexService. The implementation
// discovers Pods only through the labels owned by InferNex Bridge.
type Request struct {
	Namespace    string
	Name         string
	SinceMinutes int
	MaxPods      int
	TailLines    int64
}

type ServiceReference struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type Evidence struct {
	Timestamp time.Time `json:"timestamp"`
	Source    string    `json:"source"`
	Category  string    `json:"category"`
	Severity  Severity  `json:"severity"`
	Component string    `json:"component,omitempty"`
	Node      string    `json:"node,omitempty"`
	Pod       string    `json:"pod,omitempty"`
	Container string    `json:"container,omitempty"`
	Previous  bool      `json:"previous,omitempty"`
	Message   string    `json:"message"`
}

type Incident struct {
	ID             string     `json:"id"`
	RootCategory   string     `json:"rootCategory"`
	Severity       Severity   `json:"severity"`
	Confidence     string     `json:"confidence"`
	StartedAt      time.Time  `json:"startedAt"`
	EndedAt        time.Time  `json:"endedAt"`
	Components     []string   `json:"components,omitempty"`
	Nodes          []string   `json:"nodes,omitempty"`
	Pods           []string   `json:"pods,omitempty"`
	Symptoms       []string   `json:"symptoms,omitempty"`
	Evidence       []Evidence `json:"evidence"`
	Recommendation string     `json:"recommendation"`
}

type Report struct {
	Service           ServiceReference       `json:"service"`
	CollectedAt       time.Time              `json:"collectedAt"`
	SinceMinutes      int                    `json:"sinceMinutes"`
	TotalPods         int                    `json:"totalPods"`
	PodsTruncated     bool                   `json:"podsTruncated,omitempty"`
	EvidenceTruncated bool                   `json:"evidenceTruncated,omitempty"`
	CollectionErrors  []string               `json:"collectionErrors,omitempty"`
	Events            observer.EventEvidence `json:"events"`
	Evidence          []Evidence             `json:"evidence"`
	Incidents         []Incident             `json:"incidents"`
}

// Comparison is the experiment health gate. A category is a regression only
// when its candidate count is greater than the same category on the stable
// baseline, which avoids blaming a new feature for a pre-existing cluster fault.
type Comparison struct {
	Healthy              bool             `json:"healthy"`
	BaselineCritical     int              `json:"baselineCritical"`
	CandidateCritical    int              `json:"candidateCritical"`
	RegressionCategories []string         `json:"regressionCategories,omitempty"`
	Baseline             ServiceReference `json:"baseline"`
	Candidate            ServiceReference `json:"candidate"`
}

type Diagnoser interface {
	Diagnose(context.Context, Request) (Report, error)
}
