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

package changesafety

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infernexv1alpha1 "gitcode.com/openFuyao/InferNex/api/v1alpha1"
)

func TestSnapshotRoundTripAndSafeRestore(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := infernexv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	original := &infernexv1alpha1.InferNexService{}
	original.APIVersion = infernexv1alpha1.GroupVersion.String()
	original.Kind = "InferNexService"
	original.Namespace = "models"
	original.Name = "existing"
	original.Labels = map[string]string{managedByLabel: managedByAgent}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(original).Build()

	snapshot, err := Capture(context.Background(), kubeClient, []string{"models"}, "pre-install")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := WriteSnapshot(path, snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot, err = ReadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}

	added := original.DeepCopy()
	added.Name = "new-by-agent"
	added.ResourceVersion = ""
	added.UID = ""
	added.Annotations = map[string]string{changeIDAnnotation: "00112233445566778899aabbccddeeff"}
	if err := kubeClient.Create(context.Background(), added); err != nil {
		t.Fatal(err)
	}
	unrelated := original.DeepCopy()
	unrelated.Name = "unrelated"
	unrelated.ResourceVersion = ""
	unrelated.UID = ""
	unrelated.Labels = nil
	if err := kubeClient.Create(context.Background(), unrelated); err != nil {
		t.Fatal(err)
	}

	result, err := Restore(context.Background(), kubeClient, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Deleted) != 1 || len(result.Skipped) != 1 {
		t.Fatalf("restore result = %#v", result)
	}
	key := types.NamespacedName{Namespace: "models", Name: "new-by-agent"}
	if err := kubeClient.Get(context.Background(), key, &infernexv1alpha1.InferNexService{}); err == nil {
		t.Fatal("Agent-created post-snapshot service was not deleted")
	}
	key.Name = "unrelated"
	if err := kubeClient.Get(context.Background(), key, &infernexv1alpha1.InferNexService{}); err != nil {
		t.Fatalf("unrelated service was changed: %v", err)
	}
}

func TestSnapshotRejectsTampering(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := infernexv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	snapshot, err := Capture(context.Background(), kubeClient, []string{"models"}, "pre-install")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := WriteSnapshot(path, snapshot); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(contents), "pre-install", "post-install", 1)
	if err := os.WriteFile(path, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSnapshot(path); err == nil ||
		!strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("tampered snapshot error = %v", err)
	}
}
