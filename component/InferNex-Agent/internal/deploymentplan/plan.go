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

package deploymentplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	pageSize       = int64(500)
	maxNodes       = 10_000
	maxPods        = 100_000
	maxPolicyItems = 1_000
	maxReplicas    = 1_000
)

var baseWarnings = []string{
	"This read-only plan is an estimate, not a resource reservation or deployment approval.",
	"Traffic and performance have not been verified; the plan makes no SLO or throughput commitment.",
	"Unbound scheduling-queue demand is not reserved or subtracted; a nominated pod makes its nominated node unknown.",
}

// Generate creates a deterministic, read-only placement estimate from native
// Kubernetes objects. Any failed or ambiguous cluster read fails closed.
func Generate(ctx context.Context, reader client.Reader, request Request) (Plan, error) {
	profileRequests, profileHash, unsupported, err := validateProfile(request)
	if err != nil {
		return Plan{}, err
	}
	plan := newPlan(request, profileHash, profileRequests)
	if len(unsupported) != 0 {
		plan.Status = StatusBlocked
		plan.Reasons = unsupported
		plan.PlanHash = planHash(request, profileHash, "")
		return plan, nil
	}

	var namespace corev1.Namespace
	if err := reader.Get(ctx, client.ObjectKey{Name: request.Namespace}, &namespace); err != nil {
		return Plan{}, fmt.Errorf("read namespace %q for deployment plan: %w", request.Namespace, err)
	}
	nodes, err := listNodes(ctx, reader)
	if err != nil {
		return Plan{}, fmt.Errorf("read nodes for deployment plan: %w", err)
	}
	pods, err := listPods(ctx, reader)
	if err != nil {
		return Plan{}, fmt.Errorf("read pods for deployment plan: %w", err)
	}
	quotas, err := listResourceQuotas(ctx, reader, request.Namespace)
	if err != nil {
		return Plan{}, fmt.Errorf("read ResourceQuotas in namespace %q: %w", request.Namespace, err)
	}
	limits, err := listLimitRanges(ctx, reader, request.Namespace)
	if err != nil {
		return Plan{}, fmt.Errorf("read LimitRanges in namespace %q: %w", request.Namespace, err)
	}

	plan.SnapshotHash = snapshotHash(namespace, profileRequests, nodes, pods, quotas, limits)
	plan.PlanHash = planHash(request, profileHash, plan.SnapshotHash)
	if namespace.DeletionTimestamp != nil || namespace.Status.Phase == corev1.NamespaceTerminating {
		plan.Status = StatusBlocked
		plan.Reasons = []string{"target namespace is terminating"}
		return plan, nil
	}
	if len(limits.Items) != 0 {
		plan.Status = StatusBlocked
		plan.Reasons = []string{"namespace has LimitRange policies whose admission-time defaults and constraints are not modeled"}
		return plan, nil
	}

	quotaSlots, quotaBlocked, quotaReasons := quotaCapacity(request.Replicas, profileRequests, quotas.Items)
	if quotaBlocked {
		plan.Status = StatusBlocked
		plan.Reasons = quotaReasons
		return plan, nil
	}

	occupied, uncertainNodes := occupiedByNode(pods.Items)
	sort.Slice(nodes.Items, func(i, j int) bool {
		if nodes.Items[i].Name == nodes.Items[j].Name {
			return string(nodes.Items[i].UID) < string(nodes.Items[j].UID)
		}
		return nodes.Items[i].Name < nodes.Items[j].Name
	})

	target := request.Replicas
	if quotaSlots < target {
		target = quotaSlots
	}
	remaining := target
	unknownEligible := 0
	for i := range nodes.Items {
		node := &nodes.Items[i]
		if !nodeEligible(node, request.Profile) {
			continue
		}
		if reason := uncertainNodes[node.Name]; reason != "" {
			unknownEligible++
			continue
		}
		if missingCoreAllocatable(node.Status.Allocatable) {
			unknownEligible++
			continue
		}
		available := copyResources(node.Status.Allocatable)
		subtractResources(available, occupied[node.Name])
		placed := 0
		for placed < remaining && fits(available, profileRequests) {
			subtractResources(available, profileRequests)
			placed++
		}
		if placed == 0 {
			continue
		}
		plan.Placements = append(plan.Placements, Placement{
			NodeName:           node.Name,
			NodeUID:            string(node.UID),
			Replicas:           placed,
			RemainingResources: resourceStrings(available, profileRequests),
		})
		plan.PlaceableReplicas += placed
		remaining -= placed
		if remaining == 0 {
			break
		}
	}

	switch {
	case plan.PlaceableReplicas == request.Replicas:
		plan.Status = StatusSchedulable
	case quotaSlots < request.Replicas:
		plan.Status = StatusInsufficient
		plan.Reasons = quotaReasons
	case unknownEligible != 0 && plan.PlaceableReplicas < request.Replicas:
		plan.Status = StatusBlocked
		plan.Reasons = []string{fmt.Sprintf("%d otherwise eligible node(s) have unknown resource accounting", unknownEligible)}
	default:
		plan.Status = StatusInsufficient
		plan.Reasons = []string{"eligible nodes do not have enough unallocated resources for all requested replicas"}
	}
	return plan, nil
}

