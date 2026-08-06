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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/observer"
)

const (
	ownerLabel                = "infernex.io/owner"
	componentLabel            = "infernex.io/component"
	defaultSinceMinutes       = 15
	maxSinceMinutes           = 24 * 60
	defaultMaxPods            = 50
	maxMaxPods                = 100
	defaultTailLines    int64 = 200
	maxTailLines        int64 = 1000
	perContainerBytes   int64 = 128 * 1024
	maxEvidence               = 200
	maxCollectionErrors       = 20
	maxMessageRunes           = 512
	incidentWindow            = 2 * time.Minute
)

type PodLogReader interface {
	Read(
		context.Context,
		string,
		string,
		string,
		time.Time,
		bool,
		int64,
		int64,
	) ([]byte, error)
}

type KubernetesLogReader struct {
	client kubernetes.Interface
}

func NewKubernetesLogReader(clientset kubernetes.Interface) *KubernetesLogReader {
	return &KubernetesLogReader{client: clientset}
}

func (r *KubernetesLogReader) Read(
	ctx context.Context,
	namespace string,
	pod string,
	container string,
	since time.Time,
	previous bool,
	tailLines int64,
	limitBytes int64,
) ([]byte, error) {
	if r == nil || r.client == nil {
		return nil, fmt.Errorf("Kubernetes log client is not configured")
	}
	request := r.client.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{
		Container:  container,
		Previous:   previous,
		SinceTime:  &metav1.Time{Time: since},
		Timestamps: true,
		TailLines:  &tailLines,
		LimitBytes: &limitBytes,
	})
	contents, err := request.DoRaw(ctx)
	if err != nil {
		return nil, err
	}
	return contents, nil
}

type Collector struct {
	client   client.Client
	logs     PodLogReader
	observer observer.Observer
	now      func() time.Time
}

func New(
	kubeClient client.Client,
	logs PodLogReader,
	domainObserver observer.Observer,
) (*Collector, error) {
	if kubeClient == nil {
		return nil, fmt.Errorf("Kubernetes client is required")
	}
	if logs == nil {
		return nil, fmt.Errorf("Pod log reader is required")
	}
	return &Collector{
		client: kubeClient, logs: logs, observer: domainObserver, now: time.Now,
	}, nil
}

