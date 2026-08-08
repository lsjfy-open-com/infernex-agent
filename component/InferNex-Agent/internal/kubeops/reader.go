/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 * You can use this software according to the terms and conditions of the Mulan PSL v2.
 * You may obtain a copy of the License at:
 *          http://license.coscl.org.cn/MulanPSL2
 * THIS SOFTWARE IS PROVIDED ON AN "AS IS" BASIS, WITHOUT WARRANTIES OF ANY KIND.
 */

package kubeops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/metadata"
	"sigs.k8s.io/controller-runtime/pkg/client"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/diagnostics"
)

const (
	defaultListLimit   = 100
	maxListLimit       = 300
	defaultEventLimit  = 100
	maxEventLimit      = 300
	defaultEventWindow = 60
	defaultLogWindow   = 30
	maxWindowMinutes   = 24 * 60
	defaultTailLines   = int64(200)
	maxTailLines       = int64(1000)
	perLogBytes        = int64(128 * 1024)
	maxLogContainers   = 10
)

var (
	bearerPattern        = regexp.MustCompile(`(?i)(authorization\s*:?\s*bearer\s+)([^\s,;]+)`)
	secretPattern        = regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password)(\s*[:=]\s*)([^\s,;]+)`)
	credentialURLPattern = regexp.MustCompile(`(?i)(https?://)[^/@\s]+:[^/@\s]+@`)
)

type KubernetesReader struct {
	client    client.Client
	discovery discovery.DiscoveryInterface
	metadata  metadata.Interface
	logs      diagnostics.PodLogReader
	apiServer string
	now       func() time.Time
}

func New(
	kubeClient client.Client,
	discoveryClient discovery.DiscoveryInterface,
	metadataClient metadata.Interface,
	logs diagnostics.PodLogReader,
	apiServer string,
) (*KubernetesReader, error) {
	if kubeClient == nil || discoveryClient == nil || metadataClient == nil || logs == nil {
		return nil, fmt.Errorf("Kubernetes object, discovery, metadata, and log clients are required")
	}
	return &KubernetesReader{
		client: kubeClient, discovery: discoveryClient, metadata: metadataClient,
		logs: logs, apiServer: sanitize(apiServer, 512), now: time.Now,
	}, nil
}

