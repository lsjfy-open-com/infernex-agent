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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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

func TestSnapshotRestorePreservesConcurrentReplacementOrEdit(t *testing.T) {
	for _, mutation := range []string{"replacement", "edit"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			if err := infernexv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			added := &infernexv1alpha1.InferNexService{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "models", Name: "post-snapshot", UID: "original-service",
					Labels:      map[string]string{managedByLabel: managedByAgent},
					Annotations: map[string]string{changeIDAnnotation: "change"},
				},
			}
			var retained *infernexv1alpha1.InferNexService
			deleteCalled := false
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).
				WithInterceptorFuncs(interceptor.Funcs{
					Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
						deleteCalled = true
						pre := (&client.DeleteOptions{}).ApplyOptions(opts).Preconditions
						if pre == nil || pre.UID == nil || pre.ResourceVersion == nil ||
							*pre.UID != added.UID || *pre.ResourceVersion != added.ResourceVersion {
							t.Fatalf("snapshot rollback must bind the listed object identity: %#v", pre)
						}
						retained = added.DeepCopy()
						retained.Spec.BaseRefs = []infernexv1alpha1.NamedRef{{Name: "operator-change"}}
						if mutation == "replacement" {
							if err := c.Delete(ctx, added); err != nil {
								t.Fatal(err)
							}
							retained.UID, retained.ResourceVersion = "replacement-service", ""
							if err := c.Create(ctx, retained); err != nil {
								t.Fatal(err)
							}
						} else if err := c.Update(ctx, retained); err != nil {
							t.Fatal(err)
						}
						// The fake does not enforce UID preconditions; return the
						// conflict the API server would produce for this stale delete.
						if retained.UID == *pre.UID && retained.ResourceVersion == *pre.ResourceVersion {
							t.Fatal("test mutation did not change the protected identity")
						}
						return apierrors.NewConflict(infernexv1alpha1.GroupVersion.WithResource("infernexservices").GroupResource(), obj.GetName(), errors.New("delete precondition failed"))
					},
				}).Build()
			snapshot, err := Capture(ctx, kubeClient, []string{"models"}, "pre-install")
			if err != nil {
				t.Fatal(err)
			}
			if err := kubeClient.Create(ctx, added); err != nil {
				t.Fatal(err)
			}
			result, err := Restore(ctx, kubeClient, snapshot)
			if !deleteCalled || !apierrors.IsConflict(err) || len(result.Deleted) != 0 {
				t.Fatalf("expected restore conflict without reported deletion, result=%#v err=%v", result, err)
			}
			current := &infernexv1alpha1.InferNexService{}
			if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(retained), current); err != nil {
				t.Fatalf("concurrent service was removed: %v", err)
			}
			if current.UID != retained.UID || current.Spec.BaseRefs[0].Name != "operator-change" {
				t.Fatalf("concurrent service was changed: %#v", current)
			}
		})
	}
}
