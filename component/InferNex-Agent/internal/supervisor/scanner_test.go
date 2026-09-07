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
	"errors"
	"testing"
	"time"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/diagnostics"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/observer"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/remediator"
)

type fakeObserver struct {
	summary     observer.ServiceSummary
	detail      observer.ServiceDetail
	topology    observer.Topology
	events      observer.EventEvidence
	empty       bool
	listErr     error
	inspectErr  error
	topologyErr error
	eventsErr   error
	onTopology  func()
}

func (f *fakeObserver) ListServices(context.Context, string) (observer.ServiceList, error) {
	if f.empty || f.listErr != nil {
		return observer.ServiceList{}, f.listErr
	}
	return observer.ServiceList{
		Namespace:     f.summary.Namespace,
		TotalServices: 1,
		Services:      []observer.ServiceSummary{f.summary},
	}, nil
}

func (f *fakeObserver) InspectService(context.Context, string, string) (observer.ServiceDetail, error) {
	return f.detail, f.inspectErr
}

func (f *fakeObserver) GetTopology(context.Context, string, string) (observer.Topology, error) {
	if f.onTopology != nil {
		f.onTopology()
	}
	return f.topology, f.topologyErr
}

func (f *fakeObserver) GetEvents(context.Context, string, string, int, int) (observer.EventEvidence, error) {
	return f.events, f.eventsErr
}

type fakeAnalyzer struct {
	calls int
}

type fakeRemediator struct {
	calls int
}