func (r *KubernetesReader) DetectEnvironment(ctx context.Context) (Environment, error) {
	result := Environment{
		Platform: "kubernetes", APIServer: r.apiServer,
		ClusterRoles: []string{}, Namespaces: []string{},
		Capabilities: map[string]bool{}, Evidence: []string{},
		Recommendations: []string{}, Warnings: []string{},
	}
	if info, err := r.discovery.ServerVersion(); err != nil {
		result.Warnings = append(result.Warnings, "Kubernetes version discovery: "+sanitize(err.Error(), 256))
	} else {
		result.Kubernetes = info.GitVersion
	}

	checks := []struct {
		name, groupVersion, resource string
	}{
		{"bkeClusterLifecycle", "bke.bocloud.com/v1beta1", "bkeclusters"},
		{"bkeNodeLifecycle", "bke.bocloud.com/v1beta1", "bkenodes"},
		{"infernexBridge", "infernex.infernex.io/v1alpha1", "infernexservices"},
		{"kserveLLMInferenceServiceV1Alpha2", "serving.kserve.io/v1alpha2", "llminferenceservices"},
		{"leaderWorkerSet", "leaderworkerset.x-k8s.io/v1", "leaderworkersets"},
		{"gatewayAPI", "gateway.networking.k8s.io/v1", "gateways"},
		{"resourceScalingGroup", "autoscaling.openfuyao.com/v1alpha1", "resourcescalinggroups"},
		{"serviceMonitor", "monitoring.coreos.com/v1", "servicemonitors"},
	}
	for _, check := range checks {
		available, warning := r.hasResource(check.groupVersion, check.resource)
		result.Capabilities[check.name] = available
		if warning != "" {
			result.Warnings = append(result.Warnings, warning)
		}
	}

	var namespaces corev1.NamespaceList
	if err := r.client.List(ctx, &namespaces); err != nil {
		result.Warnings = append(result.Warnings, "list namespaces: "+sanitize(err.Error(), 256))
	} else {
		interesting := map[string]bool{
			"cluster-system": false, "openfuyao-system": false,
			"infernex-bridge-system": false, "ai-inference": false,
			"eagle-eye": false, "nats": false, "scaling-system": false,
		}
		for _, namespace := range namespaces.Items {
			if _, ok := interesting[namespace.Name]; ok {
				interesting[namespace.Name] = true
				result.Namespaces = append(result.Namespaces, namespace.Name)
			}
		}
		sort.Strings(result.Namespaces)
		if interesting["cluster-system"] || result.Capabilities["bkeClusterLifecycle"] {
			result.Platform = "openfuyao"
			result.ClusterRoles = append(result.ClusterRoles, "bootstrap-or-management-control-plane")
			result.Evidence = append(result.Evidence, "BKE/Cluster API lifecycle resources or cluster-system namespace detected")
		}
		if interesting["openfuyao-system"] {
			result.Platform = "openfuyao"
			result.ClusterRoles = append(result.ClusterRoles, "openfuyao-platform")
			result.Evidence = append(result.Evidence, "openfuyao-system namespace detected")
		}
		if interesting["ai-inference"] || interesting["eagle-eye"] || result.Capabilities["leaderWorkerSet"] {
			result.ClusterRoles = append(result.ClusterRoles, "inference-business-cluster")
			result.Evidence = append(result.Evidence, "inference namespaces or LeaderWorkerSet API detected")
		}
		if interesting["infernex-bridge-system"] || result.Capabilities["infernexBridge"] {
			result.ClusterRoles = append(result.ClusterRoles, "infernex-bridge-control-plane")
			result.Evidence = append(result.Evidence, "InferNex Bridge namespace or CRD detected")
		}
	}
	if len(result.ClusterRoles) == 0 {
		result.ClusterRoles = append(result.ClusterRoles, "kubernetes-cluster")
		result.Evidence = append(result.Evidence, "no openFuyao-specific control-plane marker was visible")
	}
	result.Recommendations = append(result.Recommendations,
		"Treat this result as the single cluster selected by the active kubeconfig; bootstrap, management, and business clusters may require different kubeconfigs.",
		"Use Kubernetes and Helm inventory before choosing InferNex main-Chart or optional Bridge-specific operations.",
		"Reuse infernex-checker for NPU, HCCS/RoCE, CoreDNS, model-path, and Driver/CANN preflight checks.",
	)
	return result, nil
}

func (r *KubernetesReader) hasResource(groupVersion, resource string) (bool, string) {
	resources, err := r.discovery.ServerResourcesForGroupVersion(groupVersion)
	if err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return false, ""
		}
		return false, fmt.Sprintf("discover %s: %s", groupVersion, sanitize(err.Error(), 256))
	}
	for _, apiResource := range resources.APIResources {
		if apiResource.Name == resource {
			return true, ""
		}
	}
	return false, ""
}

func (r *KubernetesReader) ClusterOverview(ctx context.Context) (ClusterOverview, error) {
	result := ClusterOverview{APIServer: r.apiServer, Nodes: []NodeSummary{}, Warnings: []string{}}
	if info, err := r.discovery.ServerVersion(); err != nil {
		result.Warnings = append(result.Warnings, "Kubernetes version discovery: "+sanitize(err.Error(), 256))
	} else {
		result.KubernetesVersion = info.GitVersion
	}
	var namespaces corev1.NamespaceList
	if err := r.client.List(ctx, &namespaces); err != nil {
		result.Warnings = append(result.Warnings, "list namespaces: "+sanitize(err.Error(), 256))
	} else {
		result.NamespaceCount = len(namespaces.Items)
	}
	var nodes corev1.NodeList
	if err := r.client.List(ctx, &nodes); err != nil {
		result.Warnings = append(result.Warnings, "list nodes: "+sanitize(err.Error(), 256))
	} else {
		for index := range nodes.Items {
			result.Nodes = append(result.Nodes, summarizeNode(&nodes.Items[index]))
		}
		sort.Slice(result.Nodes, func(i, j int) bool { return result.Nodes[i].Name < result.Nodes[j].Name })
	}
	var pods corev1.PodList
	if err := r.client.List(ctx, &pods); err != nil {
		result.Warnings = append(result.Warnings, "list pods: "+sanitize(err.Error(), 256))
	} else {
		result.PodCount = len(pods.Items)
		for index := range pods.Items {
			pod := &pods.Items[index]
			if podReady(pod) {
				result.ReadyPodCount++
			}
			switch pod.Status.Phase {
			case corev1.PodPending:
				result.PendingPodCount++
			case corev1.PodFailed:
				result.FailedPodCount++
			}
		}
	}
	return result, nil
}

