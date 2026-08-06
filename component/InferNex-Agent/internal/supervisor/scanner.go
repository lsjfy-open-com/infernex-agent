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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/diagnostics"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/observer"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/remediator"
)

const (
	defaultEventSinceMinutes = 60
	defaultEventLimit        = 25
	defaultMaxAnalyses       = 10
	defaultMaxDiagnostics    = 10
	defaultMinCriticalScans  = 3
	maxIssueMessageRunes     = 512
	maxIssueEvents           = 10
	maxAnalysisPods          = 50
	maxAnalysisEvents        = 25
)

type Config struct {
	Namespaces            []string
	Interval              time.Duration
	EventSinceMinutes     int
	EventLimit            int
	MaxAnalysesPerScan    int
	MaxDiagnosticsPerScan int
	MinCriticalScans      int
	Diagnoser             diagnostics.Diagnoser
}

type Scanner struct {
	observer      observer.Observer
	analyzer      Analyzer
	remediator    Remediator
	diagnoser     diagnostics.Diagnoser
	store         *SnapshotStore
	config        Config
	now           func() time.Time
	cache         map[string]cachedAnalysis
	failureCounts map[string]int
}

type cachedAnalysis struct {
	fingerprint string
	analysis    Analysis
}

func New(
	domainObserver observer.Observer,
	analyzer Analyzer,
	domainRemediator Remediator,
	store *SnapshotStore,
	config Config,
) (*Scanner, error) {
	if domainObserver == nil {
		return nil, fmt.Errorf("observer is required")
	}
	if store == nil {
		return nil, fmt.Errorf("snapshot store is required")
	}
	config.Namespaces = normalizedNamespaces(config.Namespaces)
	if len(config.Namespaces) == 0 {
		return nil, fmt.Errorf("at least one scan namespace is required")
	}
	if config.Interval <= 0 {
		return nil, fmt.Errorf("scan interval must be positive")
	}
	if config.EventSinceMinutes <= 0 {
		config.EventSinceMinutes = defaultEventSinceMinutes
	}
	if config.EventLimit <= 0 {
		config.EventLimit = defaultEventLimit
	}
	if config.MaxAnalysesPerScan <= 0 {
		config.MaxAnalysesPerScan = defaultMaxAnalyses
	}
	if config.MaxDiagnosticsPerScan <= 0 {
		config.MaxDiagnosticsPerScan = defaultMaxDiagnostics
	}
	if config.MinCriticalScans <= 0 {
		config.MinCriticalScans = defaultMinCriticalScans
	}
	return &Scanner{
		observer:      domainObserver,
		analyzer:      analyzer,
		remediator:    domainRemediator,
		diagnoser:     config.Diagnoser,
		store:         store,
		config:        config,
		now:           time.Now,
		cache:         make(map[string]cachedAnalysis),
		failureCounts: make(map[string]int),
	}, nil
}

func (s *Scanner) Run(ctx context.Context) {
	s.scanAndPublish(ctx)
	ticker := time.NewTicker(s.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scanAndPublish(ctx)
		}
	}
}

func (s *Scanner) ScanOnce(ctx context.Context) Snapshot {
	started := s.now().UTC()
	snapshot := Snapshot{
		GeneratedAt: started,
		Ready:       true,
		Namespaces:  make([]NamespaceSnapshot, 0, len(s.config.Namespaces)),
	}
	analysesRemaining := s.config.MaxAnalysesPerScan
	diagnosticsRemaining := s.config.MaxDiagnosticsPerScan
	activeCacheKeys := make(map[string]struct{})

	for _, namespace := range s.config.Namespaces {
		namespaceSnapshot, usedAnalyses, usedDiagnostics := s.scanNamespace(
			ctx,
			namespace,
			analysesRemaining,
			diagnosticsRemaining,
			activeCacheKeys,
		)
		analysesRemaining -= usedAnalyses
		diagnosticsRemaining -= usedDiagnostics
		snapshot.Namespaces = append(snapshot.Namespaces, namespaceSnapshot)
	}
	for key := range s.cache {
		if _, active := activeCacheKeys[key]; !active {
			delete(s.cache, key)
		}
	}
	snapshot.Summary = summarizeSnapshot(snapshot.Namespaces)
	snapshot.ScanDurationMs = s.now().UTC().Sub(started).Milliseconds()
	return snapshot
}

