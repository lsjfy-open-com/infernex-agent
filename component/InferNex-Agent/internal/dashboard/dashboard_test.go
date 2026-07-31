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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/supervisor"
)

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
