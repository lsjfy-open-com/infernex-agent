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
	"testing"
	"time"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/diagnostics"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/observer"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/remediator"
)

type fakeObserver struct {
	summary  observer.ServiceSummary
	detail   observer.ServiceDetail
	topology observer.Topology
	events   observer.EventEvidence
}

func (f *fakeObserver) ListServices(context.Context, string) (observer.ServiceList, error) {
	return observer.ServiceList{
		Namespace:     f.summary.Namespace,
		TotalServices: 1,
		Services:      []observer.ServiceSummary{f.summary},
	}, nil
}

func (f *fakeObserver) InspectService(context.Context, string, string) (observer.ServiceDetail, error) {
	return f.detail, nil
}

func (f *fakeObserver) GetTopology(context.Context, string, string) (observer.Topology, error) {
	return f.topology, nil
}

func (f *fakeObserver) GetEvents(context.Context, string, string, int, int) (observer.EventEvidence, error) {
	return f.events, nil
}

type fakeAnalyzer struct {
	calls int
}

type fakeRemediator struct {
	calls int
}

type fakeDiagnoser struct {
	calls int
}

type budgetObserver struct{}

func (budgetObserver) ListServices(_ context.Context, namespace string) (observer.ServiceList, error) {
	return observer.ServiceList{
		Namespace: namespace, TotalServices: 2,
		Services: []observer.ServiceSummary{
			{Namespace: namespace, Name: "first", Ready: false, Generation: 1, ObservedGeneration: 1},
			{Namespace: namespace, Name: "second", Ready: false, Generation: 1, ObservedGeneration: 1},
		},
	}, nil
}

func (budgetObserver) InspectService(_ context.Context, namespace, name string) (observer.ServiceDetail, error) {
	return observer.ServiceDetail{Service: observer.ServiceSummary{
		Namespace: namespace, Name: name, Ready: false, Generation: 1, ObservedGeneration: 1,
	}}, nil
}

func (budgetObserver) GetTopology(_ context.Context, namespace, name string) (observer.Topology, error) {
	return observer.Topology{
		Service:   observer.ServiceSummary{Namespace: namespace, Name: name, Ready: false},
		Workloads: []observer.WorkloadSummary{}, Pods: []observer.PodSummary{},
	}, nil
}

func (budgetObserver) GetEvents(_ context.Context, namespace, name string, since, _ int) (observer.EventEvidence, error) {
	return observer.EventEvidence{
		Service:      observer.ServiceReference{Namespace: namespace, Name: name},
		SinceMinutes: since, Events: []observer.EventSummary{},
	}, nil
}

func (f *fakeDiagnoser) Diagnose(_ context.Context, request diagnostics.Request) (diagnostics.Report, error) {
	f.calls++
	return diagnostics.Report{
		Service: diagnostics.ServiceReference{Namespace: request.Namespace, Name: request.Name},
		Incidents: []diagnostics.Incident{{
			ID: "npu-stream-1", RootCategory: "npu-device-failure",
			Severity: diagnostics.SeverityCritical, Confidence: "high",
			Components: []string{"npu-device-plugin", "engine-pd-decode"},
			Symptoms:   []string{"stream-interrupted"},
		}},
	}, nil
}

func (f *fakeRemediator) EnsureRecovery(
	_ context.Context,
	request remediator.Request,
) (remediator.Result, error) {
	f.calls++
	return remediator.Result{
		Namespace: request.Namespace,
		Name:      request.SourceName + "-recovery",
		Profile:   request.Profile,
		Action:    "created",
	}, nil
}

func (f *fakeAnalyzer) Analyze(
	_ context.Context,
	request AnalysisRequest,
) (AnalysisResult, error) {
	f.calls++
	return AnalysisResult{
		Provider: "test",
		Model:    "diagnostic-model",
		Content:  "Check the failed decode pod before changing capacity.",
	}, nil
}

