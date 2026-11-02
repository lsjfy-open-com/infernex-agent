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
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testImage = "example.invalid/model@sha256:0000000000000000000000000000000000000000000000000000000000000000"

func TestGeneratePlacesTwoFourDeviceReplicasOnSeparateNodes(t *testing.T) {
	reader := fakeReader(t, readyNode("node-a", "8", "64Gi", "4"), readyNode("node-b", "8", "64Gi", "4"))
	request := testRequest(2)
	request.Profile.ExtendedResources = map[string]int64{"huawei.com/Ascend910": 4}

	plan, err := Generate(context.Background(), reader, request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != StatusSchedulable || plan.PlaceableReplicas != 2 || len(plan.Placements) != 2 {
		t.Fatalf("plan = %#v, want two separate placements", plan)
	}
	for _, placement := range plan.Placements {
		if placement.Replicas != 1 {
			t.Fatalf("placement = %#v, want one replica", placement)
		}
	}
	if plan.ResourceEstimate.TotalRequested["huawei.com/Ascend910"] != "8" {
		t.Fatalf("resource estimate = %#v", plan.ResourceEstimate)
	}
}

func TestGenerateReportsInsufficientExactResources(t *testing.T) {
	reader := fakeReader(t, readyNode("node-a", "8", "64Gi", "4"))
	request := testRequest(2)
	request.Profile.ExtendedResources = map[string]int64{"huawei.com/Ascend910": 4}
	plan, err := Generate(context.Background(), reader, request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != StatusInsufficient || plan.PlaceableReplicas != 1 {
		t.Fatalf("plan = %#v, want one placeable and insufficient", plan)
	}
}

func TestGenerateFailsClosedWhenPodListForbidden(t *testing.T) {
	base := fakeReader(t, readyNode("node-a", "8", "64Gi", "4"))
	_, err := Generate(context.Background(), forbiddenPodReader{Reader: base}, testRequest(1))
	if err == nil || !strings.Contains(err.Error(), "read pods") {
		t.Fatalf("error = %v, want pod read failure", err)
	}
}

func TestGenerateCountsTerminatingPods(t *testing.T) {
	now := metav1.NewTime(time.Now())
	pod := boundPod("terminating", "node-a", "4", "1Gi")
	pod.DeletionTimestamp = &now
	pod.Finalizers = []string{"test.infernex.openfuyao.io/hold"}
	reader := fakeReader(t, readyNode("node-a", "8", "64Gi", "4"), pod)
	request := testRequest(2)
	request.Profile.CPU = "4"
	plan, err := Generate(context.Background(), reader, request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != StatusInsufficient || plan.PlaceableReplicas != 1 {
		t.Fatalf("plan = %#v, terminating pod must consume resources", plan)
	}
}

func TestGenerateAccountsInitSidecarsAndOverhead(t *testing.T) {
	restartAlways := corev1.ContainerRestartPolicyAlways
	pod := boundPod("complex", "node-a", "1", "1Gi")
	pod.Spec.InitContainers = []corev1.Container{
		{Name: "sidecar", RestartPolicy: &restartAlways, Resources: resources("500m", "1Gi")},
		{Name: "init", Resources: resources("3", "1Gi")},
	}
	pod.Spec.Overhead = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")}
	node := readyNode("node-a", "7", "64Gi", "4")
	reader := fakeReader(t, node, pod)
	request := testRequest(1)
	request.Profile.CPU = "4"
	plan, err := Generate(context.Background(), reader, request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != StatusInsufficient {
		t.Fatalf("plan = %#v, effective existing request is 4 CPU", plan)
	}
}

func TestGenerateHashStableAndDriftsWithResourceVersion(t *testing.T) {
	node := readyNode("node-a", "8", "64Gi", "4")
	node.ResourceVersion = "10"
	first, err := Generate(context.Background(), fakeReader(t, node), testRequest(1))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Generate(context.Background(), fakeReader(t, node.DeepCopy()), testRequest(1))
	if err != nil {
		t.Fatal(err)
	}
	if first.PlanHash != second.PlanHash || first.SnapshotHash != second.SnapshotHash {
		t.Fatalf("stable inputs produced different hashes: %#v %#v", first, second)
	}
	node.ResourceVersion = "11"
	drifted, err := Generate(context.Background(), fakeReader(t, node), testRequest(1))
	if err != nil {
		t.Fatal(err)
	}
	if first.PlanHash == drifted.PlanHash || first.SnapshotHash == drifted.SnapshotHash {
		t.Fatalf("resource-version drift did not change hashes")
	}
}

func TestGenerateHonorsResourceQuota(t *testing.T) {
	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "pods", Namespace: "default"},
		Spec:       corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{corev1.ResourcePods: resource.MustParse("2")}},
		Status: corev1.ResourceQuotaStatus{
			Hard: corev1.ResourceList{corev1.ResourcePods: resource.MustParse("2")},
			Used: corev1.ResourceList{corev1.ResourcePods: resource.MustParse("1")},
		},
	}
	reader := fakeReader(t, readyNode("node-a", "16", "64Gi", "4"), quota)
	plan, err := Generate(context.Background(), reader, testRequest(2))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != StatusInsufficient || plan.PlaceableReplicas != 1 || len(plan.Reasons) == 0 {
		t.Fatalf("plan = %#v, want quota-limited estimate", plan)
	}
}

func TestGenerateHonorsTaintsAndTolerations(t *testing.T) {
	node := readyNode("node-a", "8", "64Gi", "4")
	node.Spec.Taints = []corev1.Taint{{Key: "dedicated", Value: "inference", Effect: corev1.TaintEffectNoSchedule}}
	request := testRequest(1)
	without, err := Generate(context.Background(), fakeReader(t, node), request)
	if err != nil {
		t.Fatal(err)
	}
	if without.Status != StatusInsufficient {
		t.Fatalf("without toleration = %#v", without)
	}
	request.Profile.Tolerations = []corev1.Toleration{{Key: "dedicated", Value: "inference", Operator: corev1.TolerationOpEqual, Effect: corev1.TaintEffectNoSchedule}}
	with, err := Generate(context.Background(), fakeReader(t, node), request)
	if err != nil {
		t.Fatal(err)
	}
	if with.Status != StatusSchedulable {
		t.Fatalf("with toleration = %#v", with)
	}
}

func TestGenerateNodeSelectorRequiresKeyEvenForEmptyValue(t *testing.T) {
	request := testRequest(1)
	request.Profile.NodeSelector = map[string]string{"accelerator": ""}
	plan, err := Generate(context.Background(), fakeReader(t, readyNode("node-a", "8", "64Gi", "4")), request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != StatusInsufficient {
		t.Fatalf("plan = %#v, missing selector key must not match", plan)
	}
}

func TestGenerateBlocksNodeWithInPlaceResizeState(t *testing.T) {
	pod := boundPod("resizing", "node-a", "1", "1Gi")
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "main", AllocatedResources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("1Gi")},
	}}
	plan, err := Generate(context.Background(), fakeReader(t, readyNode("node-a", "8", "64Gi", "4"), pod), testRequest(1))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != StatusBlocked {
		t.Fatalf("plan = %#v, resized allocation must be unknown", plan)
	}
}