func (s *Scanner) scanAndPublish(ctx context.Context) {
	snapshot := s.ScanOnce(ctx)
	s.store.Store(snapshot)
	slog.Info(
		"completed InferNex supervisor scan",
		"namespaces", snapshot.Summary.Namespaces,
		"services", snapshot.Summary.Services,
		"degraded", snapshot.Summary.DegradedServices,
		"issues", snapshot.Summary.Issues,
		"duration_ms", snapshot.ScanDurationMs,
	)
}

func (s *Scanner) scanNamespace(
	ctx context.Context,
	namespace string,
	analysesRemaining int,
	diagnosticsRemaining int,
	activeCacheKeys map[string]struct{},
) (NamespaceSnapshot, int, int) {
	started := s.now().UTC()
	result := NamespaceSnapshot{
		Name:      namespace,
		ScannedAt: started,
		Services:  make([]ServiceSnapshot, 0),
	}
	list, err := s.observer.ListServices(ctx, namespace)
	if err != nil {
		result.Error = boundedMessage(err.Error())
		result.ScanMillis = s.now().UTC().Sub(started).Milliseconds()
		return result, 0, 0
	}
	result.Total = list.TotalServices
	result.Truncated = list.ServicesTruncated

	usedAnalyses := 0
	usedDiagnostics := 0
	for _, service := range list.Services {
		if err := ctx.Err(); err != nil {
			result.Error = boundedMessage(err.Error())
			break
		}
		serviceSnapshot, diagnosticAttempted := s.collectService(
			ctx,
			service,
			diagnosticsRemaining-usedDiagnostics > 0,
		)
		if diagnosticAttempted {
			usedDiagnostics++
		}
		cacheKey := service.Namespace + "/" + service.Name
		activeCacheKeys[cacheKey] = struct{}{}
		if len(serviceSnapshot.Issues) > 0 && s.analyzer != nil {
			fingerprint := analysisFingerprint(serviceSnapshot)
			if cached, found := s.cache[cacheKey]; found && cached.fingerprint == fingerprint {
				analysis := cached.analysis
				analysis.Cached = true
				serviceSnapshot.Analysis = &analysis
			} else if analysesRemaining-usedAnalyses > 0 {
				analysis := s.analyzeService(ctx, serviceSnapshot)
				serviceSnapshot.Analysis = &analysis
				usedAnalyses++
				if analysis.Status == "complete" {
					s.cache[cacheKey] = cachedAnalysis{fingerprint: fingerprint, analysis: analysis}
				}
			} else {
				serviceSnapshot.Analysis = &Analysis{Status: "deferred"}
			}
		}
		s.evaluateRemediation(ctx, cacheKey, &serviceSnapshot)
		result.Services = append(result.Services, serviceSnapshot)
	}
	result.ScanMillis = s.now().UTC().Sub(started).Milliseconds()
	return result, usedAnalyses, usedDiagnostics
}

func (s *Scanner) collectService(
	ctx context.Context,
	summary observer.ServiceSummary,
	allowDiagnostics bool,
) (ServiceSnapshot, bool) {
	result := ServiceSnapshot{
		Detail: observer.ServiceDetail{Service: summary},
		Topology: observer.Topology{
			Service:   summary,
			Workloads: make([]observer.WorkloadSummary, 0),
			Pods:      make([]observer.PodSummary, 0),
		},
		Events: observer.EventEvidence{
			Service: observer.ServiceReference{
				Namespace: summary.Namespace,
				Name:      summary.Name,
			},
			SinceMinutes: s.config.EventSinceMinutes,
			Events:       make([]observer.EventSummary, 0),
		},
		Issues: make([]Issue, 0),
	}

	detail, err := s.observer.InspectService(ctx, summary.Namespace, summary.Name)
	if err != nil {
		result.Issues = append(result.Issues, issueForError("INSPECT_FAILED", "InferNexService", err))
	} else {
		result.Detail = detail
	}

	topology, err := s.observer.GetTopology(ctx, summary.Namespace, summary.Name)
	if err != nil {
		result.Issues = append(result.Issues, issueForError("TOPOLOGY_FAILED", "InferNexService", err))
	} else {
		result.Topology = topology
	}

	result.Issues = append(result.Issues, detectStateIssues(result.Detail.Service, result.Topology)...)
	if len(result.Issues) > 0 || !summary.Ready {
		events, eventErr := s.observer.GetEvents(
			ctx,
			summary.Namespace,
			summary.Name,
			s.config.EventSinceMinutes,
			s.config.EventLimit,
		)
		if eventErr != nil {
			result.Issues = append(result.Issues, issueForError("EVENTS_FAILED", "InferNexService", eventErr))
		} else {
			result.Events = events
			result.Issues = append(result.Issues, detectEventIssues(events)...)
		}
	}
	diagnosticAttempted := false
	if s.diagnoser != nil && (len(result.Issues) > 0 || !summary.Ready) && !allowDiagnostics {
		result.Issues = append(result.Issues, Issue{
			Severity: SeverityInfo,
			Code:     "DIAGNOSTICS_DEFERRED",
			Message:  "log diagnostics were deferred by the per-scan collection budget",
			Resource: "InferNexService/" + summary.Name,
		})
	}
	if s.diagnoser != nil && (len(result.Issues) > 0 || !summary.Ready) && allowDiagnostics {
		diagnosticAttempted = true
		report, diagnosticErr := s.diagnoser.Diagnose(ctx, diagnostics.Request{
			Namespace:    summary.Namespace,
			Name:         summary.Name,
			SinceMinutes: s.config.EventSinceMinutes,
		})
		if diagnosticErr != nil {
			result.Issues = append(
				result.Issues,
				issueForError("DIAGNOSTICS_FAILED", "InferNexService", diagnosticErr),
			)
		} else {
			result.Diagnostics = &report
			result.Issues = append(result.Issues, issuesForIncidents(report.Incidents)...)
		}
	}
	sortIssues(result.Issues)
	return result, diagnosticAttempted
}

