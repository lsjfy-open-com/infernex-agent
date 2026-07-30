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

package observer

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	infernexv1alpha1 "gitcode.com/openFuyao/InferNex/api/v1alpha1"
)

const (
	ownerLabel          = "infernex.io/owner"
	componentLabel      = "infernex.io/component"
	maxServices         = 200
	maxPods             = 200
	defaultEventMinutes = 60
	maxEventMinutes     = 24 * 60
	defaultEventLimit   = 50
	maxEventLimit       = 200
	maxEventNoteRunes   = 512
)

type KubernetesObserver struct {
	client client.Client
	now    func() time.Time
}

func New(kubeClient client.Client) *KubernetesObserver {
	return &KubernetesObserver{client: kubeClient, now: time.Now}
}

func (o *KubernetesObserver) ListServices(ctx context.Context, namespace string) (ServiceList, error) {
	namespace, err := validateNamespace(namespace)
	if err != nil {
		return ServiceList{}, err
	}

	var services infernexv1alpha1.InferNexServiceList
	if err := o.client.List(ctx, &services, client.InNamespace(namespace)); err != nil {
		return ServiceList{}, fmt.Errorf("list InferNexServices in namespace %q: %w", namespace, err)
	}

	result := ServiceList{
		Namespace: namespace,
		Services:  make([]ServiceSummary, 0, len(services.Items)),
	}
	for index := range services.Items {
		result.Services = append(result.Services, summarizeService(&services.Items[index]))
	}
	sort.Slice(result.Services, func(left, right int) bool {
		return result.Services[left].Name < result.Services[right].Name
	})
	result.TotalServices = len(result.Services)
	if len(result.Services) > maxServices {
		result.Services = result.Services[:maxServices]
		result.ServicesTruncated = true
	}
	return result, nil
}

func (o *KubernetesObserver) InspectService(
	ctx context.Context,
	namespace string,
	name string,
) (ServiceDetail, error) {
	service, err := o.getService(ctx, namespace, name)
	if err != nil {
		return ServiceDetail{}, err
	}

	detail := ServiceDetail{Service: summarizeService(service)}
	if service.Spec.SourceRef != nil {
		sourceNamespace := strings.TrimSpace(service.Spec.SourceRef.Namespace)
		if sourceNamespace == "" {
			sourceNamespace = service.Namespace
		}
		detail.Source = &SourceSummary{
			APIVersion: service.Spec.SourceRef.APIVersion,
			Kind:       service.Spec.SourceRef.Kind,
			Namespace:  sourceNamespace,
			Name:       service.Spec.SourceRef.Name,
		}
	}
	for _, baseRef := range service.Spec.BaseRefs {
		if name := strings.TrimSpace(baseRef.Name); name != "" {
			detail.BaseRefs = append(detail.BaseRefs, name)
		}
	}
	return detail, nil
}