func summarizeNode(node *corev1.Node) NodeSummary {
	result := NodeSummary{
		Name: node.Name, OS: node.Status.NodeInfo.OSImage,
		Architecture: node.Status.NodeInfo.Architecture,
		Kubelet:      node.Status.NodeInfo.KubeletVersion,
		Capacity:     map[string]string{}, Allocatable: map[string]string{}, Taints: []string{},
	}
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			result.Ready = condition.Status == corev1.ConditionTrue
			break
		}
	}
	for name, quantity := range node.Status.Capacity {
		if acceleratorResource(string(name)) {
			result.Capacity[string(name)] = quantity.String()
		}
	}
	for name, quantity := range node.Status.Allocatable {
		if acceleratorResource(string(name)) {
			result.Allocatable[string(name)] = quantity.String()
		}
	}
	for _, taint := range node.Spec.Taints {
		result.Taints = append(result.Taints, fmt.Sprintf("%s=%s:%s", taint.Key, taint.Value, taint.Effect))
	}
	return result
}

func acceleratorResource(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "ascend") || strings.Contains(lower, "npu") || strings.Contains(lower, "gpu")
}

func (r *KubernetesReader) ListWorkloads(ctx context.Context, request WorkloadRequest) (WorkloadInventory, error) {
	request.Namespace = strings.TrimSpace(request.Namespace)
	request.LabelSelector = strings.TrimSpace(request.LabelSelector)
	if err := validateNamespace(request.Namespace, true); err != nil {
		return WorkloadInventory{}, err
	}
	selector := labels.Everything()
	if request.LabelSelector != "" {
		parsed, err := labels.Parse(request.LabelSelector)
		if err != nil {
			return WorkloadInventory{}, fmt.Errorf("invalid labelSelector: %w", err)
		}
		selector = parsed
	}
	limit, err := normalizeLimit(request.Limit, defaultListLimit, maxListLimit)
	if err != nil {
		return WorkloadInventory{}, err
	}
	result := WorkloadInventory{
		Namespace: request.Namespace, LabelSelector: request.LabelSelector,
		Workloads: []WorkloadSummary{}, Pods: []PodSummary{}, Services: []ServiceSummary{}, Warnings: []string{},
	}
	options := []client.ListOption{client.MatchingLabelsSelector{Selector: selector}}
	if request.Namespace != "" {
		options = append(options, client.InNamespace(request.Namespace))
	}

	var deployments appsv1.DeploymentList
	if listErr := r.client.List(ctx, &deployments, options...); listErr != nil {
		result.Warnings = append(result.Warnings, "list Deployments: "+sanitize(listErr.Error(), 256))
	} else {
		for index := range deployments.Items {
			item := &deployments.Items[index]
			result.Workloads = append(result.Workloads, WorkloadSummary{
				Kind: "Deployment", Namespace: item.Namespace, Name: item.Name,
				Desired: valueOrOne(item.Spec.Replicas), Ready: item.Status.ReadyReplicas,
				Available: item.Status.AvailableReplicas, Images: podSpecImages(&item.Spec.Template.Spec),
				Selector: metav1.FormatLabelSelector(item.Spec.Selector), HelmRelease: helmRelease(item), Labels: selectedLabels(item.Labels),
			})
		}
	}
	var statefulSets appsv1.StatefulSetList
	if listErr := r.client.List(ctx, &statefulSets, options...); listErr != nil {
		result.Warnings = append(result.Warnings, "list StatefulSets: "+sanitize(listErr.Error(), 256))
	} else {
		for index := range statefulSets.Items {
			item := &statefulSets.Items[index]
			result.Workloads = append(result.Workloads, WorkloadSummary{
				Kind: "StatefulSet", Namespace: item.Namespace, Name: item.Name,
				Desired: valueOrOne(item.Spec.Replicas), Ready: item.Status.ReadyReplicas,
				Available: item.Status.AvailableReplicas, Images: podSpecImages(&item.Spec.Template.Spec),
				Selector: metav1.FormatLabelSelector(item.Spec.Selector), HelmRelease: helmRelease(item), Labels: selectedLabels(item.Labels),
			})
		}
	}
	var daemonSets appsv1.DaemonSetList
	if listErr := r.client.List(ctx, &daemonSets, options...); listErr != nil {
		result.Warnings = append(result.Warnings, "list DaemonSets: "+sanitize(listErr.Error(), 256))
	} else {
		for index := range daemonSets.Items {
			item := &daemonSets.Items[index]
			result.Workloads = append(result.Workloads, WorkloadSummary{
				Kind: "DaemonSet", Namespace: item.Namespace, Name: item.Name,
				Desired: item.Status.DesiredNumberScheduled, Ready: item.Status.NumberReady,
				Available: item.Status.NumberAvailable, Images: podSpecImages(&item.Spec.Template.Spec),
				Selector: metav1.FormatLabelSelector(item.Spec.Selector), HelmRelease: helmRelease(item), Labels: selectedLabels(item.Labels),
			})
		}
	}
	var leaderWorkerSets lwsv1.LeaderWorkerSetList
	if listErr := r.client.List(ctx, &leaderWorkerSets, options...); listErr != nil {
		if !apierrors.IsNotFound(listErr) && !meta.IsNoMatchError(listErr) {
			result.Warnings = append(result.Warnings, "list LeaderWorkerSets: "+sanitize(listErr.Error(), 256))
		}
	} else {
		for index := range leaderWorkerSets.Items {
			item := &leaderWorkerSets.Items[index]
			images := podSpecImages(&item.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec)
			if item.Spec.LeaderWorkerTemplate.LeaderTemplate != nil {
				images = mergeStrings(images, podSpecImages(&item.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec))
			}
			result.Workloads = append(result.Workloads, WorkloadSummary{
				Kind: "LeaderWorkerSet", Namespace: item.Namespace, Name: item.Name,
				Desired: valueOrOne(item.Spec.Replicas), Ready: item.Status.ReadyReplicas,
				Available: item.Status.ReadyReplicas, Images: images, Selector: item.Status.HPAPodSelector,
				HelmRelease: helmRelease(item), Labels: selectedLabels(item.Labels),
			})
		}
	}
	var pods corev1.PodList
	if listErr := r.client.List(ctx, &pods, options...); listErr != nil {
		result.Warnings = append(result.Warnings, "list Pods: "+sanitize(listErr.Error(), 256))
	} else {
		for index := range pods.Items {
			result.Pods = append(result.Pods, summarizePod(&pods.Items[index]))
		}
	}
	var services corev1.ServiceList
	if listErr := r.client.List(ctx, &services, options...); listErr != nil {
		result.Warnings = append(result.Warnings, "list Services: "+sanitize(listErr.Error(), 256))
	} else {
		for index := range services.Items {
			result.Services = append(result.Services, summarizeService(&services.Items[index]))
		}
	}
	sort.Slice(result.Workloads, func(i, j int) bool {
		return inventoryKey(result.Workloads[i].Namespace, result.Workloads[i].Kind, result.Workloads[i].Name) < inventoryKey(result.Workloads[j].Namespace, result.Workloads[j].Kind, result.Workloads[j].Name)
	})
	sort.Slice(result.Pods, func(i, j int) bool {
		return inventoryKey(result.Pods[i].Namespace, "Pod", result.Pods[i].Name) < inventoryKey(result.Pods[j].Namespace, "Pod", result.Pods[j].Name)
	})
	sort.Slice(result.Services, func(i, j int) bool {
		return inventoryKey(result.Services[i].Namespace, "Service", result.Services[i].Name) < inventoryKey(result.Services[j].Namespace, "Service", result.Services[j].Name)
	})
	result.Total = len(result.Workloads) + len(result.Pods) + len(result.Services)
	remaining := limit
	result.Workloads, remaining = truncateWorkloads(result.Workloads, remaining)
	result.Pods, remaining = truncatePods(result.Pods, remaining)
	result.Services, _ = truncateServices(result.Services, remaining)
	result.Truncated = result.Total > limit
	return result, nil
}