func (s *Scanner) evaluateRemediation(
	ctx context.Context,
	key string,
	service *ServiceSnapshot,
) {
	policy := service.Detail.Service.Recovery
	if policy == nil || !policy.Enabled {
		delete(s.failureCounts, key)
		return
	}
	if strings.TrimSpace(policy.Profile) == "" {
		delete(s.failureCounts, key)
		service.Remediation = &Remediation{
			Status:  "invalid-policy",
			Message: "auto-recovery is enabled but no approved recovery profile is configured",
		}
		return
	}
	if s.remediator == nil {
		delete(s.failureCounts, key)
		service.Remediation = &Remediation{
			Status:  "disabled",
			Profile: policy.Profile,
			Message: "service opted in, but supervisor auto-remediation is disabled",
		}
		return
	}
	if service.Detail.Service.ObservedGeneration < service.Detail.Service.Generation ||
		!hasCriticalIssue(service.Issues) {
		delete(s.failureCounts, key)
		service.Remediation = &Remediation{
			Status:  "watching",
			Profile: policy.Profile,
			Message: "recovery policy is armed; no stable critical failure is present",
		}
		return
	}

	s.failureCounts[key]++
	failureScans := s.failureCounts[key]
	service.Remediation = &Remediation{
		Status:       "waiting",
		Profile:      policy.Profile,
		FailureScans: failureScans,
		Message: fmt.Sprintf(
			"waiting for %d consecutive critical scans",
			s.config.MinCriticalScans,
		),
	}
	if failureScans < s.config.MinCriticalScans {
		return
	}

	result, err := s.remediator.EnsureRecovery(ctx, remediator.Request{
		Namespace:  service.Detail.Service.Namespace,
		SourceName: service.Detail.Service.Name,
		Profile:    policy.Profile,
		Name:       policy.Name,
	})
	if err != nil {
		service.Remediation.Status = "error"
		service.Remediation.Error = boundedMessage(err.Error())
		service.Remediation.Message = "failed to ensure recovery InferNexService"
		return
	}
	service.Remediation.Status = result.Action
	service.Remediation.Namespace = result.Namespace
	service.Remediation.Name = result.Name
	service.Remediation.ChangeID = result.ChangeID
	service.Remediation.Message = "recovery InferNexService is managed by InferNex Bridge"
}

func (s *Scanner) analyzeService(ctx context.Context, service ServiceSnapshot) Analysis {
	result, err := s.analyzer.Analyze(ctx, analysisRequest(service))
	if err != nil {
		return Analysis{
			Status:      "error",
			Error:       boundedMessage(err.Error()),
			GeneratedAt: s.now().UTC(),
		}
	}
	return Analysis{
		Status:      "complete",
		Provider:    result.Provider,
		Model:       result.Model,
		Content:     boundedAnalysis(result.Content),
		GeneratedAt: s.now().UTC(),
	}
}