func newPlan(request Request, profileHash string, profileRequests corev1.ResourceList) Plan {
	total := corev1.ResourceList{}
	for i := 0; i < request.Replicas; i++ {
		addResources(total, profileRequests)
	}
	return Plan{
		Version:           PlanVersion,
		ProfileHash:       profileHash,
		Namespace:         request.Namespace,
		RequestedReplicas: request.Replicas,
		ResourceEstimate: ResourceEstimate{
			PerReplica:     allResourceStrings(profileRequests),
			TotalRequested: allResourceStrings(total),
		},
		Placements: []Placement{},
		Warnings:   append([]string(nil), baseWarnings...),
	}
}

func planHash(request Request, profileHash, snapshotHash string) string {
	hash, _ := hashJSON(struct {
		Namespace    string `json:"namespace"`
		Replicas     int    `json:"replicas"`
		ProfileHash  string `json:"profileHash"`
		SnapshotHash string `json:"snapshotHash"`
	}{request.Namespace, request.Replicas, profileHash, snapshotHash})
	return hash
}

func validateProfile(request Request) (corev1.ResourceList, string, []string, error) {
	if problems := validation.IsDNS1123Label(request.Namespace); len(problems) != 0 {
		return nil, "", nil, fmt.Errorf("namespace must be a valid DNS label: %s", strings.Join(problems, ", "))
	}
	if request.Replicas <= 0 || request.Replicas > maxReplicas {
		return nil, "", nil, fmt.Errorf("replicas must be between 1 and %d", maxReplicas)
	}
	p := request.Profile
	if p.Version != ProfileVersion {
		return nil, "", nil, fmt.Errorf("profile version must be %q", ProfileVersion)
	}
	if !isDigestImage(p.Image) {
		return nil, "", nil, fmt.Errorf("profile image must be pinned by a sha256 digest")
	}
	if strings.ContainsAny(p.Image, " \t\r\n") || strings.Contains(p.Image, "://") || strings.HasPrefix(p.Image, "/") {
		return nil, "", nil, fmt.Errorf("profile image is not a valid container image reference")
	}
	cpu, err := resource.ParseQuantity(p.CPU)
	minimumCPU := resource.MustParse("1m")
	if err != nil || cpu.Cmp(minimumCPU) < 0 || cpu.Cmp(*resource.NewMilliQuantity(cpu.MilliValue(), resource.DecimalSI)) != 0 {
		return nil, "", nil, fmt.Errorf("profile cpu must be a Kubernetes Quantity of at least 1m with whole millicore precision")
	}
	memory, err := resource.ParseQuantity(p.Memory)
	minimumMemory := resource.MustParse("1")
	if err != nil || memory.Cmp(minimumMemory) < 0 {
		return nil, "", nil, fmt.Errorf("profile memory must be a Kubernetes Quantity of at least one byte")
	}
	requests := corev1.ResourceList{
		corev1.ResourceCPU: cpu, corev1.ResourceMemory: memory,
		corev1.ResourcePods: *resource.NewQuantity(1, resource.DecimalSI),
	}
	for name, count := range p.ExtendedResources {
		if count <= 0 {
			return nil, "", nil, fmt.Errorf("extended resource %q must have a positive integer count", name)
		}
		if !strings.Contains(name, "/") || strings.HasPrefix(name, "requests.") || len(validation.IsQualifiedName(name)) != 0 {
			return nil, "", nil, fmt.Errorf("extended resource %q must be a qualified resource name", name)
		}
		resourceName := corev1.ResourceName(name)
		if _, exists := requests[resourceName]; exists {
			return nil, "", nil, fmt.Errorf("duplicate resource %q", name)
		}
		requests[resourceName] = *resource.NewQuantity(count, resource.DecimalSI)
	}
	for key, value := range p.NodeSelector {
		if problems := validation.IsQualifiedName(key); len(problems) != 0 {
			return nil, "", nil, fmt.Errorf("nodeSelector key %q is invalid: %s", key, strings.Join(problems, ", "))
		}
		if problems := validation.IsValidLabelValue(value); len(problems) != 0 {
			return nil, "", nil, fmt.Errorf("nodeSelector value for %q is invalid: %s", key, strings.Join(problems, ", "))
		}
	}
	for _, toleration := range p.Tolerations {
		operator := toleration.Operator
		if operator == "" {
			operator = corev1.TolerationOpEqual
		}
		if operator != corev1.TolerationOpEqual && operator != corev1.TolerationOpExists {
			return nil, "", nil, fmt.Errorf("toleration for key %q has unsupported operator %q", toleration.Key, toleration.Operator)
		}
		if operator == corev1.TolerationOpEqual && toleration.Key == "" {
			return nil, "", nil, fmt.Errorf("Equal toleration requires a key")
		}
		if toleration.Key != "" {
			if problems := validation.IsQualifiedName(toleration.Key); len(problems) != 0 {
				return nil, "", nil, fmt.Errorf("toleration key %q is invalid: %s", toleration.Key, strings.Join(problems, ", "))
			}
		}
		if problems := validation.IsValidLabelValue(toleration.Value); len(problems) != 0 {
			return nil, "", nil, fmt.Errorf("toleration value for key %q is invalid: %s", toleration.Key, strings.Join(problems, ", "))
		}
		if operator == corev1.TolerationOpExists && toleration.Value != "" {
			return nil, "", nil, fmt.Errorf("Exists toleration for key %q must not set a value", toleration.Key)
		}
		switch toleration.Effect {
		case "", corev1.TaintEffectNoSchedule, corev1.TaintEffectPreferNoSchedule, corev1.TaintEffectNoExecute:
		default:
			return nil, "", nil, fmt.Errorf("toleration for key %q has invalid effect %q", toleration.Key, toleration.Effect)
		}
		if toleration.TolerationSeconds != nil && (toleration.Effect != corev1.TaintEffectNoExecute || *toleration.TolerationSeconds < 0) {
			return nil, "", nil, fmt.Errorf("tolerationSeconds requires NoExecute effect and a non-negative value")
		}
	}

	canonical := struct {
		Profile Profile `json:"profile"`
	}{Profile: p}
	profileHash, err := hashJSON(canonical)
	if err != nil {
		return nil, "", nil, fmt.Errorf("hash deployment profile: %w", err)
	}
	var unsupported []string
	if len(p.PersistentVolumeClaims) != 0 {
		unsupported = append(unsupported, "persistent volume claims are not supported by this planner version")
	}
	if p.Affinity != nil {
		unsupported = append(unsupported, "pod affinity and anti-affinity are not supported by this planner version")
	}
	if len(p.ResourceClaims) != 0 {
		unsupported = append(unsupported, "dynamic resource allocation claims are not supported by this planner version")
	}
	if p.PodResources != nil {
		unsupported = append(unsupported, "pod-level resources are not supported by this planner version")
	}
	for _, toleration := range p.Tolerations {
		if toleration.TolerationSeconds != nil {
			unsupported = append(unsupported, "finite NoExecute tolerations need a time horizon that this planner version does not model")
			break
		}
	}
	return requests, profileHash, unsupported, nil
}

