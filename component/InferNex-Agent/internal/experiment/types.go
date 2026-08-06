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
	"context"
	"time"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/diagnostics"
)

const (
	PlanStatusPlanned   = "planned"
	PlanStatusRunning   = "running"
	PlanStatusCompleted = "completed"
	PlanStatusFailed    = "failed"

	StageStatusPending    = "pending"
	StageStatusCreating   = "creating"
	StageStatusWaiting    = "waiting-ready"
	StageStatusSoaking    = "soaking"
	StageStatusPassed     = "passed"
	StageStatusRolledBack = "rolled-back"
)

const ApprovedFeatureLabel = "agent.infernex.io/approved-experiment-feature"

type Request struct {
	Namespace       string
	BaselineName    string
	CandidatePrefix string
	FeatureProfiles []string
	Confirm         bool
}

type Stage struct {
	Index          int                     `json:"index"`
	FeatureProfile string                  `json:"featureProfile"`
	BaselineName   string                  `json:"baselineName"`
	CandidateName  string                  `json:"candidateName"`
	Status         string                  `json:"status"`
	ChangeID       string                  `json:"changeId,omitempty"`
	StartedAt      time.Time               `json:"startedAt,omitempty"`
	ReadyAt        time.Time               `json:"readyAt,omitempty"`
	CompletedAt    time.Time               `json:"completedAt,omitempty"`
	Message        string                  `json:"message,omitempty"`
	Comparison     *diagnostics.Comparison `json:"comparison,omitempty"`
}

type Plan struct {
	APIVersion      string    `json:"apiVersion"`
	Kind            string    `json:"kind"`
	ID              string    `json:"id"`
	Namespace       string    `json:"namespace"`
	BaselineName    string    `json:"baselineName"`
	CandidatePrefix string    `json:"candidatePrefix"`
	FeatureProfiles []string  `json:"featureProfiles"`
	Status          string    `json:"status"`
	CurrentStage    int       `json:"currentStage"`
	StableService   string    `json:"stableService"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	Message         string    `json:"message,omitempty"`
	Stages          []Stage   `json:"stages"`
}

type Store interface {
	Append(Plan) error
	Latest(string) (Plan, error)
	Pending() ([]Plan, error)
	List() ([]Plan, error)
}

type Manager interface {
	Create(context.Context, Request) (Plan, error)
	Get(context.Context, string) (Plan, error)
	List(context.Context) ([]Plan, error)
}

type Config struct {
	TemplateNamespace  string
	ReadinessTimeout   time.Duration
	SoakDuration       time.Duration
	PollInterval       time.Duration
	DiagnosticInterval time.Duration
	DiagnosticsMinutes int
}
