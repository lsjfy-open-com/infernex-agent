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

// Package k8s implements the K8s Layer checks (K-01 through K-04), verifying
// CoreDNS health, node Ready status, node taints, and node allocatable resources
// via the Kubernetes API.
package k8s

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"openfuyao/infernex-checker/pkg/executor"
	"openfuyao/infernex-checker/pkg/log"
	"openfuyao/infernex-checker/pkg/types"
)

const (
	dnsProbImage = "busybox:1.36" // container image used for DNS resolution probe
)

var (
	dnsCheckInterval = 2 * time.Second  // polling interval when waiting for DNS check pod
	dnsCheckTimeout  = 60 * time.Second // total timeout for DNS check pod to complete
)

// CheckK8s runs full K8s Layer checks
func CheckK8s(k8sClient *executor.K8sClient, nodeNames []string) *types.K8sResult {
	result := &types.K8sResult{Status: types.StatusPassed}
	ctx := context.Background()

	// K-01 CoreDNS check
	k01 := checkK01(ctx, k8sClient)
	result.Checks = append(result.Checks, k01)
	if k01.Status == types.StatusFailed {
		result.Status = types.StatusFailed
		return result
	}

	// K-02 node Ready status
	k02Results, readyNodes := checkK02(ctx, k8sClient, nodeNames)
	result.Checks = append(result.Checks, k02Results...)
	if len(readyNodes) == 0 {
		result.Status = types.StatusFailed
		return result
	}

	// K-03 node taints
	k03Results := checkK03(ctx, k8sClient, readyNodes)
	result.Checks = append(result.Checks, k03Results...)

	// K-04 node available resources
	k04Results := checkK04(ctx, k8sClient, readyNodes)
	result.Checks = append(result.Checks, k04Results...)

	return result
}

// checkK01 checks cluster CoreDNS
func checkK01(ctx context.Context, k8sClient *executor.K8sClient) types.CheckResult {
	pods, err := k8sClient.GetPodsInAllNamespaces(ctx, "k8s-app=kube-dns")
	if err != nil {
		log.Error(fmt.Sprintf("K-01 failed to get CoreDNS pods: %v", err))
		return types.CheckResult{
			ID:         "K-01",
			Status:     types.StatusFailed,
			Message:    fmt.Sprintf("Failed to get CoreDNS pods: %v", err),
			Suggestion: "Check kubeconfig configuration and cluster connectivity",
		}
	}

	if len(pods) == 0 {
		return types.CheckResult{
			ID:         "K-01",
			Status:     types.StatusFailed,
			Message:    "No CoreDNS pods found",
			Suggestion: "Check CoreDNS pod status and logs, restart CoreDNS or investigate network policies",
		}
	}

	var notRunning []string
	for _, pod := range pods {
		if pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil {
			notRunning = append(notRunning, fmt.Sprintf("%s(%s)", pod.Name, pod.Status.Phase))
		}
	}

	if len(notRunning) == len(pods) {
		return types.CheckResult{
			ID:     "K-01",
			Status: types.StatusFailed,
			Message: fmt.Sprintf("All CoreDNS pods are not in Running state: %s",
				strings.Join(notRunning, ", ")),
			Suggestion: "Check CoreDNS pod status and logs, restart CoreDNS or investigate network policies",
		}
	}

	if len(notRunning) > 0 {
		return types.CheckResult{
			ID:     "K-01",
			Status: types.StatusWarning,
			Message: fmt.Sprintf("Some CoreDNS pods are not in Running state: %s",
				strings.Join(notRunning, ", ")),
			Suggestion: "Check CoreDNS pod status and logs, restart CoreDNS or investigate network policies",
		}
	}

	// create a temporary pod to verify DNS resolution
	dnsResult := checkDNSResolution(ctx, k8sClient)
	if dnsResult.Status == types.StatusFailed {
		return dnsResult
	}

	return types.CheckResult{
		ID:      "K-01",
		Status:  types.StatusPassed,
		Message: "CoreDNS Running, service domain names are resolvable",
	}
}

// checkDNSResolution creates a temporary pod to verify DNS resolution
func checkDNSResolution(ctx context.Context, k8sClient *executor.K8sClient) types.CheckResult {
	podName := fmt.Sprintf("infernex-dns-check-%d", time.Now().Unix())
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:    "dns-check",
					Image:   dnsProbImage,
					Command: []string{"nslookup", "kubernetes.default.svc.cluster.local"},
				},
			},
		},
	}

	_, err := k8sClient.CreatePod(ctx, pod)
	if err != nil {
		log.Error(fmt.Sprintf("K-01 failed to create DNS check pod: %v", err))
		return types.CheckResult{
			ID:         "K-01",
			Status:     types.StatusFailed,
			Message:    fmt.Sprintf("Failed to create DNS check pod: %v", err),
			Suggestion: "Check cluster permission configuration",
		}
	}

	defer func() {
		_ = k8sClient.DeletePod(ctx, "default", podName)
	}()

	return waitForDNSPod(ctx, k8sClient, podName)
}