func (o *KubernetesObserver) GetTopology(
	ctx context.Context,
	namespace string,
	name string,
) (Topology, error) {
	service, err := o.getService(ctx, namespace, name)
	if err != nil {
		return Topology{}, err
	}

	selector := client.MatchingLabels{ownerLabel: service.Name}
	topology := Topology{
		Service:   summarizeService(service),
		Workloads: make([]WorkloadSummary, 0),
		Pods:      make([]PodSummary, 0),
	}

	var deployments appsv1.DeploymentList
	if err := o.client.List(ctx, &deployments, client.InNamespace(service.Namespace), selector); err != nil {
		return Topology{}, fmt.Errorf("list Deployments for InferNexService %q: %w", service.Name, err)
	}
	for index := range deployments.Items {
		deployment := &deployments.Items[index]
		desired := int32(1)
		if deployment.Spec.Replicas != nil {
			desired = *deployment.Spec.Replicas
		}
		topology.Workloads = append(topology.Workloads, WorkloadSummary{
			Kind:      "Deployment",
			Name:      deployment.Name,
			Component: deployment.Labels[componentLabel],
			Desired:   desired,
			Ready:     deployment.Status.ReadyReplicas,
		})
	}

	var daemonSets appsv1.DaemonSetList
	if err := o.client.List(ctx, &daemonSets, client.InNamespace(service.Namespace), selector); err != nil {
		return Topology{}, fmt.Errorf("list DaemonSets for InferNexService %q: %w", service.Name, err)
	}
	for index := range daemonSets.Items {
		daemonSet := &daemonSets.Items[index]
		topology.Workloads = append(topology.Workloads, WorkloadSummary{
			Kind:      "DaemonSet",
			Name:      daemonSet.Name,
			Component: daemonSet.Labels[componentLabel],
			Desired:   daemonSet.Status.DesiredNumberScheduled,
			Ready:     daemonSet.Status.NumberReady,
		})
	}

	var leaderWorkerSets lwsv1.LeaderWorkerSetList
	if err := o.client.List(ctx, &leaderWorkerSets, client.InNamespace(service.Namespace), selector); err != nil {
		return Topology{}, fmt.Errorf("list LeaderWorkerSets for InferNexService %q: %w", service.Name, err)
	}
	for index := range leaderWorkerSets.Items {
		leaderWorkerSet := &leaderWorkerSets.Items[index]
		desired := int32(1)
		if leaderWorkerSet.Spec.Replicas != nil {
			desired = *leaderWorkerSet.Spec.Replicas
		}
		topology.Workloads = append(topology.Workloads, WorkloadSummary{
			Kind:      "LeaderWorkerSet",
			Name:      leaderWorkerSet.Name,
			Component: leaderWorkerSet.Labels[componentLabel],
			Desired:   desired,
			Ready:     leaderWorkerSet.Status.ReadyReplicas,
			GroupSize: leaderWorkerSet.Spec.LeaderWorkerTemplate.Size,
		})
	}

	var pods corev1.PodList
	if err := o.client.List(ctx, &pods, client.InNamespace(service.Namespace), selector); err != nil {
		return Topology{}, fmt.Errorf("list Pods for InferNexService %q: %w", service.Name, err)
	}
	for index := range pods.Items {
		topology.Pods = append(topology.Pods, summarizePod(&pods.Items[index]))
	}

	sort.Slice(topology.Workloads, func(left, right int) bool {
		if topology.Workloads[left].Component != topology.Workloads[right].Component {
			return topology.Workloads[left].Component < topology.Workloads[right].Component
		}
		if topology.Workloads[left].Kind != topology.Workloads[right].Kind {
			return topology.Workloads[left].Kind < topology.Workloads[right].Kind
		}
		return topology.Workloads[left].Name < topology.Workloads[right].Name
	})
	sort.Slice(topology.Pods, func(left, right int) bool {
		if topology.Pods[left].Component != topology.Pods[right].Component {
			return topology.Pods[left].Component < topology.Pods[right].Component
		}
		return topology.Pods[left].Name < topology.Pods[right].Name
	})
	topology.TotalPods = len(topology.Pods)
	if len(topology.Pods) > maxPods {
		topology.Pods = topology.Pods[:maxPods]
		topology.PodsTruncated = true
	}
	return topology, nil
}