func isDigestImage(image string) bool {
	parts := strings.Split(image, "@sha256:")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || len(parts[1]) != 64 {
		return false
	}
	_, err := hex.DecodeString(parts[1])
	return err == nil && parts[1] == strings.ToLower(parts[1])
}

type nodeSnapshot struct {
	Name            string            `json:"name"`
	UID             types.UID         `json:"uid"`
	ResourceVersion string            `json:"resourceVersion"`
	Ready           bool              `json:"ready"`
	Unschedulable   bool              `json:"unschedulable"`
	Labels          map[string]string `json:"labels,omitempty"`
	Taints          []taintSnapshot   `json:"taints,omitempty"`
	Allocatable     map[string]string `json:"allocatable"`
}

type taintSnapshot struct {
	Key    string             `json:"key"`
	Value  string             `json:"value,omitempty"`
	Effect corev1.TaintEffect `json:"effect"`
}

type quotaSnapshot struct {
	Name            string                      `json:"name"`
	UID             types.UID                   `json:"uid"`
	ResourceVersion string                      `json:"resourceVersion"`
	SpecHard        map[string]string           `json:"specHard"`
	StatusHard      map[string]string           `json:"statusHard"`
	Used            map[string]string           `json:"used"`
	Scopes          []corev1.ResourceQuotaScope `json:"scopes,omitempty"`
	ScopeSelector   *corev1.ScopeSelector       `json:"scopeSelector,omitempty"`
}