func detectStateIssues(
	service observer.ServiceSummary,
	topology observer.Topology,
) []Issue {
	issues := make([]Issue, 0)
	if service.Recovery != nil &&
		service.Recovery.Enabled &&
		strings.TrimSpace(service.Recovery.Profile) == "" {
		issues = append(issues, Issue{
			Severity: SeverityWarning,
			Code:     "RECOVERY_POLICY_INVALID",
			Message:  "auto-recovery is enabled without a recovery profile",
			Resource: "InferNexService/" + service.Name,
		})
	}
	if service.ObservedGeneration < service.Generation {
		issues = append(issues, Issue{
			Severity: SeverityWarning,
			Code:     "RECONCILE_PENDING",
			Message: fmt.Sprintf(
				"Bridge observed generation %d, desired generation is %d",
				service.ObservedGeneration,
				service.Generation,
			),
			Resource: "InferNexService/" + service.Name,
		})
	}
	if !service.Ready {
		severity := SeverityCritical
		if service.ObservedGeneration < service.Generation {
			severity = SeverityWarning
		}
		issues = append(issues, Issue{
			Severity: severity,
			Code:     "SERVICE_NOT_READY",
			Message:  "InferNexService is not ready",
			Resource: "InferNexService/" + service.Name,
		})
	}
	for _, component := range service.Components {
		if component.Ready {
			continue
		}
		message := "component is not ready"
		if strings.TrimSpace(component.Message) != "" {
			message = component.Message
		}
		issues = append(issues, Issue{
			Severity:  SeverityWarning,
			Code:      "COMPONENT_NOT_READY",
			Message:   boundedMessage(message),
			Component: component.Name,
		})
	}
	for _, workload := range topology.Workloads {
		if workload.Ready >= workload.Desired {
			continue
		}
		issues = append(issues, Issue{
			Severity:  SeverityCritical,
			Code:      "WORKLOAD_READY_DEFICIT",
			Message:   fmt.Sprintf("%d of %d replicas/groups are ready", workload.Ready, workload.Desired),
			Component: workload.Component,
			Resource:  workload.Kind + "/" + workload.Name,
		})
	}
	for _, pod := range topology.Pods {
		resource := "Pod/" + pod.Name
		if !pod.Ready {
			severity := SeverityWarning
			if pod.Phase == string(corev1.PodFailed) || criticalPodReason(pod.Reason) {
				severity = SeverityCritical
			}
			message := "pod is not ready"
			if pod.Reason != "" {
				message += ": " + pod.Reason
			}
			issues = append(issues, Issue{
				Severity:  severity,
				Code:      "POD_NOT_READY",
				Message:   boundedMessage(message),
				Component: pod.Component,
				Resource:  resource,
			})
		}
		if pod.Restarts > 0 {
			issues = append(issues, Issue{
				Severity:  SeverityWarning,
				Code:      "POD_RESTARTS",
				Message:   fmt.Sprintf("pod containers restarted %d times", pod.Restarts),
				Component: pod.Component,
				Resource:  resource,
			})
		}
	}
	return issues
}

func hasCriticalIssue(issues []Issue) bool {
	for _, issue := range issues {
		if issue.Severity == SeverityCritical {
			return true
		}
	}
	return false
}

func detectEventIssues(events observer.EventEvidence) []Issue {
	issues := make([]Issue, 0, maxIssueEvents)
	for _, event := range events.Events {
		if !strings.EqualFold(event.Type, corev1.EventTypeWarning) {
			continue
		}
		resource := ""
		if event.Kind != "" || event.Name != "" {
			resource = event.Kind + "/" + event.Name
		}
		message := "recent warning event"
		if event.Reason != "" {
			message += ": " + event.Reason
		}
		if event.Count > 1 {
			message += fmt.Sprintf(" (count %d)", event.Count)
		}
		issues = append(issues, Issue{
			Severity:  SeverityWarning,
			Code:      "KUBERNETES_WARNING_EVENT",
			Message:   message,
			Component: event.Component,
			Resource:  resource,
		})
		if len(issues) == maxIssueEvents {
			break
		}
	}
	return issues
}