type fakeDiagnoser struct {
	calls int
	err   error
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
	if f.err != nil {
		return diagnostics.Report{}, f.err
	}
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

func newRecoveryScanner(t *testing.T) (*Scanner, *fakeObserver, *fakeRemediator) {
	t.Helper()
	service := observer.ServiceSummary{
		Namespace: "models", Name: "qwen", UID: "qwen-uid", Generation: 4, ObservedGeneration: 4,
		Recovery: &observer.RecoverySummary{Enabled: true, Profile: "approved", Name: "qwen-recovery"},
	}
	domainObserver := &fakeObserver{}
	domainObserver.setService(service)
	domainRemediator := &fakeRemediator{}
	scanner, err := New(domainObserver, nil, domainRemediator, NewSnapshotStore("test", time.Minute, true), Config{
		Namespaces: []string{"models"}, Interval: time.Minute, MinCriticalScans: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	return scanner, domainObserver, domainRemediator
}

func (f *fakeObserver) setService(service observer.ServiceSummary) {
	f.summary = service
	f.detail.Service = service
	f.topology.Service = service
}

func requireRemediation(t *testing.T, snapshot Snapshot, status string, scans int) {
	t.Helper()
	if len(snapshot.Namespaces) != 1 || len(snapshot.Namespaces[0].Services) != 1 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	remediation := snapshot.Namespaces[0].Services[0].Remediation
	if remediation == nil || remediation.Status != status || remediation.FailureScans != scans {
		t.Fatalf("remediation = %#v, want status %q with %d scans", remediation, status, scans)
	}
}

func TestScannerRestartsCriticalSequenceAfterInterruptedObservation(t *testing.T) {
	tests := []struct {
		name      string
		interrupt func(*Scanner, *fakeObserver, context.CancelFunc)
	}{
		{"service disappeared", func(_ *Scanner, observer *fakeObserver, _ context.CancelFunc) {
			observer.empty = true
		}},
		{"list failed", func(_ *Scanner, observer *fakeObserver, _ context.CancelFunc) {
			observer.listErr = errors.New("list unavailable")
		}},
		{"inspect failed", func(_ *Scanner, observer *fakeObserver, _ context.CancelFunc) {
			observer.inspectErr = errors.New("inspect unavailable")
		}},
		{"topology failed", func(_ *Scanner, observer *fakeObserver, _ context.CancelFunc) {
			observer.topologyErr = errors.New("topology unavailable")
		}},
		{"events failed", func(_ *Scanner, observer *fakeObserver, _ context.CancelFunc) {
			observer.eventsErr = errors.New("events unavailable")
		}},
		{"diagnostics failed", func(scanner *Scanner, _ *fakeObserver, _ context.CancelFunc) {
			scanner.diagnoser = &fakeDiagnoser{err: errors.New("diagnostics unavailable")}
		}},
		{"cancelled before collection", func(_ *Scanner, _ *fakeObserver, cancel context.CancelFunc) {
			cancel()
		}},
		{"cancelled during collection", func(_ *Scanner, observer *fakeObserver, cancel context.CancelFunc) {
			observer.onTopology = cancel
		}},
		{"healthy", func(_ *Scanner, observer *fakeObserver, _ context.CancelFunc) {
			service := observer.summary
			service.Ready = true
			observer.setService(service)
		}},
		{"reconciliation pending", func(_ *Scanner, observer *fakeObserver, _ context.CancelFunc) {
			service := observer.summary
			service.ObservedGeneration--
			observer.setService(service)
		}},
		{"recovery disabled", func(_ *Scanner, observer *fakeObserver, _ context.CancelFunc) {
			service := observer.summary
			service.Recovery = nil
			observer.setService(service)
		}},
		{"invalid recovery policy", func(_ *Scanner, domainObserver *fakeObserver, _ context.CancelFunc) {
			service := domainObserver.summary
			service.Recovery = &observer.RecoverySummary{Enabled: true}
			domainObserver.setService(service)
		}},
		{"supervisor remediation disabled", func(scanner *Scanner, _ *fakeObserver, _ context.CancelFunc) {
			scanner.remediator = nil
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scanner, domainObserver, domainRemediator := newRecoveryScanner(t)
			original := *domainObserver
			requireRemediation(t, scanner.ScanOnce(context.Background()), "waiting", 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			test.interrupt(scanner, domainObserver, cancel)
			scanner.ScanOnce(ctx)
			if domainRemediator.calls != 0 || len(scanner.failures) != 0 {
				t.Fatalf("interrupted observation retained failures: calls=%d failures=%#v", domainRemediator.calls, scanner.failures)
			}

			*domainObserver = original
			scanner.remediator = domainRemediator
			scanner.diagnoser = nil
			requireRemediation(t, scanner.ScanOnce(context.Background()), "waiting", 1)
			if domainRemediator.calls != 0 {
				t.Fatalf("recovery triggered before a new consecutive sequence: calls=%d", domainRemediator.calls)
			}
			requireRemediation(t, scanner.ScanOnce(context.Background()), "created", 2)
			if domainRemediator.calls != 1 {
				t.Fatalf("recovery calls = %d, want 1", domainRemediator.calls)
			}
		})
	}
}

func TestScannerBindsCriticalSequenceToRecoveryIdentity(t *testing.T) {
	tests := []struct {
		name   string
		change func(*observer.ServiceSummary)
	}{
		{"new UID", func(service *observer.ServiceSummary) { service.UID = "replacement-uid" }},
		{"new generation already reconciled", func(service *observer.ServiceSummary) {
			service.Generation++
			service.ObservedGeneration = service.Generation
		}},
		{"new profile", func(service *observer.ServiceSummary) { service.Recovery.Profile = "approved-v2" }},
		{"new recovery name", func(service *observer.ServiceSummary) { service.Recovery.Name = "qwen-recovery-v2" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scanner, domainObserver, domainRemediator := newRecoveryScanner(t)
			requireRemediation(t, scanner.ScanOnce(context.Background()), "waiting", 1)
			service := domainObserver.summary
			test.change(&service)
			domainObserver.setService(service)
			requireRemediation(t, scanner.ScanOnce(context.Background()), "waiting", 1)
			if domainRemediator.calls != 0 {
				t.Fatalf("new identity inherited previous failures: calls=%d", domainRemediator.calls)
			}
			requireRemediation(t, scanner.ScanOnce(context.Background()), "created", 2)
			if domainRemediator.calls != 1 {
				t.Fatalf("recovery calls = %d, want 1", domainRemediator.calls)
			}
		})
	}
}

func TestScannerRejectsMixedServiceObservations(t *testing.T) {
	tests := []struct {
		name   string
		change func(*fakeObserver)
	}{
		{"replaced after listing", func(observer *fakeObserver) { observer.summary.UID = "previous-uid" }},
		{"replaced after inspection", func(observer *fakeObserver) { observer.topology.Service.UID = "replacement-uid" }},
		{"generation changed after inspection", func(observer *fakeObserver) { observer.topology.Service.Generation++ }},
		{"policy changed after inspection", func(observer *fakeObserver) {
			policy := *observer.topology.Service.Recovery
			policy.Profile = "approved-v2"
			observer.topology.Service.Recovery = &policy
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scanner, domainObserver, domainRemediator := newRecoveryScanner(t)
			requireRemediation(t, scanner.ScanOnce(context.Background()), "waiting", 1)
			test.change(domainObserver)
			snapshot := scanner.ScanOnce(context.Background())
			requireRemediation(t, snapshot, "watching", 0)
			if domainRemediator.calls != 0 || len(scanner.failures) != 0 {
				t.Fatalf("mixed observation retained failures: calls=%d failures=%#v", domainRemediator.calls, scanner.failures)
			}
			found := false
			for _, issue := range snapshot.Namespaces[0].Services[0].Issues {
				found = found || issue.Code == "OBSERVATION_CHANGED"
			}
			if !found {
				t.Fatal("missing changed observation issue")
			}
		})
	}
}

func TestScannerDoesNotReuseAnalysisForReplacementService(t *testing.T) {
	scanner, domainObserver, _ := newRecoveryScanner(t)
	analyzer := &fakeAnalyzer{}
	scanner.analyzer = analyzer
	scanner.ScanOnce(context.Background())
	service := domainObserver.summary
	service.UID = "replacement-uid"
	domainObserver.setService(service)
	snapshot := scanner.ScanOnce(context.Background())
	analysis := snapshot.Namespaces[0].Services[0].Analysis
	if analyzer.calls != 2 || analysis == nil || analysis.Cached {
		t.Fatalf("replacement service reused analysis: calls=%d analysis=%#v", analyzer.calls, analysis)
	}
}

func TestScannerRestartsCriticalSequenceAfterDiagnosticBudgetDeferral(t *testing.T) {
	scanner, domainObserver, domainRemediator := newRecoveryScanner(t)
	diagnoser := &fakeDiagnoser{}
	scanner.diagnoser = diagnoser
	requireRemediation(t, scanner.ScanOnce(context.Background()), "waiting", 1)

	// Model this service being reached after earlier services consumed the
	// per-scan diagnostic budget.
	service, attempted := scanner.collectService(context.Background(), domainObserver.summary, false)
	scanner.evaluateRemediation(context.Background(), domainObserver.summary.Namespace+"/"+domainObserver.summary.Name, &service)
	if attempted || diagnoser.calls != 1 {
		t.Fatalf("deferred service unexpectedly collected diagnostics: attempted=%v calls=%d", attempted, diagnoser.calls)
	}
	if service.Remediation == nil || service.Remediation.Status != "watching" || service.Remediation.FailureScans != 0 ||
		domainRemediator.calls != 0 || len(scanner.failures) != 0 {
		t.Fatalf("diagnostic deferral did not interrupt recovery: remediation=%#v calls=%d failures=%#v",
			service.Remediation, domainRemediator.calls, scanner.failures)
	}

	requireRemediation(t, scanner.ScanOnce(context.Background()), "waiting", 1)
	if domainRemediator.calls != 0 {
		t.Fatalf("recovery triggered before a new complete sequence: calls=%d", domainRemediator.calls)
	}
	requireRemediation(t, scanner.ScanOnce(context.Background()), "created", 2)
	if domainRemediator.calls != 1 {
		t.Fatalf("recovery calls = %d, want 1", domainRemediator.calls)
	}
}
