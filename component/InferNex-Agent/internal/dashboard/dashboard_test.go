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

package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/experiment"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/observer"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/slo"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/supervisor"
)

type experimentReaderStub struct{}

func (experimentReaderStub) List(context.Context) ([]experiment.Plan, error) {
	return []experiment.Plan{{
		ID: "experiment-1", Status: experiment.PlanStatusRunning, StableService: "qwen-stable",
		Stages: []experiment.Stage{{FeatureProfile: "enable-mooncake", Status: experiment.StageStatusSoaking}},
	}}, nil
}
func TestPublicSummaryOmitsFreeTextEvidence(t *testing.T) {
	store := supervisor.NewSnapshotStore("test", time.Minute, false)
	store.Store(supervisor.Snapshot{Ready: true, Namespaces: []supervisor.NamespaceSnapshot{{Name: "models", Error: "secret-error", Services: []supervisor.ServiceSnapshot{{Detail: observer.ServiceDetail{Service: observer.ServiceSummary{Name: "qwen", Namespace: "models", Ready: true}}, Analysis: &supervisor.Analysis{Content: "private-prompt-response"}}}}}})
	handler := New(store, WithPublicSummary(true))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil))
	body := response.Body.String()
	if strings.Contains(body, "private-prompt-response") || strings.Contains(body, "secret-error") || !strings.Contains(body, "qwen") {
		t.Fatalf("public snapshot leaked free text or lost service: %s", body)
	}
}
func TestPublicExperimentsCloneWithoutPrivatePayload(t *testing.T) {
	private := []experiment.Plan{{ID: "exp", Message: "secret-plan", SLOSnapshot: &slo.Profile{Cases: []slo.Case{{Prompt: "secret-prompt"}}}, Stages: []experiment.Stage{{Status: experiment.StageStatusRolledBack, Message: "secret-stage", SLO: &slo.Result{Decision: "inconclusive", Reason: "secret-response"}}}}}
	safe := publicExperiments(private)
	data, _ := json.Marshal(safe)
	if strings.Contains(string(data), "secret") || safe[0].Stages[0].Status != experiment.StageStatusRolledBack {
		t.Fatalf("unsafe public experiment: %s", data)
	}
	if private[0].Message != "secret-plan" || private[0].Stages[0].Message != "secret-stage" {
		t.Fatal("source experiment mutated")
	}
}

func TestDashboardServesUIAndReadinessSnapshot(t *testing.T) {
	store := supervisor.NewSnapshotStore("test-version", time.Minute, false)
	handler := New(store)

	notReady := httptest.NewRecorder()
	handler.ServeHTTP(notReady, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if notReady.Code != http.StatusServiceUnavailable {
		t.Fatalf("initial readiness status = %d", notReady.Code)
	}

	store.Store(supervisor.Snapshot{
		GeneratedAt: time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
		Ready:       true,
		Summary:     supervisor.Summary{Services: 2, ReadyServices: 1, DegradedServices: 1},
		Namespaces:  []supervisor.NamespaceSnapshot{},
	})
	api := httptest.NewRecorder()
	handler.ServeHTTP(api, httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil))
	if api.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d", api.Code)
	}
	decoded := supervisor.Snapshot{}
	if err := json.NewDecoder(api.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if !decoded.Ready || decoded.Version != "test-version" || decoded.Summary.Services != 2 {
		t.Fatalf("snapshot = %#v", decoded)
	}
	if api.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("security headers were not set")
	}

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	body, err := io.ReadAll(page.Body)
	if err != nil {
		t.Fatalf("read dashboard page: %v", err)
	}
	if !strings.Contains(string(body), "InferNex <span>Agent</span>") ||
		!strings.Contains(string(body), "/api/v1/snapshot") {
		t.Fatal("dashboard page is missing expected content")
	}
}

func TestDashboardServesExperimentState(t *testing.T) {
	store := supervisor.NewSnapshotStore("test-version", time.Minute, false)
	handler := New(store, WithExperiments(experimentReaderStub{}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/experiments", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("experiments status = %d, body = %s", response.Code, response.Body.String())
	}
	var plans []experiment.Plan
	if err := json.NewDecoder(response.Body).Decode(&plans); err != nil {
		t.Fatalf("decode experiments: %v", err)
	}
	if len(plans) != 1 || plans[0].StableService != "qwen-stable" ||
		len(plans[0].Stages) != 1 || plans[0].Stages[0].FeatureProfile != "enable-mooncake" {
		t.Fatalf("plans = %#v", plans)
	}
}

func TestDashboardServesManagementLocations(t *testing.T) {
	store := supervisor.NewSnapshotStore("test-version", time.Minute, false)
	handler := New(store, WithManagementInfo(ManagementInfo{
		SLOProfileDirectory:    "/etc/infernex-agent/slo-profiles",
		ConfigVersionDirectory: "/var/lib/infernex-agent/config-versions",
		SLOEnabled:             true,
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/management", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("management status = %d", response.Code)
	}
	var info ManagementInfo
	if err := json.NewDecoder(response.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if !info.SLOEnabled || info.SLOProfileDirectory == "" || info.ConfigVersionDirectory == "" {
		t.Fatalf("management = %#v", info)
	}
}