func waitForDNSPod(ctx context.Context, k8sClient *executor.K8sClient, podName string) types.CheckResult {
	ticker := time.NewTicker(dnsCheckInterval)
	defer ticker.Stop()
	timeoutCtx, cancel := context.WithTimeout(ctx, dnsCheckTimeout)
	defer cancel()

	for {
		select {
		case <-timeoutCtx.Done():
			return types.CheckResult{
				ID:         "K-01",
				Status:     types.StatusFailed,
				Message:    "DNS check pod timed out",
				Suggestion: "Check cluster scheduling status",
			}
		case <-ticker.C:
			p, err := k8sClient.GetPod(timeoutCtx, "default", podName)
			if err != nil {
				break
			}
			if p.Status.Phase == corev1.PodSucceeded {
				return types.CheckResult{
					ID:      "K-01",
					Status:  types.StatusPassed,
					Message: "DNS resolution is working",
				}
			}
			if p.Status.Phase == corev1.PodFailed {
				return types.CheckResult{
					ID:         "K-01",
					Status:     types.StatusFailed,
					Message:    "DNS resolution failed (nslookup kubernetes.default.svc.cluster.local returned non-zero)",
					Suggestion: "Check CoreDNS pod status and logs, restart CoreDNS or investigate network policies",
				}
			}
		}
	}
}

// checkK02 checks whether nodes are Ready; returns check results and list of Ready node names
func checkK02(ctx context.Context, k8sClient *executor.K8sClient, nodeNames []string) ([]types.CheckResult, []string) {
	var results []types.CheckResult
	var readyNodes []string

	for _, name := range nodeNames {
		node, err := k8sClient.GetNode(ctx, name)
		if err != nil {
			log.Error(fmt.Sprintf("K-02 failed to get node %s: %v", name, err))
			results = append(results, types.CheckResult{
				ID:         "K-02",
				Status:     types.StatusFailed,
				Message:    fmt.Sprintf("Failed to get node %s: %v", name, err),
				Suggestion: "Please check the node name and cluster connectivity",
			})
			continue
		}

		ready := false
		for _, cond := range node.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
				ready = true
				break
			}
		}

		if ready {
			readyNodes = append(readyNodes, name)
			results = append(results, types.CheckResult{
				ID:      "K-02",
				Status:  types.StatusPassed,
				Message: fmt.Sprintf("%s Ready", name),
			})
		} else {
			results = append(results, types.CheckResult{
				ID:         "K-02",
				Status:     types.StatusFailed,
				Message:    fmt.Sprintf("%s is not in Ready state", name),
				Suggestion: "Investigate the cause of node anomaly (network, resources, component failure, etc.)",
			})
		}
	}

	return results, readyNodes
}

// checkK03 checks node taints
func checkK03(ctx context.Context, k8sClient *executor.K8sClient, nodeNames []string) []types.CheckResult {
	var results []types.CheckResult

	for _, name := range nodeNames {
		node, err := k8sClient.GetNode(ctx, name)
		if err != nil {
			log.Error(fmt.Sprintf("K-03 failed to get node %s: %v", name, err))
			continue
		}

		if len(node.Spec.Taints) == 0 {
			results = append(results, types.CheckResult{
				ID:      "K-03",
				Status:  types.StatusPassed,
				Message: fmt.Sprintf("%s has no taints", name),
			})
		} else {
			var taintStrs []string
			for _, t := range node.Spec.Taints {
				taintStrs = append(taintStrs, fmt.Sprintf("%s=%s:%s", t.Key, t.Value, t.Effect))
			}
			results = append(results, types.CheckResult{
				ID:      "K-03",
				Status:  types.StatusInfo,
				Message: fmt.Sprintf("%s has taints: %s", name, strings.Join(taintStrs, ", ")),
				Suggestion: "For user to determine whether this affects InferNex pod scheduling; " +
					"if taints block scheduling, add corresponding tolerations for InferNex pods in values.yaml, " +
					"or remove unnecessary taints",
			})
		}
	}

	return results
}

// checkK04 checks node available resources
func checkK04(ctx context.Context, k8sClient *executor.K8sClient, nodeNames []string) []types.CheckResult {
	var results []types.CheckResult

	for _, name := range nodeNames {
		node, err := k8sClient.GetNode(ctx, name)
		if err != nil {
			log.Error(fmt.Sprintf("K-04 failed to get node %s: %v", name, err))
			continue
		}

		cpuAllocatable := node.Status.Allocatable.Cpu()
		memAllocatable := node.Status.Allocatable.Memory()

		results = append(results, types.CheckResult{
			ID:     "K-04",
			Status: types.StatusInfo,
			Message: fmt.Sprintf("%s  Allocatable resources: CPU %s, Memory %s",
				name,
				cpuAllocatable.String(),
				memAllocatable.String(),
			),
			Detail: map[string]interface{}{
				"node":               name,
				"cpu_allocatable":    cpuAllocatable.String(),
				"memory_allocatable": memAllocatable.String(),
			},
			Suggestion: "Please confirm this meets InferNex deployment requirements; " +
				"if insufficient, consider releasing occupied resources or adjusting workloads",
		})
	}

	return results
}