type limitSnapshot struct {
	Name            string                `json:"name"`
	UID             types.UID             `json:"uid"`
	ResourceVersion string                `json:"resourceVersion"`
	Spec            corev1.LimitRangeSpec `json:"spec"`
}

type podSnapshot struct {
	Namespace       string            `json:"namespace"`
	Name            string            `json:"name"`
	UID             types.UID         `json:"uid"`
	ResourceVersion string            `json:"resourceVersion"`
	NodeName        string            `json:"nodeName"`
	Phase           corev1.PodPhase   `json:"phase"`
	Requests        map[string]string `json:"requests,omitempty"`
	Unsupported     bool              `json:"unsupported,omitempty"`
}

func snapshotHash(namespace corev1.Namespace, profileRequests corev1.ResourceList, nodes corev1.NodeList, pods corev1.PodList, quotas corev1.ResourceQuotaList, limits corev1.LimitRangeList) string {
	nodeRecords := make([]nodeSnapshot, 0, len(nodes.Items))
	for i := range nodes.Items {
		n := &nodes.Items[i]
		taints := make([]taintSnapshot, 0, len(n.Spec.Taints))
		for _, taint := range n.Spec.Taints {
			taints = append(taints, taintSnapshot{Key: taint.Key, Value: taint.Value, Effect: taint.Effect})
		}
		nodeRecords = append(nodeRecords, nodeSnapshot{
			Name: n.Name, UID: n.UID, ResourceVersion: n.ResourceVersion,
			Ready: nodeReady(n), Unschedulable: n.Spec.Unschedulable,
			Labels: n.Labels, Taints: taints,
			Allocatable: allResourceStrings(n.Status.Allocatable),
		})
	}
	sort.Slice(nodeRecords, func(i, j int) bool { return nodeRecords[i].Name < nodeRecords[j].Name })
	podRecords := make([]podSnapshot, 0, len(pods.Items))
	for i := range pods.Items {
		p := &pods.Items[i]
		if !activePod(p) {
			continue
		}
		r, unsupported := podRequests(p)
		podRecords = append(podRecords, podSnapshot{
			Namespace: p.Namespace, Name: p.Name, UID: p.UID, ResourceVersion: p.ResourceVersion,
			NodeName: p.Spec.NodeName, Phase: p.Status.Phase, Requests: allResourceStrings(r), Unsupported: unsupported != "",
		})
	}
	sort.Slice(podRecords, func(i, j int) bool {
		return podRecords[i].Namespace+"/"+podRecords[i].Name < podRecords[j].Namespace+"/"+podRecords[j].Name
	})
	quotaRecords := make([]quotaSnapshot, 0, len(quotas.Items))
	for i := range quotas.Items {
		quota := &quotas.Items[i]
		quotaRecords = append(quotaRecords, quotaSnapshot{
			Name: quota.Name, UID: quota.UID, ResourceVersion: quota.ResourceVersion,
			SpecHard: allResourceStrings(quota.Spec.Hard), StatusHard: allResourceStrings(quota.Status.Hard), Used: allResourceStrings(quota.Status.Used),
			Scopes: quota.Spec.Scopes, ScopeSelector: quota.Spec.ScopeSelector,
		})
	}
	sort.Slice(quotaRecords, func(i, j int) bool { return quotaRecords[i].Name < quotaRecords[j].Name })
	limitRecords := make([]limitSnapshot, 0, len(limits.Items))
	for i := range limits.Items {
		limit := &limits.Items[i]
		limitRecords = append(limitRecords, limitSnapshot{Name: limit.Name, UID: limit.UID, ResourceVersion: limit.ResourceVersion, Spec: limit.Spec})
	}
	sort.Slice(limitRecords, func(i, j int) bool { return limitRecords[i].Name < limitRecords[j].Name })
	record := struct {
		Namespace         string                `json:"namespace"`
		NamespaceUID      types.UID             `json:"namespaceUid"`
		NamespaceRV       string                `json:"namespaceResourceVersion"`
		NamespacePhase    corev1.NamespacePhase `json:"namespacePhase"`
		NamespaceDeleting bool                  `json:"namespaceDeleting"`
		ProfileRequests   map[string]string     `json:"profileRequests"`
		Nodes             []nodeSnapshot        `json:"nodes"`
		Pods              []podSnapshot         `json:"pods"`
		NodeListRV        string                `json:"nodeListResourceVersion"`
		PodListRV         string                `json:"podListResourceVersion"`
		Quotas            []quotaSnapshot       `json:"quotas"`
		Limits            []limitSnapshot       `json:"limits"`
	}{namespace.Name, namespace.UID, namespace.ResourceVersion, namespace.Status.Phase, namespace.DeletionTimestamp != nil, allResourceStrings(profileRequests), nodeRecords, podRecords, nodes.ResourceVersion, pods.ResourceVersion, quotaRecords, limitRecords}
	hash, _ := hashJSON(record)
	return hash
}

func hashJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func listNodes(ctx context.Context, reader client.Reader) (corev1.NodeList, error) {
	var result corev1.NodeList
	continueToken := ""
	seen := map[string]struct{}{}
	for {
		var page corev1.NodeList
		if err := reader.List(ctx, &page, &client.ListOptions{Limit: pageSize, Continue: continueToken}); err != nil {
			return result, err
		}
		if result.ResourceVersion == "" {
			result.ResourceVersion = page.ResourceVersion
		}
		result.Items = append(result.Items, page.Items...)
		if len(result.Items) > maxNodes {
			return result, fmt.Errorf("node count exceeds safety limit %d", maxNodes)
		}
		if page.Continue == "" {
			return result, nil
		}
		if page.Continue == continueToken {
			return result, fmt.Errorf("node pagination token did not advance")
		}
		if _, duplicate := seen[page.Continue]; duplicate {
			return result, fmt.Errorf("node pagination token repeated")
		}
		seen[page.Continue] = struct{}{}
		if len(seen) > maxNodes {
			return result, fmt.Errorf("node pagination exceeds safety limit")
		}
		continueToken = page.Continue
	}
}

func listPods(ctx context.Context, reader client.Reader) (corev1.PodList, error) {
	var result corev1.PodList
	continueToken := ""
	seen := map[string]struct{}{}
	for {
		var page corev1.PodList
		if err := reader.List(ctx, &page, &client.ListOptions{Limit: pageSize, Continue: continueToken}); err != nil {
			return result, err
		}
		if result.ResourceVersion == "" {
			result.ResourceVersion = page.ResourceVersion
		}
		result.Items = append(result.Items, page.Items...)
		if len(result.Items) > maxPods {
			return result, fmt.Errorf("pod count exceeds safety limit %d", maxPods)
		}
		if page.Continue == "" {
			return result, nil
		}
		if page.Continue == continueToken {
			return result, fmt.Errorf("pod pagination token did not advance")
		}
		if _, duplicate := seen[page.Continue]; duplicate {
			return result, fmt.Errorf("pod pagination token repeated")
		}
		seen[page.Continue] = struct{}{}
		if len(seen) > maxPods {
			return result, fmt.Errorf("pod pagination exceeds safety limit")
		}
		continueToken = page.Continue
	}
}