func (o *KubernetesObserver) GetEvents(
	ctx context.Context,
	namespace string,
	name string,
	sinceMinutes int,
	limit int,
) (EventEvidence, error) {
	service, err := o.getService(ctx, namespace, name)
	if err != nil {
		return EventEvidence{}, err
	}
	sinceMinutes, limit, err = validateEventWindow(sinceMinutes, limit)
	if err != nil {
		return EventEvidence{}, err
	}

	relatedByUID := make(map[types.UID]relatedObject)
	relatedByKey := make(map[string]relatedObject)
	addRelatedObject(relatedByUID, relatedByKey, relatedObject{
		UID: service.UID, Kind: "InferNexService", Name: service.Name,
	})

	selector := client.MatchingLabels{ownerLabel: service.Name}
	var deployments appsv1.DeploymentList
	if err := o.client.List(ctx, &deployments, client.InNamespace(service.Namespace), selector); err != nil {
		return EventEvidence{}, fmt.Errorf("list Deployments for InferNexService events %q: %w", service.Name, err)
	}
	for index := range deployments.Items {
		item := &deployments.Items[index]
		addRelatedObject(relatedByUID, relatedByKey, relatedObject{
			UID: item.UID, Kind: "Deployment", Name: item.Name, Component: item.Labels[componentLabel],
		})
	}

	var daemonSets appsv1.DaemonSetList
	if err := o.client.List(ctx, &daemonSets, client.InNamespace(service.Namespace), selector); err != nil {
		return EventEvidence{}, fmt.Errorf("list DaemonSets for InferNexService events %q: %w", service.Name, err)
	}
	for index := range daemonSets.Items {
		item := &daemonSets.Items[index]
		addRelatedObject(relatedByUID, relatedByKey, relatedObject{
			UID: item.UID, Kind: "DaemonSet", Name: item.Name, Component: item.Labels[componentLabel],
		})
	}

	var leaderWorkerSets lwsv1.LeaderWorkerSetList
	if err := o.client.List(ctx, &leaderWorkerSets, client.InNamespace(service.Namespace), selector); err != nil {
		return EventEvidence{}, fmt.Errorf("list LeaderWorkerSets for InferNexService events %q: %w", service.Name, err)
	}
	for index := range leaderWorkerSets.Items {
		item := &leaderWorkerSets.Items[index]
		addRelatedObject(relatedByUID, relatedByKey, relatedObject{
			UID: item.UID, Kind: "LeaderWorkerSet", Name: item.Name, Component: item.Labels[componentLabel],
		})
	}

	var pods corev1.PodList
	if err := o.client.List(ctx, &pods, client.InNamespace(service.Namespace), selector); err != nil {
		return EventEvidence{}, fmt.Errorf("list Pods for InferNexService events %q: %w", service.Name, err)
	}
	for index := range pods.Items {
		item := &pods.Items[index]
		addRelatedObject(relatedByUID, relatedByKey, relatedObject{
			UID: item.UID, Kind: "Pod", Name: item.Name, Component: item.Labels[componentLabel],
		})
	}

	var events corev1.EventList
	if err := o.client.List(ctx, &events, client.InNamespace(service.Namespace)); err != nil {
		return EventEvidence{}, fmt.Errorf("list Events for InferNexService %q: %w", service.Name, err)
	}

	cutoff := o.now().UTC().Add(-time.Duration(sinceMinutes) * time.Minute)
	result := EventEvidence{
		Service:      ServiceReference{Namespace: service.Namespace, Name: service.Name},
		SinceMinutes: sinceMinutes,
		Events:       make([]EventSummary, 0),
	}
	for index := range events.Items {
		event := &events.Items[index]
		related, ok := relatedByUID[event.InvolvedObject.UID]
		if !ok && event.InvolvedObject.UID == "" {
			related, ok = relatedByKey[objectKey(event.InvolvedObject.Kind, event.InvolvedObject.Name)]
		}
		if !ok {
			continue
		}

		timestamp := eventTimestamp(event)
		if !timestamp.IsZero() && timestamp.Before(cutoff) {
			continue
		}
		count := event.Count
		if count == 0 {
			count = 1
		}
		reporter := strings.TrimSpace(event.ReportingController)
		if reporter == "" {
			reporter = strings.TrimSpace(event.Source.Component)
		}
		summary := EventSummary{
			Type:      event.Type,
			Reason:    event.Reason,
			Action:    event.Action,
			Note:      boundedEventNote(event.Message),
			Count:     count,
			Reporter:  reporter,
			Kind:      related.Kind,
			Name:      related.Name,
			Component: related.Component,
		}
		if !timestamp.IsZero() {
			summary.Timestamp = timestamp.UTC().Format(time.RFC3339)
		}
		result.Events = append(result.Events, summary)
	}

	sort.SliceStable(result.Events, func(left, right int) bool {
		return result.Events[left].Timestamp > result.Events[right].Timestamp
	})
	result.TotalEvents = len(result.Events)
	if len(result.Events) > limit {
		result.Events = result.Events[:limit]
		result.EventsTruncated = true
	}
	return result, nil
}

func (o *KubernetesObserver) getService(
	ctx context.Context,
	namespace string,
	name string,
) (*infernexv1alpha1.InferNexService, error) {
	namespace, err := validateNamespace(namespace)
	if err != nil {
		return nil, err
	}
	name, err = validateName(name)
	if err != nil {
		return nil, err
	}

	service := &infernexv1alpha1.InferNexService{}
	if err := o.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, service); err != nil {
		return nil, fmt.Errorf("get InferNexService %s/%s: %w", namespace, name, err)
	}
	return service, nil
}

func summarizeService(service *infernexv1alpha1.InferNexService) ServiceSummary {
	summary := ServiceSummary{
		Namespace:          service.Namespace,
		Name:               service.Name,
		Mode:               service.Status.Mode,
		Ready:              service.Status.Ready,
		Generation:         service.Generation,
		ObservedGeneration: service.Status.ObservedGeneration,
	}
	if service.Spec.Model != nil {
		summary.Model = &ModelSummary{
			Name: service.Spec.Model.Name,
			URI:  sanitizedURI(service.Spec.Model.URI),
		}
	}
	summary.Components = summarizeComponents(service.Status.Components)
	for _, condition := range service.Status.Conditions {
		item := ConditionSummary{
			Type:               condition.Type,
			Status:             string(condition.Status),
			Reason:             condition.Reason,
			Message:            condition.Message,
			ObservedGeneration: condition.ObservedGeneration,
		}
		if !condition.LastTransitionTime.IsZero() {
			item.LastTransitionTime = condition.LastTransitionTime.UTC().Format("2006-01-02T15:04:05Z")
		}
		summary.Conditions = append(summary.Conditions, item)
	}
	sort.Slice(summary.Components, func(left, right int) bool {
		return summary.Components[left].Name < summary.Components[right].Name
	})
	sort.Slice(summary.Conditions, func(left, right int) bool {
		return summary.Conditions[left].Type < summary.Conditions[right].Type
	})
	return summary
}

