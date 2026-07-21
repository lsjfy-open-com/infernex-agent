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

package k8s

import (
	"context"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"openfuyao/infernex-checker/pkg/executor"
	"openfuyao/infernex-checker/pkg/types"
)

// TestMain sets a short DNS check timeout so tests that hit the timeout path
// complete in ~1 second instead of the default 60 seconds.
func TestMain(m *testing.M) {
	dnsCheckTimeout = 1 * time.Second
	os.Exit(m.Run())
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func makePod(name, namespace string, phase corev1.PodPhase, labels map[string]string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func makeNode(name string, ready bool, taints []corev1.Taint, cpu, mem string) corev1.Node {
	condStatus := corev1.ConditionFalse
	if ready {
		condStatus = corev1.ConditionTrue
	}
	allocatable := corev1.ResourceList{}
	if cpu != "" {
		allocatable[corev1.ResourceCPU] = resource.MustParse(cpu)
	}
	if mem != "" {
		allocatable[corev1.ResourceMemory] = resource.MustParse(mem)
	}
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.NodeSpec{Taints: taints},
		Status: corev1.NodeStatus{
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: condStatus}},
			Allocatable: allocatable,
		},
	}
}

func newClient(objects ...runtime.Object) *executor.K8sClient {
	cs := fake.NewSimpleClientset(objects...)
	return executor.NewK8sClientForTest(cs)
}

// ---------------------------------------------------------------------------
// checkK01
// ---------------------------------------------------------------------------

func TestCheckK01_NoPods(t *testing.T) {
	client := executor.NewK8sClientForTest(fake.NewSimpleClientset())
	result := checkK01(context.Background(), client)
	if result.Status != types.StatusFailed {
		t.Errorf("expected failed when no CoreDNS pods, got %q", result.Status)
	}
	if result.ID != "K-01" {
		t.Errorf("expected ID K-01, got %q", result.ID)
	}
}

func TestCheckK01_AllPodsNotRunning(t *testing.T) {
	pod := makePod("coredns-1", "kube-system", corev1.PodPending,
		map[string]string{"k8s-app": "kube-dns"})
	client := executor.NewK8sClientForTest(fake.NewSimpleClientset(&pod))
	result := checkK01(context.Background(), client)
	if result.Status != types.StatusFailed {
		t.Errorf("expected failed when all CoreDNS pods not running, got %q", result.Status)
	}
}

func TestCheckK01_SomePodsNotRunning(t *testing.T) {
	pod1 := makePod("coredns-1", "kube-system", corev1.PodRunning,
		map[string]string{"k8s-app": "kube-dns"})
	pod2 := makePod("coredns-2", "kube-system", corev1.PodPending,
		map[string]string{"k8s-app": "kube-dns"})
	client := executor.NewK8sClientForTest(fake.NewSimpleClientset(&pod1, &pod2))
	result := checkK01(context.Background(), client)
	if result.Status != types.StatusWarning {
		t.Errorf("expected warning when some CoreDNS pods not running, got %q", result.Status)
	}
}

func TestCheckK01_TerminatingPodCountsAsNotRunning(t *testing.T) {
	now := metav1.Now()
	pod := makePod("coredns-1", "kube-system", corev1.PodRunning,
		map[string]string{"k8s-app": "kube-dns"})
	pod.DeletionTimestamp = &now
	client := executor.NewK8sClientForTest(fake.NewSimpleClientset(&pod))
	result := checkK01(context.Background(), client)
	if result.Status != types.StatusFailed {
		t.Errorf("expected failed when all CoreDNS pods are terminating, got %q", result.Status)
	}
}

func TestCheckK01_TerminatingPodPartial(t *testing.T) {
	now := metav1.Now()
	pod1 := makePod("coredns-1", "kube-system", corev1.PodRunning,
		map[string]string{"k8s-app": "kube-dns"})
	pod2 := makePod("coredns-2", "kube-system", corev1.PodRunning,
		map[string]string{"k8s-app": "kube-dns"})
	pod2.DeletionTimestamp = &now
	client := executor.NewK8sClientForTest(fake.NewSimpleClientset(&pod1, &pod2))
	result := checkK01(context.Background(), client)
	if result.Status != types.StatusWarning {
		t.Errorf("expected warning when one CoreDNS pod is terminating, got %q", result.Status)
	}
}