func listResourceQuotas(ctx context.Context, reader client.Reader, namespace string) (corev1.ResourceQuotaList, error) {
	var result corev1.ResourceQuotaList
	continueToken := ""
	seen := map[string]struct{}{}
	for {
		var page corev1.ResourceQuotaList
		if err := reader.List(ctx, &page, &client.ListOptions{Namespace: namespace, Limit: pageSize, Continue: continueToken}); err != nil {
			return result, err
		}
		result.Items = append(result.Items, page.Items...)
		if len(result.Items) > maxPolicyItems {
			return result, fmt.Errorf("ResourceQuota count exceeds safety limit %d", maxPolicyItems)
		}
		if page.Continue == "" {
			return result, nil
		}
		if page.Continue == continueToken {
			return result, fmt.Errorf("ResourceQuota pagination token did not advance")
		}
		if _, duplicate := seen[page.Continue]; duplicate {
			return result, fmt.Errorf("ResourceQuota pagination token repeated")
		}
		seen[page.Continue] = struct{}{}
		if len(seen) > maxPolicyItems {
			return result, fmt.Errorf("ResourceQuota pagination exceeds safety limit")
		}
		continueToken = page.Continue
	}
}

func listLimitRanges(ctx context.Context, reader client.Reader, namespace string) (corev1.LimitRangeList, error) {
	var result corev1.LimitRangeList
	continueToken := ""
	seen := map[string]struct{}{}
	for {
		var page corev1.LimitRangeList
		if err := reader.List(ctx, &page, &client.ListOptions{Namespace: namespace, Limit: pageSize, Continue: continueToken}); err != nil {
			return result, err
		}
		result.Items = append(result.Items, page.Items...)
		if len(result.Items) > maxPolicyItems {
			return result, fmt.Errorf("LimitRange count exceeds safety limit %d", maxPolicyItems)
		}
		if page.Continue == "" {
			return result, nil
		}
		if page.Continue == continueToken {
			return result, fmt.Errorf("LimitRange pagination token did not advance")
		}
		if _, duplicate := seen[page.Continue]; duplicate {
			return result, fmt.Errorf("LimitRange pagination token repeated")
		}
		seen[page.Continue] = struct{}{}
		if len(seen) > maxPolicyItems {
			return result, fmt.Errorf("LimitRange pagination exceeds safety limit")
		}
		continueToken = page.Continue
	}
}

func occupiedByNode(pods []corev1.Pod) (map[string]corev1.ResourceList, map[string]string) {
	occupied := make(map[string]corev1.ResourceList)
	uncertain := make(map[string]string)
	for i := range pods {
		pod := &pods[i]
		if !activePod(pod) {
			continue
		}
		if pod.Spec.NodeName == "" {
			if pod.Status.NominatedNodeName != "" {
				uncertain[pod.Status.NominatedNodeName] = "nominated pending pod"
			}
			continue
		}
		requests, unsupported := podRequests(pod)
		if unsupported != "" {
			uncertain[pod.Spec.NodeName] = unsupported
			continue
		}
		if occupied[pod.Spec.NodeName] == nil {
			occupied[pod.Spec.NodeName] = corev1.ResourceList{}
		}
		addResources(occupied[pod.Spec.NodeName], requests)
	}
	return occupied, uncertain
}

func countsAgainstNode(pod *corev1.Pod) bool {
	return pod.Spec.NodeName != "" && activePod(pod)
}

func activePod(pod *corev1.Pod) bool {
	return pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed
}