func (r *KubernetesReader) GetEvents(ctx context.Context, request EventRequest) (EventList, error) {
	request.Namespace = strings.TrimSpace(request.Namespace)
	request.Kind = strings.TrimSpace(request.Kind)
	request.Name = strings.TrimSpace(request.Name)
	if err := validateNamespace(request.Namespace, true); err != nil {
		return EventList{}, err
	}
	if request.Name == "" && request.Kind != "" {
		return EventList{}, fmt.Errorf("kind requires name")
	}
	if request.Name != "" && request.Kind == "" {
		return EventList{}, fmt.Errorf("name requires kind")
	}
	window, err := normalizeWindow(request.SinceMinutes, defaultEventWindow)
	if err != nil {
		return EventList{}, err
	}
	limit, err := normalizeLimit(request.Limit, defaultEventLimit, maxEventLimit)
	if err != nil {
		return EventList{}, err
	}
	options := []client.ListOption{}
	if request.Namespace != "" {
		options = append(options, client.InNamespace(request.Namespace))
	}
	var events corev1.EventList
	if err := r.client.List(ctx, &events, options...); err != nil {
		return EventList{}, fmt.Errorf("list Kubernetes Events: %w", err)
	}
	cutoff := r.now().UTC().Add(-time.Duration(window) * time.Minute)
	result := EventList{Namespace: request.Namespace, SinceMinutes: window, Events: []EventSummary{}}
	for index := range events.Items {
		event := &events.Items[index]
		if request.Kind != "" && (!strings.EqualFold(event.InvolvedObject.Kind, request.Kind) || event.InvolvedObject.Name != request.Name) {
			continue
		}
		timestamp := eventTimestamp(event)
		if timestamp.Before(cutoff) {
			continue
		}
		result.Events = append(result.Events, EventSummary{
			Timestamp: timestamp.Format(time.RFC3339), Type: event.Type,
			Reason: sanitize(event.Reason, 128),
			Object: fmt.Sprintf("%s/%s", event.InvolvedObject.Kind, event.InvolvedObject.Name),
			Source: sanitize(event.Source.Component, 128), Count: event.Count,
			Message: sanitize(event.Message, 512),
		})
	}
	sort.SliceStable(result.Events, func(i, j int) bool { return result.Events[i].Timestamp > result.Events[j].Timestamp })
	result.Total = len(result.Events)
	if len(result.Events) > limit {
		result.Events = result.Events[:limit]
		result.Truncated = true
	}
	return result, nil
}