func analysisRequest(service ServiceSnapshot) AnalysisRequest {
	request := AnalysisRequest{
		Service:    service.Detail.Service,
		BaseRefs:   service.Detail.BaseRefs,
		Components: service.Detail.Service.Components,
		Workloads:  service.Topology.Workloads,
		Issues:     service.Issues,
	}
	for index, pod := range service.Topology.Pods {
		if index >= maxAnalysisPods {
			break
		}
		request.Pods = append(request.Pods, AnalysisPod{
			Name:      pod.Name,
			Component: pod.Component,
			Phase:     pod.Phase,
			Ready:     pod.Ready,
			Restarts:  pod.Restarts,
			Reason:    pod.Reason,
		})
	}
	for index, event := range service.Events.Events {
		if index >= maxAnalysisEvents {
			break
		}
		request.Events = append(request.Events, AnalysisEvent{
			Type:      event.Type,
			Reason:    event.Reason,
			Count:     event.Count,
			Kind:      event.Kind,
			Name:      event.Name,
			Component: event.Component,
		})
	}
	if service.Diagnostics != nil {
		for index, incident := range service.Diagnostics.Incidents {
			if index >= 20 {
				break
			}
			request.Incidents = append(request.Incidents, AnalysisIncident{
				RootCategory: incident.RootCategory,
				Severity:     Severity(incident.Severity),
				Confidence:   incident.Confidence,
				Components:   append([]string(nil), incident.Components...),
				Symptoms:     append([]string(nil), incident.Symptoms...),
			})
		}
	}
	return request
}

func issuesForIncidents(incidents []diagnostics.Incident) []Issue {
	issues := make([]Issue, 0, len(incidents))
	for index, incident := range incidents {
		if index >= 10 {
			break
		}
		severity := SeverityWarning
		if incident.Severity == diagnostics.SeverityCritical {
			severity = SeverityCritical
		}
		component := ""
		if len(incident.Components) > 0 {
			component = strings.Join(incident.Components, ",")
		}
		issues = append(issues, Issue{
			Severity:  severity,
			Code:      "DIAGNOSTIC_" + strings.ToUpper(strings.ReplaceAll(incident.RootCategory, "-", "_")),
			Message:   boundedMessage(incident.Recommendation),
			Component: component,
			Resource:  "Incident/" + incident.ID,
		})
	}
	return issues
}

func summarizeSnapshot(namespaces []NamespaceSnapshot) Summary {
	summary := Summary{Namespaces: len(namespaces)}
	for _, namespace := range namespaces {
		for _, service := range namespace.Services {
			summary.Services++
			if service.Detail.Service.Ready {
				summary.ReadyServices++
			} else {
				summary.DegradedServices++
			}
			for _, issue := range service.Issues {
				summary.Issues++
				switch issue.Severity {
				case SeverityCritical:
					summary.CriticalIssues++
				case SeverityWarning:
					summary.WarningIssues++
				}
			}
			if service.Analysis != nil && service.Analysis.Status == "complete" {
				summary.AnalyzedServices++
			}
		}
	}
	return summary
}

func analysisFingerprint(service ServiceSnapshot) string {
	value, err := json.Marshal(analysisRequest(service))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func issueForError(code string, resource string, err error) Issue {
	return Issue{
		Severity: SeverityWarning,
		Code:     code,
		Message:  boundedMessage(err.Error()),
		Resource: resource,
	}
}

func criticalPodReason(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "crashloopbackoff", "imagepullbackoff", "errimagepull", "oomkilled",
		"createcontainerconfigerror", "runcontainererror":
		return true
	default:
		return false
	}
}

func sortIssues(issues []Issue) {
	rank := func(severity Severity) int {
		switch severity {
		case SeverityCritical:
			return 0
		case SeverityWarning:
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(issues, func(left, right int) bool {
		if rank(issues[left].Severity) != rank(issues[right].Severity) {
			return rank(issues[left].Severity) < rank(issues[right].Severity)
		}
		if issues[left].Code != issues[right].Code {
			return issues[left].Code < issues[right].Code
		}
		return issues[left].Resource < issues[right].Resource
	})
}

func normalizedNamespaces(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		namespace := strings.TrimSpace(value)
		if namespace == "" {
			continue
		}
		if _, found := seen[namespace]; found {
			continue
		}
		seen[namespace] = struct{}{}
		result = append(result, namespace)
	}
	sort.Strings(result)
	return result
}

func boundedMessage(value string) string {
	return boundedRunes(strings.TrimSpace(value), maxIssueMessageRunes)
}

func boundedAnalysis(value string) string {
	return boundedRunes(strings.TrimSpace(value), 16*1024)
}

func boundedRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit]) + "…"
}