func (c *Collector) Diagnose(ctx context.Context, request Request) (Report, error) {
	request, err := validateRequest(request)
	if err != nil {
		return Report{}, err
	}
	collectedAt := c.now().UTC()
	report := Report{
		Service:          ServiceReference{Namespace: request.Namespace, Name: request.Name},
		CollectedAt:      collectedAt,
		SinceMinutes:     request.SinceMinutes,
		Evidence:         make([]Evidence, 0),
		Incidents:        make([]Incident, 0),
		CollectionErrors: make([]string, 0),
		Events: observer.EventEvidence{
			Service:      observer.ServiceReference{Namespace: request.Namespace, Name: request.Name},
			SinceMinutes: request.SinceMinutes,
			Events:       make([]observer.EventSummary, 0),
		},
	}

	var pods corev1.PodList
	if err := c.client.List(
		ctx,
		&pods,
		client.InNamespace(request.Namespace),
		client.MatchingLabels{ownerLabel: request.Name},
	); err != nil {
		return Report{}, fmt.Errorf("list diagnostic Pods for %s/%s: %w", request.Namespace, request.Name, err)
	}
	sort.Slice(pods.Items, func(left, right int) bool {
		leftComponent := pods.Items[left].Labels[componentLabel]
		rightComponent := pods.Items[right].Labels[componentLabel]
		if leftComponent != rightComponent {
			return leftComponent < rightComponent
		}
		return pods.Items[left].Name < pods.Items[right].Name
	})
	report.TotalPods = len(pods.Items)
	if len(pods.Items) > request.MaxPods {
		pods.Items = pods.Items[:request.MaxPods]
		report.PodsTruncated = true
	}

	if c.observer != nil {
		events, eventErr := c.observer.GetEvents(
			ctx,
			request.Namespace,
			request.Name,
			request.SinceMinutes,
			100,
		)
		if eventErr != nil {
			report.addCollectionError("events: " + eventErr.Error())
		} else {
			report.Events = events
			report.Events.Events = append([]observer.EventSummary(nil), events.Events...)
			for index := range report.Events.Events {
				report.Events.Events[index].Reason = sanitizeMessage(report.Events.Events[index].Reason)
				report.Events.Events[index].Action = sanitizeMessage(report.Events.Events[index].Action)
				report.Events.Events[index].Note = sanitizeMessage(report.Events.Events[index].Note)
			}
			for _, event := range report.Events.Events {
				if evidence, ok := classifyEvent(event, collectedAt); ok {
					report.addEvidence(evidence)
				}
			}
		}
	}

	since := collectedAt.Add(-time.Duration(request.SinceMinutes) * time.Minute)
	for index := range pods.Items {
		if ctx.Err() != nil {
			return Report{}, ctx.Err()
		}
		pod := &pods.Items[index]
		statuses := containerStatuses(pod)
		containers := containerNames(pod)
		for _, containerName := range containers {
			contents, readErr := c.logs.Read(
				ctx,
				pod.Namespace,
				pod.Name,
				containerName,
				since,
				false,
				request.TailLines,
				perContainerBytes,
			)
			if readErr != nil {
				report.addCollectionError(fmt.Sprintf(
					"Pod/%s container/%s: %v", pod.Name, containerName, readErr,
				))
			} else {
				c.classifyLog(&report, pod, containerName, false, contents, collectedAt)
			}
			if statuses[containerName] <= 0 {
				continue
			}
			previous, previousErr := c.logs.Read(
				ctx,
				pod.Namespace,
				pod.Name,
				containerName,
				since,
				true,
				request.TailLines,
				perContainerBytes,
			)
			if previousErr != nil {
				report.addCollectionError(fmt.Sprintf(
					"Pod/%s previous container/%s: %v", pod.Name, containerName, previousErr,
				))
			} else {
				c.classifyLog(&report, pod, containerName, true, previous, collectedAt)
			}
		}
	}

	sort.SliceStable(report.Evidence, func(left, right int) bool {
		return report.Evidence[left].Timestamp.Before(report.Evidence[right].Timestamp)
	})
	report.Incidents = correlate(report.Service, report.Evidence)
	return report, nil
}

func (c *Collector) classifyLog(
	report *Report,
	pod *corev1.Pod,
	container string,
	previous bool,
	contents []byte,
	fallback time.Time,
) {
	if !utf8.Valid(contents) {
		report.addEvidence(Evidence{
			Timestamp: fallback,
			Source:    "pod-log",
			Category:  "output-corruption",
			Severity:  SeverityCritical,
			Component: pod.Labels[componentLabel],
			Node:      pod.Spec.NodeName,
			Pod:       pod.Name,
			Container: container,
			Previous:  previous,
			Message:   "container log contains invalid UTF-8 bytes",
		})
	}
	text := strings.ToValidUTF8(string(contents), "?")
	for _, line := range strings.Split(text, "\n") {
		timestamp, message := parseTimestampedLine(line, fallback)
		category, severity, matched := classifyMessage(message)
		if !matched {
			continue
		}
		report.addEvidence(Evidence{
			Timestamp: timestamp,
			Source:    "pod-log",
			Category:  category,
			Severity:  severity,
			Component: pod.Labels[componentLabel],
			Node:      pod.Spec.NodeName,
			Pod:       pod.Name,
			Container: container,
			Previous:  previous,
			Message:   sanitizeMessage(message),
		})
	}
}

func (r *Report) addEvidence(evidence Evidence) {
	if len(r.Evidence) >= maxEvidence {
		r.EvidenceTruncated = true
		return
	}
	r.Evidence = append(r.Evidence, evidence)
}