func summarizeComponents(
	statuses *infernexv1alpha1.InferNexComponentStatuses,
) []ComponentSummary {
	if statuses == nil {
		return nil
	}
	components := make([]ComponentSummary, 0, 7)
	appendStatus := func(name string, status *infernexv1alpha1.ComponentStatus) {
		if status != nil {
			components = append(components, ComponentSummary{
				Name: name, Ready: status.Ready, Message: status.Message,
			})
		}
	}
	appendStatus("inference-engine", statuses.InferenceEngine)
	appendStatus("hermes-router", statuses.HermesRouter)
	appendStatus("proxy-server", statuses.ProxyServer)
	appendStatus("cache-indexer", statuses.CacheIndexer)
	appendStatus("mooncake", statuses.Mooncake)
	appendStatus("pd-orchestrator", statuses.PDOrchestrator)
	appendStatus("eagle-eye", statuses.EagleEye)
	return components
}

func summarizePod(pod *corev1.Pod) PodSummary {
	summary := PodSummary{
		Name:      pod.Name,
		Component: pod.Labels[componentLabel],
		Node:      pod.Spec.NodeName,
		Phase:     string(pod.Status.Phase),
		Reason:    pod.Status.Reason,
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			summary.Ready = true
			break
		}
	}
	for _, container := range pod.Status.ContainerStatuses {
		summary.Restarts += container.RestartCount
		if summary.Reason == "" && container.State.Waiting != nil {
			summary.Reason = container.State.Waiting.Reason
		}
		if summary.Reason == "" && container.State.Terminated != nil {
			if container.State.Terminated.Reason != "" {
				summary.Reason = container.State.Terminated.Reason
			} else {
				summary.Reason = "ExitCode" + strconv.Itoa(int(container.State.Terminated.ExitCode))
			}
		}
	}
	if pod.DeletionTimestamp != nil {
		summary.Reason = "Terminating"
	}
	return summary
}

func sanitizedURI(value string) string {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil {
		return value
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func validateNamespace(value string) (string, error) {
	namespace := strings.TrimSpace(value)
	if problems := validation.IsDNS1123Label(namespace); len(problems) > 0 {
		return "", fmt.Errorf("invalid namespace %q: %s", namespace, strings.Join(problems, "; "))
	}
	return namespace, nil
}

func validateName(value string) (string, error) {
	name := strings.TrimSpace(value)
	if problems := validation.IsDNS1123Subdomain(name); len(problems) > 0 {
		return "", fmt.Errorf("invalid InferNexService name %q: %s", name, strings.Join(problems, "; "))
	}
	return name, nil
}

type relatedObject struct {
	UID       types.UID
	Kind      string
	Name      string
	Component string
}

func addRelatedObject(
	byUID map[types.UID]relatedObject,
	byKey map[string]relatedObject,
	object relatedObject,
) {
	if object.UID != "" {
		byUID[object.UID] = object
	}
	byKey[objectKey(object.Kind, object.Name)] = object
}

func objectKey(kind string, name string) string {
	return strings.ToLower(strings.TrimSpace(kind)) + "/" + strings.TrimSpace(name)
}

func eventTimestamp(event *corev1.Event) time.Time {
	if !event.EventTime.IsZero() {
		return event.EventTime.Time
	}
	if !event.LastTimestamp.IsZero() {
		return event.LastTimestamp.Time
	}
	if !event.FirstTimestamp.IsZero() {
		return event.FirstTimestamp.Time
	}
	return event.CreationTimestamp.Time
}

func boundedEventNote(value string) string {
	value = strings.Map(func(character rune) rune {
		if character == '\n' || character == '\r' || character == '\t' {
			return ' '
		}
		if character < 0x20 || character == 0x7f {
			return -1
		}
		return character
	}, strings.TrimSpace(value))
	runes := []rune(value)
	if len(runes) <= maxEventNoteRunes {
		return value
	}
	return string(runes[:maxEventNoteRunes]) + "..."
}

func validateEventWindow(sinceMinutes int, limit int) (int, int, error) {
	if sinceMinutes == 0 {
		sinceMinutes = defaultEventMinutes
	}
	if sinceMinutes < 1 || sinceMinutes > maxEventMinutes {
		return 0, 0, fmt.Errorf(
			"sinceMinutes must be between 1 and %d", maxEventMinutes,
		)
	}
	if limit == 0 {
		limit = defaultEventLimit
	}
	if limit < 1 || limit > maxEventLimit {
		return 0, 0, fmt.Errorf("limit must be between 1 and %d", maxEventLimit)
	}
	return sinceMinutes, limit, nil
}