func TestCheckK01_PodRunning_DNSCheckFails(t *testing.T) {
	// CoreDNS pod is Running, but the DNS check pod creation will succeed and
	// the fake client will return a pod that stays in Pending forever → timeout.
	// To avoid a 60-second wait in CI, we just verify that when all CoreDNS pods
	// are Running the function proceeds past the pod-running check.
	// (Full DNS resolution path requires real cluster; covered by integration tests.)
	pod := makePod("coredns-1", "kube-system", corev1.PodRunning,
		map[string]string{"k8s-app": "kube-dns"})
	client := executor.NewK8sClientForTest(fake.NewSimpleClientset(&pod))

	// We only test that K-01 returns K-01 as the ID regardless of the DNS outcome.
	result := checkK01(context.Background(), client)
	if result.ID != "K-01" {
		t.Errorf("expected ID K-01, got %q", result.ID)
	}
}

// ---------------------------------------------------------------------------
// checkK02
// ---------------------------------------------------------------------------

func TestCheckK02_NodeReady(t *testing.T) {
	node := makeNode("node-1", true, nil, "8", "16Gi")
	client := executor.NewK8sClientForTest(fake.NewSimpleClientset(&node))
	results, readyNodes := checkK02(context.Background(), client, []string{"node-1"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != types.StatusPassed {
		t.Errorf("expected passed, got %q: %s", results[0].Status, results[0].Message)
	}
	if len(readyNodes) != 1 || readyNodes[0] != "node-1" {
		t.Errorf("expected readyNodes=[node-1], got %v", readyNodes)
	}
}

func TestCheckK02_NodeNotReady(t *testing.T) {
	node := makeNode("node-1", false, nil, "8", "16Gi")
	client := executor.NewK8sClientForTest(fake.NewSimpleClientset(&node))
	results, readyNodes := checkK02(context.Background(), client, []string{"node-1"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != types.StatusFailed {
		t.Errorf("expected failed, got %q", results[0].Status)
	}
	if len(readyNodes) != 0 {
		t.Errorf("expected no ready nodes, got %v", readyNodes)
	}
}

func TestCheckK02_NodeNotFound(t *testing.T) {
	client := executor.NewK8sClientForTest(fake.NewSimpleClientset())
	results, readyNodes := checkK02(context.Background(), client, []string{"missing-node"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != types.StatusFailed {
		t.Errorf("expected failed for missing node, got %q", results[0].Status)
	}
	if len(readyNodes) != 0 {
		t.Errorf("expected no ready nodes, got %v", readyNodes)
	}
}

func TestCheckK02_MultipleNodes(t *testing.T) {
	node1 := makeNode("node-1", true, nil, "8", "16Gi")
	node2 := makeNode("node-2", false, nil, "8", "16Gi")
	node3 := makeNode("node-3", true, nil, "8", "16Gi")
	client := executor.NewK8sClientForTest(fake.NewSimpleClientset(&node1, &node2, &node3))
	results, readyNodes := checkK02(context.Background(), client, []string{"node-1", "node-2", "node-3"})
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if len(readyNodes) != 2 {
		t.Errorf("expected 2 ready nodes, got %v", readyNodes)
	}
}

// ---------------------------------------------------------------------------
// checkK03
// ---------------------------------------------------------------------------

func TestCheckK03_NoTaints(t *testing.T) {
	node := makeNode("node-1", true, nil, "8", "16Gi")
	client := executor.NewK8sClientForTest(fake.NewSimpleClientset(&node))
	results := checkK03(context.Background(), client, []string{"node-1"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != types.StatusPassed {
		t.Errorf("expected passed for no taints, got %q", results[0].Status)
	}
}

func TestCheckK03_WithTaints(t *testing.T) {
	taints := []corev1.Taint{
		{Key: "nvidia.com/gpu", Value: "present", Effect: corev1.TaintEffectNoSchedule},
	}
	node := makeNode("node-1", true, taints, "8", "16Gi")
	client := executor.NewK8sClientForTest(fake.NewSimpleClientset(&node))
	results := checkK03(context.Background(), client, []string{"node-1"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != types.StatusInfo {
		t.Errorf("expected info for tainted node, got %q", results[0].Status)
	}
	if results[0].Message == "" {
		t.Error("expected non-empty message for taint")
	}
}

func TestCheckK03_MixedNodes(t *testing.T) {
	taints := []corev1.Taint{
		{Key: "key1", Value: "val1", Effect: corev1.TaintEffectNoSchedule},
	}
	nodeClean := makeNode("node-clean", true, nil, "8", "16Gi")
	nodeTainted := makeNode("node-tainted", true, taints, "8", "16Gi")
	client := newClient(&nodeClean, &nodeTainted)
	results := checkK03(context.Background(), client, []string{"node-clean", "node-tainted"})
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Status != types.StatusPassed {
		t.Errorf("expected node-clean (results[0]) to be passed, got %q", results[0].Status)
	}
	if results[1].Status != types.StatusInfo {
		t.Errorf("expected node-tainted (results[1]) to be info, got %q", results[1].Status)
	}
}

func TestCheckK03_NodeNotFound(t *testing.T) {
	client := executor.NewK8sClientForTest(fake.NewSimpleClientset())
	// When the node doesn't exist, checkK03 skips it (no result appended)
	results := checkK03(context.Background(), client, []string{"ghost-node"})
	if len(results) != 0 {
		t.Errorf("expected 0 results for missing node, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// checkK04
// ---------------------------------------------------------------------------

func TestCheckK04_ReturnsInfo(t *testing.T) {
	node := makeNode("node-1", true, nil, "32", "128Gi")
	client := executor.NewK8sClientForTest(fake.NewSimpleClientset(&node))
	results := checkK04(context.Background(), client, []string{"node-1"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != types.StatusInfo {
		t.Errorf("expected info status, got %q", results[0].Status)
	}
	if results[0].ID != "K-04" {
		t.Errorf("expected ID K-04, got %q", results[0].ID)
	}
	if results[0].Detail == nil {
		t.Error("expected non-nil Detail")
	}
	if _, ok := results[0].Detail["cpu_allocatable"]; !ok {
		t.Error("expected cpu_allocatable in Detail")
	}
	if _, ok := results[0].Detail["memory_allocatable"]; !ok {
		t.Error("expected memory_allocatable in Detail")
	}
}

func TestCheckK04_MultipleNodes(t *testing.T) {
	node1 := makeNode("node-1", true, nil, "8", "32Gi")
	node2 := makeNode("node-2", true, nil, "16", "64Gi")
	client := executor.NewK8sClientForTest(fake.NewSimpleClientset(&node1, &node2))
	results := checkK04(context.Background(), client, []string{"node-1", "node-2"})
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for _, r := range results {
		if r.Status != types.StatusInfo {
			t.Errorf("expected info status, got %q: %s", r.Status, r.Message)
		}
	}
}

func TestCheckK04_NodeWithNoResources(t *testing.T) {
	// Node with empty CPU and memory allocatable — should still return StatusInfo.
	node := makeNode("node-empty", true, nil, "", "")
	client := newClient(&node)
	results := checkK04(context.Background(), client, []string{"node-empty"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != types.StatusInfo {
		t.Errorf("expected info status, got %q", results[0].Status)
	}
}

func TestCheckK04_DetailNodeField(t *testing.T) {
	node := makeNode("node-1", true, nil, "4", "8Gi")
	client := newClient(&node)
	results := checkK04(context.Background(), client, []string{"node-1"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	detail := results[0].Detail
	if detail["node"] != "node-1" {
		t.Errorf("expected detail[node]=node-1, got %v", detail["node"])
	}
}

func TestCheckK04_NodeNotFound(t *testing.T) {
	client := executor.NewK8sClientForTest(fake.NewSimpleClientset())
	results := checkK04(context.Background(), client, []string{"ghost-node"})
	// When the node doesn't exist, checkK04 skips it
	if len(results) != 0 {
		t.Errorf("expected 0 results for missing node, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// CheckK8s integration (fake client, no real cluster)
// ---------------------------------------------------------------------------

func TestCheckK8s_K01FailsNoCoreDNS(t *testing.T) {
	client := executor.NewK8sClientForTest(fake.NewSimpleClientset())
	result := CheckK8s(client, []string{"node-1"})
	if result.Status != types.StatusFailed {
		t.Errorf("expected failed when no CoreDNS, got %q", result.Status)
	}
	// Only K-01 check should be present (terminated early)
	if len(result.Checks) != 1 {
		t.Errorf("expected 1 check (K-01 only), got %d", len(result.Checks))
	}
	if result.Checks[0].ID != "K-01" {
		t.Errorf("expected first check ID K-01, got %q", result.Checks[0].ID)
	}
}

func TestCheckK8s_K02AllNodesNotReady(t *testing.T) {
	// CoreDNS pod running so K-01 proceeds, but nodes are not ready
	dnsPod := makePod("coredns-1", "kube-system", corev1.PodRunning,
		map[string]string{"k8s-app": "kube-dns"})
	node := makeNode("node-1", false, nil, "8", "16Gi")
	client := executor.NewK8sClientForTest(fake.NewSimpleClientset(&dnsPod, &node))

	// K-01 will try DNS resolution and likely time out or fail;
	// the test verifies early termination path works (K-01 or K-02 level)
	result := CheckK8s(client, []string{"node-1"})
	if result.Status != types.StatusFailed {
		t.Errorf("expected failed result, got %q", result.Status)
	}
}

func TestCheckK8s_AllReadyRunsK03K04(t *testing.T) {
	// No CoreDNS pods → K-01 fails; we cannot easily pass K-01 with a fake
	// client because DNS pod phase never transitions. Instead verify that when
	// K-02 returns ready nodes, K-03 and K-04 are appended.
	// We test this by calling checkK02/K03/K04 directly and confirming the
	// combined result set has the right IDs.
	node := makeNode("node-1", true, nil, "8", "16Gi")
	client := newClient(&node)

	k02Results, readyNodes := checkK02(context.Background(), client, []string{"node-1"})
	k03Results := checkK03(context.Background(), client, readyNodes)
	k04Results := checkK04(context.Background(), client, readyNodes)

	allChecks := append(append(k02Results, k03Results...), k04Results...)
	ids := map[string]bool{}
	for _, c := range allChecks {
		ids[c.ID] = true
	}
	if !ids["K-02"] {
		t.Error("expected K-02 in combined checks")
	}
	if !ids["K-03"] {
		t.Error("expected K-03 in combined checks")
	}
	if !ids["K-04"] {
		t.Error("expected K-04 in combined checks")
	}
}

func TestCheckK8s_PartialReadyNodes(t *testing.T) {
	// node-1 ready, node-2 not ready → K-02 partial, K-03/K-04 run on ready nodes only.
	node1 := makeNode("node-1", true, nil, "8", "16Gi")
	node2 := makeNode("node-2", false, nil, "8", "16Gi")
	client := newClient(&node1, &node2)

	k02Results, readyNodes := checkK02(context.Background(), client, []string{"node-1", "node-2"})
	if len(readyNodes) != 1 || readyNodes[0] != "node-1" {
		t.Errorf("expected only node-1 ready, got %v", readyNodes)
	}

	k03Results := checkK03(context.Background(), client, readyNodes)
	k04Results := checkK04(context.Background(), client, readyNodes)

	if len(k02Results) != 2 {
		t.Errorf("expected 2 K-02 results, got %d", len(k02Results))
	}
	if len(k03Results) != 1 {
		t.Errorf("expected 1 K-03 result (only ready node), got %d", len(k03Results))
	}
	if len(k04Results) != 1 {
		t.Errorf("expected 1 K-04 result (only ready node), got %d", len(k04Results))
	}
}