func (r *Report) addCollectionError(message string) {
	if len(r.CollectionErrors) >= maxCollectionErrors {
		return
	}
	r.CollectionErrors = append(r.CollectionErrors, sanitizeMessage(message))
}

func validateRequest(request Request) (Request, error) {
	request.Namespace = strings.TrimSpace(request.Namespace)
	request.Name = strings.TrimSpace(request.Name)
	if problems := validation.IsDNS1123Label(request.Namespace); len(problems) > 0 {
		return Request{}, fmt.Errorf("invalid namespace %q: %s", request.Namespace, strings.Join(problems, "; "))
	}
	if problems := validation.IsDNS1123Subdomain(request.Name); len(problems) > 0 {
		return Request{}, fmt.Errorf("invalid InferNexService name %q: %s", request.Name, strings.Join(problems, "; "))
	}
	if request.SinceMinutes <= 0 {
		request.SinceMinutes = defaultSinceMinutes
	}
	if request.SinceMinutes > maxSinceMinutes {
		return Request{}, fmt.Errorf("sinceMinutes must not exceed %d", maxSinceMinutes)
	}
	if request.MaxPods <= 0 {
		request.MaxPods = defaultMaxPods
	}
	if request.MaxPods > maxMaxPods {
		return Request{}, fmt.Errorf("maxPods must not exceed %d", maxMaxPods)
	}
	if request.TailLines <= 0 {
		request.TailLines = defaultTailLines
	}
	if request.TailLines > maxTailLines {
		return Request{}, fmt.Errorf("tailLines must not exceed %d", maxTailLines)
	}
	return request, nil
}

func containerNames(pod *corev1.Pod) []string {
	result := make([]string, 0, len(pod.Spec.InitContainers)+len(pod.Spec.Containers))
	for _, container := range pod.Spec.InitContainers {
		result = append(result, container.Name)
	}
	for _, container := range pod.Spec.Containers {
		result = append(result, container.Name)
	}
	return result
}

func containerStatuses(pod *corev1.Pod) map[string]int32 {
	result := make(map[string]int32)
	for _, status := range pod.Status.InitContainerStatuses {
		result[status.Name] = status.RestartCount
	}
	for _, status := range pod.Status.ContainerStatuses {
		result[status.Name] = status.RestartCount
	}
	return result
}

func parseTimestampedLine(line string, fallback time.Time) (time.Time, string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return fallback, ""
	}
	parts := strings.SplitN(line, " ", 2)
	if len(parts) == 2 {
		if timestamp, err := time.Parse(time.RFC3339Nano, parts[0]); err == nil {
			return timestamp.UTC(), strings.TrimSpace(parts[1])
		}
	}
	return fallback, line
}

type classificationRule struct {
	category       string
	severity       Severity
	pattern        *regexp.Regexp
	rank           int
	recommendation string
}