func podRequests(pod *corev1.Pod) (corev1.ResourceList, string) {
	if pod.Spec.Resources != nil {
		return nil, "pod-level resources"
	}
	if len(pod.Spec.ResourceClaims) != 0 {
		return nil, "dynamic resource allocation"
	}
	for _, c := range pod.Spec.Containers {
		if len(c.Resources.Claims) != 0 {
			return nil, "dynamic resource allocation"
		}
	}
	for _, c := range pod.Spec.InitContainers {
		if len(c.Resources.Claims) != 0 {
			return nil, "dynamic resource allocation"
		}
	}
	if pod.Status.Resize != "" || resizeConditionActive(pod.Status.Conditions) || containerResourcesDiffer(pod.Spec.Containers, pod.Status.ContainerStatuses) || containerResourcesDiffer(pod.Spec.InitContainers, pod.Status.InitContainerStatuses) {
		return nil, "in-place pod resource resize"
	}

	regular := corev1.ResourceList{}
	for i := range pod.Spec.Containers {
		addResources(regular, pod.Spec.Containers[i].Resources.Requests)
	}
	restartable := corev1.ResourceList{}
	initPeak := corev1.ResourceList{}
	for i := range pod.Spec.InitContainers {
		container := &pod.Spec.InitContainers[i]
		if container.RestartPolicy != nil && *container.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			addResources(restartable, container.Resources.Requests)
			maxResources(initPeak, restartable)
			continue
		}
		step := copyResources(restartable)
		addResources(step, container.Resources.Requests)
		maxResources(initPeak, step)
	}
	addResources(regular, restartable)
	maxResources(regular, initPeak)
	addResources(regular, pod.Spec.Overhead)
	regular[corev1.ResourcePods] = *resource.NewQuantity(1, resource.DecimalSI)
	return regular, ""
}

func resizeConditionActive(conditions []corev1.PodCondition) bool {
	for _, condition := range conditions {
		if (condition.Type == corev1.PodResizePending || condition.Type == corev1.PodResizeInProgress) && condition.Status != corev1.ConditionFalse {
			return true
		}
	}
	return false
}

func missingCoreAllocatable(allocatable corev1.ResourceList) bool {
	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory, corev1.ResourcePods} {
		if _, ok := allocatable[name]; !ok {
			return true
		}
	}
	return false
}

func nodeEligible(node *corev1.Node, profile Profile) bool {
	if node.Spec.Unschedulable || !nodeReady(node) {
		return false
	}
	for key, value := range profile.NodeSelector {
		actual, exists := node.Labels[key]
		if !exists || actual != value {
			return false
		}
	}
	for _, taint := range node.Spec.Taints {
		if taint.Effect != corev1.TaintEffectNoSchedule && taint.Effect != corev1.TaintEffectNoExecute {
			continue
		}
		if !taintTolerated(taint, profile.Tolerations) {
			return false
		}
	}
	return true
}

func containerResourcesDiffer(containers []corev1.Container, statuses []corev1.ContainerStatus) bool {
	desired := make(map[string]corev1.ResourceList, len(containers))
	for i := range containers {
		desired[containers[i].Name] = containers[i].Resources.Requests
	}
	for i := range statuses {
		status := &statuses[i]
		if len(status.AllocatedResources) != 0 && !resourceListsEqual(desired[status.Name], status.AllocatedResources) {
			return true
		}
		if status.Resources != nil && !resourceListsEqual(desired[status.Name], status.Resources.Requests) {
			return true
		}
	}
	return false
}

func resourceListsEqual(left, right corev1.ResourceList) bool {
	for name, quantity := range left {
		other := right[name]
		if quantity.Cmp(other) != 0 {
			return false
		}
	}
	for name, quantity := range right {
		other := left[name]
		if quantity.Cmp(other) != 0 {
			return false
		}
	}
	return true
}

