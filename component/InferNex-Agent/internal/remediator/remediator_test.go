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

package remediator

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infernexv1alpha1 "gitcode.com/openFuyao/InferNex/api/v1alpha1"
)

func TestEnsureRecoveryUsesOnlyApprovedProfileAndIsIdempotent(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := infernexv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("register scheme: %v", err)
	}
	profile := &infernexv1alpha1.InferNexServiceConfig{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "infernex-bridge-system",
			Name:      "qwen-pd-recovery-v1",
			Labels:    map[string]string{ApprovedProfileLabel: "true"},
		},
		Spec: infernexv1alpha1.InferNexServiceConfigSpec{
			InferNexServiceSpec: infernexv1alpha1.InferNexServiceSpec{
				Model: &infernexv1alpha1.LLMModelSpec{Name: "qwen"},
			},
		},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(profile).Build()
	domainRemediator, err := New(kubeClient, "infernex-bridge-system")
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	request := Request{
		Namespace:  "models",
		SourceName: "qwen-pd",
		Profile:    "qwen-pd-recovery-v1",
	}
	first, err := domainRemediator.EnsureRecovery(context.Background(), request)
	if err != nil {
		t.Fatalf("EnsureRecovery returned error: %v", err)
	}
	if first.Action != "created" || first.Name != "qwen-pd-recovery" {
		t.Fatalf("first result = %#v", first)
	}
	created := &infernexv1alpha1.InferNexService{}
	if err := kubeClient.Get(
		context.Background(),
		types.NamespacedName{Namespace: "models", Name: "qwen-pd-recovery"},
		created,
	); err != nil {
		t.Fatalf("get recovery service: %v", err)
	}
	if len(created.Spec.BaseRefs) != 1 ||
		created.Spec.BaseRefs[0].Name != "qwen-pd-recovery-v1" ||
		created.Labels[managedLabel] != "true" {
		t.Fatalf("recovery service = %#v", created)
	}

	second, err := domainRemediator.EnsureRecovery(context.Background(), request)
	if err != nil {
		t.Fatalf("idempotent EnsureRecovery returned error: %v", err)
	}
	if second.Action != "unchanged" {
		t.Fatalf("second result = %#v", second)
	}
}

func TestEnsureRecoveryRejectsUnapprovedProfile(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := infernexv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("register scheme: %v", err)
	}
	profile := &infernexv1alpha1.InferNexServiceConfig{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "infernex-bridge-system",
			Name:      "unapproved",
		},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(profile).Build()
	domainRemediator, err := New(kubeClient, "infernex-bridge-system")
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	_, err = domainRemediator.EnsureRecovery(context.Background(), Request{
		Namespace: "models", SourceName: "qwen", Profile: "unapproved",
	})
	if err == nil || !strings.Contains(err.Error(), ApprovedProfileLabel) {
		t.Fatalf("error = %v, want approval-label rejection", err)
	}
}