func (r *KubernetesReader) GetPodLogs(ctx context.Context, request PodLogRequest) (PodLogResult, error) {
	request.Namespace = strings.TrimSpace(request.Namespace)
	request.Pod = strings.TrimSpace(request.Pod)
	request.Container = strings.TrimSpace(request.Container)
	if err := validateNamespace(request.Namespace, false); err != nil {
		return PodLogResult{}, err
	}
	if problems := validation.IsDNS1123Subdomain(request.Pod); len(problems) > 0 {
		return PodLogResult{}, fmt.Errorf("invalid pod name %q: %s", request.Pod, strings.Join(problems, "; "))
	}
	window, err := normalizeWindow(request.SinceMinutes, defaultLogWindow)
	if err != nil {
		return PodLogResult{}, err
	}
	if request.TailLines <= 0 {
		request.TailLines = defaultTailLines
	}
	if request.TailLines > maxTailLines {
		return PodLogResult{}, fmt.Errorf("tailLines must not exceed %d", maxTailLines)
	}
	var pod corev1.Pod
	if err := r.client.Get(ctx, client.ObjectKey{Namespace: request.Namespace, Name: request.Pod}, &pod); err != nil {
		return PodLogResult{}, fmt.Errorf("get Pod %s/%s: %w", request.Namespace, request.Pod, err)
	}
	containers := podContainerNames(&pod)
	if request.Container != "" {
		found := false
		for _, name := range containers {
			if name == request.Container {
				found = true
				break
			}
		}
		if !found {
			return PodLogResult{}, fmt.Errorf("container %q not found in Pod %s/%s", request.Container, request.Namespace, request.Pod)
		}
		containers = []string{request.Container}
	}
	result := PodLogResult{
		Namespace: request.Namespace, Pod: request.Pod, Previous: request.Previous,
		SinceMinutes: window, Streams: []LogStream{}, Warnings: []string{},
	}
	if len(containers) > maxLogContainers {
		containers = containers[:maxLogContainers]
		result.Warnings = append(result.Warnings, fmt.Sprintf("container list limited to %d", maxLogContainers))
	}
	since := r.now().UTC().Add(-time.Duration(window) * time.Minute)
	for _, containerName := range containers {
		contents, readErr := r.logs.Read(ctx, request.Namespace, request.Pod, containerName, since, request.Previous, request.TailLines, perLogBytes)
		if readErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("container/%s: %s", containerName, sanitize(readErr.Error(), 256)))
			continue
		}
		text := strings.ToValidUTF8(string(contents), "�")
		result.Streams = append(result.Streams, LogStream{
			Container: containerName, Text: sanitize(text, 128*1024), Truncated: int64(len(contents)) >= perLogBytes,
		})
	}
	return result, nil
}

