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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/deploymentplan"
)

func TestRunDeploymentPlanReadsProfileFileAndWritesPlan(t *testing.T) {
	profilePath := writeDeploymentProfile(t, `{
  "version":"infernex.openfuyao.io/v1alpha1",
  "image":"example.invalid/model@sha256:0000000000000000000000000000000000000000000000000000000000000000",
  "cpu":"100m",
  "memory":"64Mi"
}`)
	reader := deploymentPlanTestReader(t)
	var stdout, stderr bytes.Buffer
	err := runDeploymentPlanWithIO(context.Background(), []string{
		"--profile", profilePath, "--namespace", "default", "--replicas", "2",
	}, &stdout, &stderr, func(kubeconfig string) (client.Reader, error) {
		if kubeconfig != "" {
			t.Fatalf("kubeconfig = %q", kubeconfig)
		}
		return reader, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var plan deploymentplan.Plan
	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if plan.Status != deploymentplan.StatusSchedulable || plan.RequestedReplicas != 2 || plan.PlanHash == "" {
		t.Fatalf("plan = %#v", plan)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunDeploymentPlanRejectsUnknownProfileFieldsBeforeClusterAccess(t *testing.T) {
	profilePath := writeDeploymentProfile(t, `{
  "version":"infernex.openfuyao.io/v1alpha1",
  "image":"example.invalid/model@sha256:0000000000000000000000000000000000000000000000000000000000000000",
  "cpu":"100m", "memory":"64Mi", "command":["unsafe"]
}`)
	called := false
	err := runDeploymentPlanWithIO(context.Background(), []string{"--profile", profilePath}, &bytes.Buffer{}, &bytes.Buffer{}, func(string) (client.Reader, error) {
		called = true
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v", err)
	}
	if called {
		t.Fatal("cluster reader was created for an invalid profile")
	}
}

func TestReadDeploymentProfileRejectsOversizeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.json")
	if err := os.WriteFile(path, bytes.Repeat([]byte{'x'}, maxDeploymentProfileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readDeploymentProfile(path)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v", err)
	}
}

func writeDeploymentProfile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profile.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func deploymentPlanTestReader(t *testing.T) client.Reader {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default", UID: "namespace-uid", ResourceVersion: "1"}}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-a", UID: "node-uid", ResourceVersion: "1"},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("8"), corev1.ResourceMemory: resource.MustParse("64Gi"), corev1.ResourcePods: resource.MustParse("110"),
			},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(namespace, node).Build()
}