func nodeReady(node *corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func taintTolerated(taint corev1.Taint, tolerations []corev1.Toleration) bool {
	for _, t := range tolerations {
		if t.Effect != "" && t.Effect != taint.Effect {
			continue
		}
		operator := t.Operator
		if operator == "" {
			operator = corev1.TolerationOpEqual
		}
		switch operator {
		case corev1.TolerationOpExists:
			if t.Key == "" || t.Key == taint.Key {
				return true
			}
		case corev1.TolerationOpEqual:
			if t.Key == taint.Key && t.Value == taint.Value {
				return true
			}
		}
	}
	return false
}

func quotaCapacity(replicas int, requests corev1.ResourceList, quotas []corev1.ResourceQuota) (int, bool, []string) {
	capacity := replicas
	var reasons []string
	for i := range quotas {
		quota := &quotas[i]
		if len(quota.Spec.Scopes) != 0 || quota.Spec.ScopeSelector != nil {
			return 0, true, []string{fmt.Sprintf("ResourceQuota %q uses scopes that this planner cannot model", quota.Name)}
		}
		for hardName := range quota.Spec.Hard {
			perReplica, relevant := quotaRequest(hardName, requests)
			if !relevant {
				if quotaResourceAffectsPod(hardName) {
					return 0, true, []string{fmt.Sprintf("ResourceQuota %q constrains unsupported pod resource %q", quota.Name, hardName)}
				}
				continue
			}
			hard, hardKnown := quota.Status.Hard[hardName]
			if !hardKnown {
				return 0, true, []string{fmt.Sprintf("ResourceQuota %q has no observed hard limit for %q", quota.Name, hardName)}
			}
			if hard.Cmp(quota.Spec.Hard[hardName]) != 0 {
				return 0, true, []string{fmt.Sprintf("ResourceQuota %q observed hard limit for %q is not synchronized", quota.Name, hardName)}
			}
			used, known := quota.Status.Used[hardName]
			if !known {
				return 0, true, []string{fmt.Sprintf("ResourceQuota %q has no observed usage for %q", quota.Name, hardName)}
			}
			slots := 0
			trial := used.DeepCopy()
			for slots < replicas {
				trial.Add(perReplica)
				if trial.Cmp(hard) > 0 {
					break
				}
				slots++
			}
			if slots < capacity {
				capacity = slots
				reasons = []string{fmt.Sprintf("ResourceQuota %q allows only %d additional replica(s) for %q", quota.Name, slots, hardName)}
			}
		}
	}
	return capacity, false, reasons
}

func quotaResourceAffectsPod(name corev1.ResourceName) bool {
	text := string(name)
	if strings.HasPrefix(text, "limits.") || strings.HasPrefix(text, "requests.") {
		return true
	}
	if text == "ephemeral-storage" || text == "count/pods" {
		return true
	}
	return strings.Contains(text, "/") && !strings.HasPrefix(text, "count/")
}

func quotaRequest(name corev1.ResourceName, requests corev1.ResourceList) (resource.Quantity, bool) {
	if name == corev1.ResourcePods || name == corev1.ResourceName("count/pods") {
		return *resource.NewQuantity(1, resource.DecimalSI), true
	}
	if name == corev1.ResourceCPU || name == corev1.ResourceRequestsCPU {
		q, ok := requests[corev1.ResourceCPU]
		return q, ok
	}
	if name == corev1.ResourceMemory || name == corev1.ResourceRequestsMemory {
		q, ok := requests[corev1.ResourceMemory]
		return q, ok
	}
	text := string(name)
	if strings.HasPrefix(text, "requests.") {
		q, ok := requests[corev1.ResourceName(strings.TrimPrefix(text, "requests."))]
		return q, ok
	}
	q, ok := requests[name]
	return q, ok
}

func copyResources(source corev1.ResourceList) corev1.ResourceList {
	result := corev1.ResourceList{}
	for name, quantity := range source {
		result[name] = quantity.DeepCopy()
	}
	return result
}

func addResources(target, addition corev1.ResourceList) {
	for name, quantity := range addition {
		current := target[name]
		current.Add(quantity)
		target[name] = current
	}
}

func subtractResources(target, subtraction corev1.ResourceList) {
	for name, quantity := range subtraction {
		current := target[name]
		current.Sub(quantity)
		target[name] = current
	}
}

func maxResources(target, candidate corev1.ResourceList) {
	for name, quantity := range candidate {
		if current, ok := target[name]; !ok || quantity.Cmp(current) > 0 {
			target[name] = quantity.DeepCopy()
		}
	}
}

func fits(available, requests corev1.ResourceList) bool {
	for name, requested := range requests {
		quantity := available[name]
		if quantity.Cmp(requested) < 0 {
			return false
		}
	}
	return true
}

func resourceStrings(resources, filter corev1.ResourceList) map[string]string {
	result := make(map[string]string, len(filter))
	for name := range filter {
		quantity := resources[name]
		result[string(name)] = quantity.String()
	}
	return result
}

func allResourceStrings(resources corev1.ResourceList) map[string]string {
	result := make(map[string]string, len(resources))
	for name, quantity := range resources {
		result[string(name)] = quantity.String()
	}
	return result
}