func (r *KubernetesReader) ListHelmReleases(ctx context.Context, request HelmReleaseRequest) (HelmReleaseList, error) {
	request.Namespace = strings.TrimSpace(request.Namespace)
	if err := validateNamespace(request.Namespace, true); err != nil {
		return HelmReleaseList{}, err
	}
	limit, err := normalizeLimit(request.Limit, defaultListLimit, maxListLimit)
	if err != nil {
		return HelmReleaseList{}, err
	}
	result := HelmReleaseList{Namespace: request.Namespace, Releases: []HelmReleaseSummary{}, Warnings: []string{}}
	byRelease := map[string]HelmReleaseSummary{}
	for _, storage := range []string{"secrets", "configmaps"} {
		resource := r.metadata.Resource(schema.GroupVersionResource{Group: "", Version: "v1", Resource: storage})
		var list *metav1.PartialObjectMetadataList
		if request.Namespace == "" {
			list, err = resource.List(ctx, metav1.ListOptions{LabelSelector: "owner=helm"})
		} else {
			list, err = resource.Namespace(request.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "owner=helm"})
		}
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("list Helm %s metadata: %s", storage, sanitize(err.Error(), 256)))
			continue
		}
		for index := range list.Items {
			item := &list.Items[index]
			name := strings.TrimSpace(item.Labels["name"])
			if name == "" {
				continue
			}
			revision, _ := strconv.Atoi(item.Labels["version"])
			key := item.Namespace + "\x00" + name
			candidate := HelmReleaseSummary{
				Namespace: item.Namespace, Name: name, Revision: revision,
				Status: item.Labels["status"], Storage: strings.TrimSuffix(storage, "s"),
			}
			if existing, ok := byRelease[key]; !ok || candidate.Revision > existing.Revision {
				byRelease[key] = candidate
			}
		}
	}
	for _, release := range byRelease {
		result.Releases = append(result.Releases, release)
	}
	sort.Slice(result.Releases, func(i, j int) bool {
		return inventoryKey(result.Releases[i].Namespace, "HelmRelease", result.Releases[i].Name) < inventoryKey(result.Releases[j].Namespace, "HelmRelease", result.Releases[j].Name)
	})
	result.Total = len(result.Releases)
	if len(result.Releases) > limit {
		result.Releases = result.Releases[:limit]
		result.Truncated = true
	}
	return result, nil
}

func summarizePod(pod *corev1.Pod) PodSummary {
	result := PodSummary{
		Namespace: pod.Namespace, Name: pod.Name, Node: pod.Spec.NodeName,
		Phase: string(pod.Status.Phase), Ready: podReady(pod),
		Containers: podContainerNames(pod), HelmRelease: helmRelease(pod),
	}
	for _, owner := range pod.OwnerReferences {
		if owner.Controller != nil && *owner.Controller {
			result.Owner = owner.Kind + "/" + owner.Name
			break
		}
	}
	for _, status := range append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...) {
		result.Restarts += status.RestartCount
		if status.State.Waiting != nil && result.Reason == "" {
			result.Reason = status.State.Waiting.Reason
		}
		if status.State.Terminated != nil && status.State.Terminated.ExitCode != 0 && result.Reason == "" {
			result.Reason = status.State.Terminated.Reason
		}
	}
	if result.Reason == "" {
		result.Reason = pod.Status.Reason
	}
	return result
}

func summarizeService(service *corev1.Service) ServiceSummary {
	result := ServiceSummary{
		Namespace: service.Namespace, Name: service.Name, Type: string(service.Spec.Type),
		ClusterIP: service.Spec.ClusterIP, Selector: service.Spec.Selector,
		Ports: []string{}, HelmRelease: helmRelease(service),
	}
	for _, port := range service.Spec.Ports {
		value := fmt.Sprintf("%s:%d/%s", port.Name, port.Port, port.Protocol)
		if port.NodePort > 0 {
			value += fmt.Sprintf(" nodePort=%d", port.NodePort)
		}
		result.Ports = append(result.Ports, value)
	}
	return result
}

func podReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func podContainerNames(pod *corev1.Pod) []string {
	result := make([]string, 0, len(pod.Spec.InitContainers)+len(pod.Spec.Containers))
	for _, container := range pod.Spec.InitContainers {
		result = append(result, container.Name)
	}
	for _, container := range pod.Spec.Containers {
		result = append(result, container.Name)
	}
	return result
}

func podSpecImages(spec *corev1.PodSpec) []string {
	result := []string{}
	for _, container := range append(append([]corev1.Container{}, spec.InitContainers...), spec.Containers...) {
		result = append(result, container.Image)
	}
	return mergeStrings(nil, result)
}

func mergeStrings(left, right []string) []string {
	seen := map[string]struct{}{}
	result := []string{}
	for _, value := range append(append([]string{}, left...), right...) {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func helmRelease(object metav1.Object) string {
	if value := strings.TrimSpace(object.GetAnnotations()["meta.helm.sh/release-name"]); value != "" {
		return value
	}
	if strings.EqualFold(object.GetLabels()["app.kubernetes.io/managed-by"], "Helm") {
		return strings.TrimSpace(object.GetLabels()["app.kubernetes.io/instance"])
	}
	return ""
}

func selectedLabels(values map[string]string) map[string]string {
	allowed := []string{
		"app.kubernetes.io/name", "app.kubernetes.io/instance", "app.kubernetes.io/component",
		"openfuyao.com/engine", "openfuyao.com/pdRole", "openfuyao.com/kvmanager",
		"infernex.io/owner", "infernex.io/component",
	}
	result := map[string]string{}
	for _, key := range allowed {
		if value := strings.TrimSpace(values[key]); value != "" {
			result[key] = sanitize(value, 128)
		}
	}
	return result
}

func valueOrOne(value *int32) int32 {
	if value == nil {
		return 1
	}
	return *value
}

func eventTimestamp(event *corev1.Event) time.Time {
	if !event.EventTime.IsZero() {
		return event.EventTime.Time.UTC()
	}
	if !event.LastTimestamp.IsZero() {
		return event.LastTimestamp.Time.UTC()
	}
	if !event.FirstTimestamp.IsZero() {
		return event.FirstTimestamp.Time.UTC()
	}
	return event.CreationTimestamp.Time.UTC()
}

func validateNamespace(namespace string, optional bool) error {
	if namespace == "" && optional {
		return nil
	}
	if problems := validation.IsDNS1123Label(namespace); len(problems) > 0 {
		return fmt.Errorf("invalid namespace %q: %s", namespace, strings.Join(problems, "; "))
	}
	return nil
}

func normalizeLimit(value, defaultValue, maxValue int) (int, error) {
	if value <= 0 {
		return defaultValue, nil
	}
	if value > maxValue {
		return 0, fmt.Errorf("limit must not exceed %d", maxValue)
	}
	return value, nil
}

func normalizeWindow(value, defaultValue int) (int, error) {
	if value <= 0 {
		return defaultValue, nil
	}
	if value > maxWindowMinutes {
		return 0, fmt.Errorf("sinceMinutes must not exceed %d", maxWindowMinutes)
	}
	return value, nil
}

func sanitize(value string, maxRunes int) string {
	value = strings.ToValidUTF8(value, "�")
	value = bearerPattern.ReplaceAllString(value, "$1<redacted>")
	value = secretPattern.ReplaceAllString(value, "$1$2<redacted>")
	value = credentialURLPattern.ReplaceAllString(value, "$1<redacted>@")
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxRunes]) + "…"
}

func inventoryKey(namespace, kind, name string) string {
	return namespace + "\x00" + kind + "\x00" + name
}

func truncateWorkloads(values []WorkloadSummary, remaining int) ([]WorkloadSummary, int) {
	if remaining <= 0 {
		return []WorkloadSummary{}, 0
	}
	if len(values) <= remaining {
		return values, remaining - len(values)
	}
	return values[:remaining], 0
}

func truncatePods(values []PodSummary, remaining int) ([]PodSummary, int) {
	if remaining <= 0 {
		return []PodSummary{}, 0
	}
	if len(values) <= remaining {
		return values, remaining - len(values)
	}
	return values[:remaining], 0
}

func truncateServices(values []ServiceSummary, remaining int) ([]ServiceSummary, int) {
	if remaining <= 0 {
		return []ServiceSummary{}, 0
	}
	if len(values) <= remaining {
		return values, remaining - len(values)
	}
	return values[:remaining], 0
}