var classificationRules = []classificationRule{
	{
		category: "npu-device-failure", severity: SeverityCritical, rank: 0,
		pattern:        regexp.MustCompile(`(?i)(npu|ascend|acl|hbm|device).*(lost|fault|error|failed|out of memory|ecc)|(?:EZ|EE)\d{4}|rt(?:Stream|Device|Mem)[A-Za-z]*.*(?:error|fail)`),
		recommendation: "????????? NPU ?????/CANN ??????????????????",
	},
	{
		category: "collective-communication-failure", severity: SeverityCritical, rank: 1,
		pattern:        regexp.MustCompile(`(?i)(hccl|collective|all[-_ ]?reduce|rdma).*(timeout|abort|error|failed|disconnect|unreachable)`),
		recommendation: "??????? HCCL/RDMA/?????rank ?????????????????????",
	},
	{
		category: "resource-exhausted", severity: SeverityCritical, rank: 1,
		pattern:        regexp.MustCompile(`(?i)(out of memory|oomkilled|cannot allocate memory|resource exhausted|hbm allocation.*fail)`),
		recommendation: "??????????/????????? KV cache ????????????????",
	},
	{
		category: "kv-transport-failure", severity: SeverityCritical, rank: 2,
		pattern:        regexp.MustCompile(`(?i)(mooncake|nixl|kv.?cache|transfer engine).*(timeout|abort|error|failed|disconnect|corrupt)`),
		recommendation: "?? Mooncake/NIXL ?????KV connector ???????? prefill/decode ???????",
	},
	{
		category: "engine-worker-failure", severity: SeverityCritical, rank: 3,
		pattern:        regexp.MustCompile(`(?i)(engine core|vllm|worker|executor).*(died|dead|crash|abort|error|failed)|segmentation fault|sigsegv|process exited`),
		recommendation: "??????? engine/worker ????????? NPU?HCCL?Mooncake ?????????",
	},
	{
		category: "stream-interrupted", severity: SeverityCritical, rank: 5,
		pattern:        regexp.MustCompile(`(?i)(broken pipe|connection reset|unexpected eof|stream.*interrupt|response.*truncat|client disconnect|abort request|socket.*closed)`),
		recommendation: "? router/proxy ? decode engine ??????????????? worker ????????????????",
	},
	{
		category: "output-corruption", severity: SeverityCritical, rank: 5,
		pattern:        regexp.MustCompile(`(?i)(unicode.*decode|invalid utf-?8|invalid byte sequence|replacement character|mojibake|garbled|decode.*failed|json.*invalid)`),
		recommendation: "?????????tokenizer/???????????????????????????????????",
	},
	{
		category: "operation-timeout", severity: SeverityWarning, rank: 4,
		pattern:        regexp.MustCompile(`(?i)(deadline exceeded|operation timed out|request timeout|read timeout|startup timeout)`),
		recommendation: "????????????????????????????????????",
	},
}

var (
	bearerPattern        = regexp.MustCompile(`(?i)(authorization\s*:?\s*bearer\s+)([^\s,;]+)`)
	secretPattern        = regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password)(\s*[:=]\s*)([^\s,;]+)`)
	credentialURLPattern = regexp.MustCompile(`(?i)(https?://)[^/@\s]+:[^/@\s]+@`)
)

func classifyMessage(message string) (string, Severity, bool) {
	message = strings.TrimSpace(message)
	if message == "" {
		return "", "", false
	}
	for _, rule := range classificationRules {
		if rule.pattern.MatchString(message) {
			return rule.category, rule.severity, true
		}
	}
	return "", "", false
}

func classifyEvent(event observer.EventSummary, fallback time.Time) (Evidence, bool) {
	text := strings.TrimSpace(strings.Join([]string{event.Reason, event.Action, event.Note}, " "))
	category, severity, matched := classifyMessage(text)
	if !matched && strings.EqualFold(event.Type, corev1.EventTypeWarning) {
		category = "kubernetes-warning"
		severity = SeverityWarning
		matched = true
	}
	if !matched {
		return Evidence{}, false
	}
	timestamp := fallback
	if parsed, err := time.Parse(time.RFC3339, event.Timestamp); err == nil {
		timestamp = parsed.UTC()
	}
	return Evidence{
		Timestamp: timestamp,
		Source:    "kubernetes-event",
		Category:  category,
		Severity:  severity,
		Component: event.Component,
		Message:   sanitizeMessage(text),
	}, true
}

func sanitizeMessage(value string) string {
	value = bearerPattern.ReplaceAllString(value, "$1<redacted>")
	value = secretPattern.ReplaceAllString(value, "$1$2<redacted>")
	value = credentialURLPattern.ReplaceAllString(value, "$1<redacted>@")
	return bounded(strings.TrimSpace(value), maxMessageRunes)
}

func bounded(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit]) + "?"
}