func TestGenerateBlocksUnsupportedQuotaResource(t *testing.T) {
	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "limits", Namespace: "default"},
		Spec:       corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{corev1.ResourceLimitsCPU: resource.MustParse("8")}},
		Status: corev1.ResourceQuotaStatus{
			Hard: corev1.ResourceList{corev1.ResourceLimitsCPU: resource.MustParse("8")},
			Used: corev1.ResourceList{corev1.ResourceLimitsCPU: resource.MustParse("1")},
		},
	}
	plan, err := Generate(context.Background(), fakeReader(t, readyNode("node-a", "8", "64Gi", "4"), quota), testRequest(1))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != StatusBlocked {
		t.Fatalf("plan = %#v, limits quota must be blocked", plan)
	}
}

func TestGenerateFailsClosedOnStuckPagination(t *testing.T) {
	base := fakeReader(t, readyNode("node-a", "8", "64Gi", "4"))
	_, err := Generate(context.Background(), stuckNodePaginationReader{Reader: base}, testRequest(1))
	if err == nil || !strings.Contains(err.Error(), "pagination token") {
		t.Fatalf("error = %v", err)
	}
}

func TestGenerateBlocksTerminatingNamespace(t *testing.T) {
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default", UID: "uid-default", ResourceVersion: "2"}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating}}
	plan, err := Generate(context.Background(), fakeReader(t, namespace, readyNode("node-a", "8", "64Gi", "4")), testRequest(1))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != StatusBlocked || !strings.Contains(plan.Reasons[0], "terminating") {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestGenerateBlocksNominatedNode(t *testing.T) {
	pod := boundPod("nominated", "", "1", "1Gi")
	pod.Status.Phase = corev1.PodPending
	pod.Status.NominatedNodeName = "node-a"
	plan, err := Generate(context.Background(), fakeReader(t, readyNode("node-a", "8", "64Gi", "4"), pod), testRequest(1))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != StatusBlocked {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestGenerateRejectsInvalidToleration(t *testing.T) {
	request := testRequest(1)
	request.Profile.Tolerations = []corev1.Toleration{{Operator: corev1.TolerationOpEqual}}
	if _, err := Generate(context.Background(), nil, request); err == nil || !strings.Contains(err.Error(), "requires a key") {
		t.Fatalf("error = %v", err)
	}
}

func TestGenerateBlocksUnsupportedProfileConstraints(t *testing.T) {
	request := testRequest(1)
	request.Profile.ResourceClaims = []corev1.PodResourceClaim{{Name: "device"}}
	plan, err := Generate(context.Background(), nil, request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != StatusBlocked || len(plan.Reasons) == 0 || plan.TrafficVerified || plan.PerformanceVerified || plan.ReservationCreated {
		t.Fatalf("plan = %#v, want blocked and all verification fields false", plan)
	}
}

func TestGenerateBlocksLimitRange(t *testing.T) {
	limit := &corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: "defaults", Namespace: "default"}}
	plan, err := Generate(context.Background(), fakeReader(t, readyNode("node-a", "8", "64Gi", "4"), limit), testRequest(1))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != StatusBlocked {
		t.Fatalf("plan = %#v", plan)
	}
}

func testRequest(replicas int) Request {
	return Request{Namespace: "default", Replicas: replicas, Profile: Profile{
		Version: ProfileVersion, Image: testImage, CPU: "1", Memory: "1Gi",
	}}
}

func readyNode(name, cpu, memory, devices string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-" + name), ResourceVersion: "1"},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(memory),
				corev1.ResourcePods: resource.MustParse("110"), corev1.ResourceName("huawei.com/Ascend910"): resource.MustParse(devices),
			},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
}

func boundPod(name, node, cpu, memory string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "other", UID: types.UID("uid-" + name), ResourceVersion: "1"},
		Spec:       corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Name: "main", Resources: resources(cpu, memory)}}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func resources(cpu, memory string) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{Requests: corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(memory),
	}}
}

func fakeReader(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	hasNamespace := false
	for _, object := range objects {
		if _, ok := object.(*corev1.Namespace); ok {
			hasNamespace = true
		}
	}
	if !hasNamespace {
		objects = append([]client.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default", UID: "uid-default", ResourceVersion: "1"}}}, objects...)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

type forbiddenPodReader struct{ client.Reader }

func (r forbiddenPodReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*corev1.PodList); ok {
		return apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("denied"))
	}
	return r.Reader.List(ctx, list, opts...)
}

type stuckNodePaginationReader struct{ client.Reader }

func (r stuckNodePaginationReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if err := r.Reader.List(ctx, list, opts...); err != nil {
		return err
	}
	if nodes, ok := list.(*corev1.NodeList); ok {
		nodes.Continue = "stuck"
	}
	return nil
}