func TestScannerBuildsSnapshotAndCachesUnchangedAnalysis(t *testing.T) {
	service := observer.ServiceSummary{
		Namespace:          "models",
		Name:               "qwen-pd",
		Mode:               "pd",
		Ready:              false,
		Generation:         4,
		ObservedGeneration: 4,
		Components: []observer.ComponentSummary{{
			Name: "inference-engine", Ready: false, Message: "decode group is unavailable",
		}},
		Recovery: &observer.RecoverySummary{
			Enabled: true,
			Profile: "approved-pd-profile",
		},
	}
	domainObserver := &fakeObserver{
		summary: service,
		detail: observer.ServiceDetail{
			Service:  service,
			BaseRefs: []string{"approved-pd-profile"},
		},
		topology: observer.Topology{
			Service: service,
			Workloads: []observer.WorkloadSummary{{
				Kind: "LeaderWorkerSet", Name: "qwen-pd-decode",
				Component: "engine-pd-decode", Desired: 2, Ready: 1,
			}},
			TotalPods: 1,
			Pods: []observer.PodSummary{{
				Name: "qwen-pd-decode-0", Component: "engine-pd-decode",
				Phase: "Running", Ready: false, Restarts: 3, Reason: "CrashLoopBackOff",
			}},
		},
		events: observer.EventEvidence{
			Service:      observer.ServiceReference{Namespace: "models", Name: "qwen-pd"},
			SinceMinutes: 60,
			TotalEvents:  1,
			Events: []observer.EventSummary{{
				Type: "Warning", Reason: "BackOff", Count: 3,
				Kind: "Pod", Name: "qwen-pd-decode-0", Component: "engine-pd-decode",
			}},
		},
	}
	domainAnalyzer := &fakeAnalyzer{}
	domainRemediator := &fakeRemediator{}
	domainDiagnoser := &fakeDiagnoser{}
	store := NewSnapshotStore("test", time.Minute, true)
	scanner, err := New(domainObserver, domainAnalyzer, domainRemediator, store, Config{
		Namespaces:       []string{"models", "models"},
		Interval:         time.Minute,
		MinCriticalScans: 2,
		Diagnoser:        domainDiagnoser,
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	first := scanner.ScanOnce(context.Background())
	if !first.Ready || first.Summary.Services != 1 || first.Summary.DegradedServices != 1 {
		t.Fatalf("unexpected summary: %#v", first.Summary)
	}
	if first.Summary.CriticalIssues < 2 || first.Summary.WarningIssues < 2 {
		t.Fatalf("issues were not classified: %#v", first.Summary)
	}
	serviceSnapshot := first.Namespaces[0].Services[0]
	if serviceSnapshot.Analysis == nil ||
		serviceSnapshot.Analysis.Status != "complete" ||
		serviceSnapshot.Analysis.Content == "" {
		t.Fatalf("analysis = %#v", serviceSnapshot.Analysis)
	}
	if domainAnalyzer.calls != 1 {
		t.Fatalf("analyzer calls = %d, want 1", domainAnalyzer.calls)
	}
	if domainDiagnoser.calls != 1 || serviceSnapshot.Diagnostics == nil ||
		len(serviceSnapshot.Diagnostics.Incidents) != 1 {
		t.Fatalf("diagnostics = %#v, calls = %d", serviceSnapshot.Diagnostics, domainDiagnoser.calls)
	}
	foundDiagnosticIssue := false
	for _, issue := range serviceSnapshot.Issues {
		if issue.Code == "DIAGNOSTIC_NPU_DEVICE_FAILURE" {
			foundDiagnosticIssue = true
			break
		}
	}
	if !foundDiagnosticIssue {
		t.Fatalf("diagnostic issue missing: %#v", serviceSnapshot.Issues)
	}
	if serviceSnapshot.Remediation == nil ||
		serviceSnapshot.Remediation.Status != "waiting" ||
		domainRemediator.calls != 0 {
		t.Fatalf("first remediation = %#v", serviceSnapshot.Remediation)
	}

	second := scanner.ScanOnce(context.Background())
	secondAnalysis := second.Namespaces[0].Services[0].Analysis
	if secondAnalysis == nil || !secondAnalysis.Cached {
		t.Fatalf("second analysis = %#v, want cached", secondAnalysis)
	}
	if domainAnalyzer.calls != 1 {
		t.Fatalf("unchanged evidence called analyzer %d times, want 1", domainAnalyzer.calls)
	}
	if second.Namespaces[0].Services[0].Remediation == nil ||
		second.Namespaces[0].Services[0].Remediation.Status != "created" ||
		domainRemediator.calls != 1 {
		t.Fatalf("second remediation = %#v", second.Namespaces[0].Services[0].Remediation)
	}
}

func TestNewScannerRejectsMissingNamespaces(t *testing.T) {
	_, err := New(
		&fakeObserver{},
		nil,
		nil,
		NewSnapshotStore("test", time.Minute, false),
		Config{Interval: time.Minute},
	)
	if err == nil {
		t.Fatal("New unexpectedly accepted an empty namespace list")
	}
}

func TestScannerBoundsLogDiagnosticsPerScan(t *testing.T) {
	diagnoser := &fakeDiagnoser{}
	scanner, err := New(
		budgetObserver{},
		nil,
		nil,
		NewSnapshotStore("test", time.Minute, false),
		Config{
			Namespaces:            []string{"models"},
			Interval:              time.Minute,
			MaxDiagnosticsPerScan: 1,
			Diagnoser:             diagnoser,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := scanner.ScanOnce(context.Background())
	if diagnoser.calls != 1 {
		t.Fatalf("diagnostic calls = %d, want 1", diagnoser.calls)
	}
	if len(snapshot.Namespaces) != 1 || len(snapshot.Namespaces[0].Services) != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	deferred := false
	for _, issue := range snapshot.Namespaces[0].Services[1].Issues {
		if issue.Code == "DIAGNOSTICS_DEFERRED" {
			deferred = true
		}
	}
	if !deferred {
		t.Fatalf("second service was not deferred: %#v", snapshot.Namespaces[0].Services[1].Issues)
	}
}