func correlate(service ServiceReference, evidence []Evidence) []Incident {
	if len(evidence) == 0 {
		return []Incident{}
	}
	clusters := make([][]Evidence, 0)
	current := []Evidence{evidence[0]}
	for _, item := range evidence[1:] {
		if item.Timestamp.Sub(current[len(current)-1].Timestamp) > incidentWindow {
			clusters = append(clusters, current)
			current = []Evidence{item}
			continue
		}
		current = append(current, item)
	}
	clusters = append(clusters, current)

	incidents := make([]Incident, 0, len(clusters))
	for _, cluster := range clusters {
		root := cluster[0]
		rootRank := categoryRank(root.Category)
		severity := root.Severity
		components := make(map[string]struct{})
		nodes := make(map[string]struct{})
		pods := make(map[string]struct{})
		categories := make(map[string]struct{})
		for _, item := range cluster {
			if categoryRank(item.Category) < rootRank {
				root = item
				rootRank = categoryRank(item.Category)
			}
			if item.Severity == SeverityCritical {
				severity = SeverityCritical
			}
			addNonEmpty(components, item.Component)
			addNonEmpty(nodes, item.Node)
			addNonEmpty(pods, item.Pod)
			if item.Category != root.Category {
				categories[item.Category] = struct{}{}
			}
		}
		confidence := "medium"
		if len(components) > 1 || len(nodes) > 1 || len(categories) > 0 {
			confidence = "high"
		}
		visibleEvidence := cluster
		if len(visibleEvidence) > 20 {
			visibleEvidence = visibleEvidence[:20]
		}
		incidents = append(incidents, Incident{
			ID:             incidentID(service, root.Category, cluster[0].Timestamp),
			RootCategory:   root.Category,
			Severity:       severity,
			Confidence:     confidence,
			StartedAt:      cluster[0].Timestamp,
			EndedAt:        cluster[len(cluster)-1].Timestamp,
			Components:     sortedSet(components),
			Nodes:          sortedSet(nodes),
			Pods:           sortedSet(pods),
			Symptoms:       sortedSet(categories),
			Evidence:       append([]Evidence(nil), visibleEvidence...),
			Recommendation: categoryRecommendation(root.Category),
		})
	}
	return incidents
}

func Compare(baseline Report, candidate Report) Comparison {
	baselineCounts := criticalCategoryCounts(baseline)
	candidateCounts := criticalCategoryCounts(candidate)
	regressions := make([]string, 0)
	for category, count := range candidateCounts {
		if count > baselineCounts[category] {
			regressions = append(regressions, category)
		}
	}
	sort.Strings(regressions)
	return Comparison{
		Healthy:              len(regressions) == 0,
		BaselineCritical:     criticalIncidentCount(baseline),
		CandidateCritical:    criticalIncidentCount(candidate),
		RegressionCategories: regressions,
		Baseline:             baseline.Service,
		Candidate:            candidate.Service,
	}
}

func criticalCategoryCounts(report Report) map[string]int {
	result := make(map[string]int)
	for _, incident := range report.Incidents {
		if incident.Severity == SeverityCritical {
			result[incident.RootCategory]++
			for _, symptom := range incident.Symptoms {
				result[symptom]++
			}
		}
	}
	return result
}

func criticalIncidentCount(report Report) int {
	count := 0
	for _, incident := range report.Incidents {
		if incident.Severity == SeverityCritical {
			count++
		}
	}
	return count
}

func categoryRank(category string) int {
	for _, rule := range classificationRules {
		if rule.category == category {
			return rule.rank
		}
	}
	return 10
}

func categoryRecommendation(category string) string {
	for _, rule := range classificationRules {
		if rule.category == category {
			return rule.recommendation
		}
	}
	return "??????????????????? Kubernetes Event ??????"
}

func incidentID(service ServiceReference, category string, timestamp time.Time) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		service.Namespace, service.Name, category, timestamp.UTC().Format(time.RFC3339Nano),
	}, "\x00")))
	return hex.EncodeToString(sum[:8])
}

func addNonEmpty(values map[string]struct{}, value string) {
	if value = strings.TrimSpace(value); value != "" {
		values[value] = struct{}{}
	}
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
