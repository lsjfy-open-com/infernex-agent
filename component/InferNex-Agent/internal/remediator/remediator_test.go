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
	"errors"
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
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/changesafety"
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
	source := recoverySource("models", "qwen-pd", profile.Name)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(profile, source).Build()
	store := changesafety.NewMemoryStore()
	domainRemediator, err := New(
		kubeClient,
		"infernex-bridge-system",
		WithStore(store),
	)
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
	if first.Action != "created" ||
		first.Name != "qwen-pd-recovery" ||
		first.ChangeID == "" {
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
		created.Labels[managedLabel] != "true" ||
		created.Labels[managedByLabel] != "infernex-agent" ||
		created.Annotations[changeIDAnnotation] != first.ChangeID {
		t.Fatalf("recovery service = %#v", created)
	}
	record, err := store.Latest(first.ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != changesafety.StatusCommitted ||
		record.Action != "create-recovery" {
		t.Fatalf("recovery change record = %#v", record)
	}

	second, err := domainRemediator.EnsureRecovery(context.Background(), request)
	if err != nil {
		t.Fatalf("idempotent EnsureRecovery returned error: %v", err)
	}
	if second.Action != "unchanged" {
		t.Fatalf("second result = %#v", second)
	}
	if second.ChangeID != first.ChangeID {
		t.Fatalf("idempotent changeId = %q, want %q", second.ChangeID, first.ChangeID)
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
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(profile, recoverySource("models", "qwen", profile.Name)).Build()
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

type failingCommitStore struct {
	changesafety.Store
}

func (s failingCommitStore) Append(record changesafety.ChangeRecord) error {
	if record.Status == changesafety.StatusCommitted {
		return errors.New("commit storage unavailable")
	}
	return s.Store.Append(record)
}

func TestEmergencyRollbackPreservesConcurrentlyChangedRecovery(t *testing.T) {
	for _, mutation := range []string{"replacement", "edit"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			if err := infernexv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			profile := &infernexv1alpha1.InferNexServiceConfig{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "templates", Name: "approved",
					Labels: map[string]string{ApprovedProfileLabel: "true"},
				},
			}
			var created *infernexv1alpha1.InferNexService
			var retained *infernexv1alpha1.InferNexService
			deleteCalled := false
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(profile, recoverySource("models", "qwen", profile.Name)).
				WithInterceptorFuncs(interceptor.Funcs{
					Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
						obj.SetUID("original-recovery")
						if err := c.Create(ctx, obj, opts...); err != nil {
							return err
						}
						created = obj.(*infernexv1alpha1.InferNexService).DeepCopy()
						return nil
					},
					Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
						deleteCalled = true
						pre := (&client.DeleteOptions{}).ApplyOptions(opts).Preconditions
						if pre == nil || pre.UID == nil || pre.ResourceVersion == nil ||
							*pre.UID != created.UID || *pre.ResourceVersion != created.ResourceVersion {
							t.Fatalf("emergency rollback must bind the create response identity: %#v", pre)
						}
						retained = created.DeepCopy()
						retained.Spec.BaseRefs = []infernexv1alpha1.NamedRef{{Name: "operator-change"}}
						if mutation == "replacement" {
							if err := c.Delete(ctx, created); err != nil {
								t.Fatal(err)
							}
							retained.UID, retained.ResourceVersion = "replacement-recovery", ""
							if err := c.Create(ctx, retained); err != nil {
								t.Fatal(err)
							}
						} else if err := c.Update(ctx, retained); err != nil {
							t.Fatal(err)
						}
						// The fake client does not enforce UID preconditions, so model
						// the API server's conflict after the concurrent mutation.
						if retained.UID == *pre.UID && retained.ResourceVersion == *pre.ResourceVersion {
							t.Fatal("test mutation did not change the protected identity")
						}
						return apierrors.NewConflict(infernexv1alpha1.GroupVersion.WithResource("infernexservices").GroupResource(), obj.GetName(), errors.New("delete precondition failed"))
					},
				}).Build()
			r, err := New(kubeClient, "templates", WithStore(failingCommitStore{changesafety.NewMemoryStore()}))
			if err != nil {
				t.Fatal(err)
			}
			_, err = r.EnsureRecovery(ctx, Request{Namespace: "models", SourceName: "qwen", Profile: "approved"})
			if !deleteCalled || !apierrors.IsConflict(err) || !strings.Contains(err.Error(), "emergency rollback") {
				t.Fatalf("expected emergency rollback conflict, got %v", err)
			}
			current := &infernexv1alpha1.InferNexService{}
			if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(retained), current); err != nil {
				t.Fatalf("concurrent recovery resource was removed: %v", err)
			}
			if current.UID != retained.UID || current.Spec.BaseRefs[0].Name != "operator-change" {
				t.Fatalf("concurrent recovery resource was changed: %#v", current)
			}
		})
	}
}

func recoverySource(namespace, name, profile string) *infernexv1alpha1.InferNexService {
	return &infernexv1alpha1.InferNexService{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace, Name: name, UID: "source-uid", Generation: 1,
			Annotations: map[string]string{
				autoRecoveryAnnotation: "true", recoveryProfileAnnotation: profile,
			},
		},
		Status: infernexv1alpha1.InferNexServiceStatus{ObservedGeneration: 1},
	}
}

func approvedProfile() *infernexv1alpha1.InferNexServiceConfig {
	return &infernexv1alpha1.InferNexServiceConfig{ObjectMeta: metav1.ObjectMeta{
		Namespace: "templates", Name: "approved", Labels: map[string]string{ApprovedProfileLabel: "true"},
	}}
}

func TestRecoveryRequiresCurrentSourceAndProfileApproval(t *testing.T) {
	for _, test := range []struct {
		name       string
		omitSource bool
		change     func(*infernexv1alpha1.InferNexService, *infernexv1alpha1.InferNexServiceConfig, *Request)
	}{
		{name: "source missing", omitSource: true},
		{name: "source opt-in absent", change: func(source *infernexv1alpha1.InferNexService, _ *infernexv1alpha1.InferNexServiceConfig, _ *Request) {
			delete(source.Annotations, autoRecoveryAnnotation)
		}},
		{name: "source opt-in revoked", change: func(source *infernexv1alpha1.InferNexService, _ *infernexv1alpha1.InferNexServiceConfig, _ *Request) {
			source.Annotations[autoRecoveryAnnotation] = "false"
		}},
		{name: "source profile changed", change: func(source *infernexv1alpha1.InferNexService, _ *infernexv1alpha1.InferNexServiceConfig, _ *Request) {
			source.Annotations[recoveryProfileAnnotation] = "different-profile"
		}},
		{name: "source recovery name changed", change: func(source *infernexv1alpha1.InferNexService, _ *infernexv1alpha1.InferNexServiceConfig, _ *Request) {
			source.Annotations[recoveryNameAnnotation] = "different-target"
		}},
		{name: "request recovery name not authorized", change: func(_ *infernexv1alpha1.InferNexService, _ *infernexv1alpha1.InferNexServiceConfig, request *Request) {
			request.Name = "different-target"
		}},
		{name: "profile approval revoked", change: func(_ *infernexv1alpha1.InferNexService, profile *infernexv1alpha1.InferNexServiceConfig, _ *Request) {
			profile.Labels[ApprovedProfileLabel] = "false"
		}},
		{name: "profile terminating", change: func(_ *infernexv1alpha1.InferNexService, profile *infernexv1alpha1.InferNexServiceConfig, _ *Request) {
			now := metav1.Now()
			profile.DeletionTimestamp = &now
			profile.Finalizers = []string{"test.infernex.io/retain"}
		}},
		{name: "source replacement", change: func(_ *infernexv1alpha1.InferNexService, _ *infernexv1alpha1.InferNexServiceConfig, request *Request) {
			request.ExpectedSource = &SourceIdentity{UID: "previous-source-uid", Generation: 1}
		}},
		{name: "source generation changed", change: func(source *infernexv1alpha1.InferNexService, _ *infernexv1alpha1.InferNexServiceConfig, request *Request) {
			request.ExpectedSource = &SourceIdentity{UID: source.UID, Generation: 1}
			source.Generation, source.Status.ObservedGeneration = 2, 2
		}},
		{name: "source reconciliation pending", change: func(source *infernexv1alpha1.InferNexService, _ *infernexv1alpha1.InferNexServiceConfig, _ *Request) {
			source.Generation = 2
		}},
		{name: "expected identity missing UID", change: func(_ *infernexv1alpha1.InferNexService, _ *infernexv1alpha1.InferNexServiceConfig, request *Request) {
			request.ExpectedSource = &SourceIdentity{Generation: 1}
		}},
		{name: "expected identity missing generation", change: func(source *infernexv1alpha1.InferNexService, _ *infernexv1alpha1.InferNexServiceConfig, request *Request) {
			request.ExpectedSource = &SourceIdentity{UID: source.UID}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := infernexv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			source, profile := recoverySource("models", "qwen", "approved"), approvedProfile()
			request := Request{Namespace: "models", SourceName: "qwen", Profile: "approved"}
			if test.change != nil {
				test.change(source, profile, &request)
			}
			objects := []client.Object{profile}
			if !test.omitSource {
				objects = append(objects, source)
			}
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			store := changesafety.NewMemoryStore()
			r, err := New(kubeClient, "templates", WithStore(store))
			if err != nil {
				t.Fatal(err)
			}
			_, err = r.EnsureRecovery(context.Background(), request)
			if !errors.Is(err, ErrRecoveryPrecondition) {
				t.Fatalf("expected precondition rejection, got %v", err)
			}
			name := request.Name
			if name == "" {
				name = defaultRecoveryName(source.Name)
			}
			if err := kubeClient.Get(context.Background(), types.NamespacedName{Namespace: "models", Name: name}, &infernexv1alpha1.InferNexService{}); !apierrors.IsNotFound(err) {
				t.Fatalf("unauthorized recovery target exists: %v", err)
			}
			if pending, err := store.Pending(); err != nil || len(pending) != 0 {
				t.Fatalf("rejected request left a pending change: %#v, %v", pending, err)
			}
		})
	}
}

type afterPlanStore struct {
	changesafety.Store
	afterPlan func(changesafety.ChangeRecord)
}

func (s afterPlanStore) Append(record changesafety.ChangeRecord) error {
	if err := s.Store.Append(record); err != nil {
		return err
	}
	if record.Status == changesafety.StatusPlanned {
		s.afterPlan(record)
	}
	return nil
}

func TestRecoveryRechecksPreconditionsAfterPersistingPlan(t *testing.T) {
	for _, mutation := range []string{"source replacement", "generation changed", "opt-in revoked", "profile changed", "name changed", "source deleted", "profile approval revoked", "profile terminating"} {
		for _, withExpectation := range []bool{false, true} {
			name := mutation + "/direct"
			if withExpectation {
				name = mutation + "/observed"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				scheme := runtime.NewScheme()
				if err := infernexv1alpha1.AddToScheme(scheme); err != nil {
					t.Fatal(err)
				}
				source, profile := recoverySource("models", "qwen", "approved"), approvedProfile()
				if mutation == "profile terminating" {
					profile.Finalizers = []string{"test.infernex.io/retain"}
				}
				kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(source, profile).Build()
				store := changesafety.NewMemoryStore()
				changeID := ""
				hooked := afterPlanStore{Store: store, afterPlan: func(record changesafety.ChangeRecord) {
					changeID = record.ID
					current := &infernexv1alpha1.InferNexService{}
					if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(source), current); err != nil {
						t.Fatal(err)
					}
					switch mutation {
					case "source replacement", "source deleted":
						if err := kubeClient.Delete(ctx, current); err != nil {
							t.Fatal(err)
						}
						if mutation == "source replacement" {
							current.UID, current.ResourceVersion = "replacement-uid", ""
							if err := kubeClient.Create(ctx, current); err != nil {
								t.Fatal(err)
							}
						}
					case "profile approval revoked", "profile terminating":
						currentProfile := &infernexv1alpha1.InferNexServiceConfig{}
						if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(profile), currentProfile); err != nil {
							t.Fatal(err)
						}
						if mutation == "profile terminating" {
							if err := kubeClient.Delete(ctx, currentProfile); err != nil {
								t.Fatal(err)
							}
						} else {
							currentProfile.Labels[ApprovedProfileLabel] = "false"
							if err := kubeClient.Update(ctx, currentProfile); err != nil {
								t.Fatal(err)
							}
						}
					default:
						switch mutation {
						case "generation changed":
							current.Generation, current.Status.ObservedGeneration = 2, 2
						case "opt-in revoked":
							current.Annotations[autoRecoveryAnnotation] = "false"
						case "profile changed":
							current.Annotations[recoveryProfileAnnotation] = "different-profile"
						case "name changed":
							current.Annotations[recoveryNameAnnotation] = "different-target"
						}
						if err := kubeClient.Update(ctx, current); err != nil {
							t.Fatal(err)
						}
					}
				}}
				r, err := New(kubeClient, "templates", WithStore(hooked))
				if err != nil {
					t.Fatal(err)
				}
				request := Request{Namespace: "models", SourceName: source.Name, Profile: profile.Name}
				if withExpectation {
					request.ExpectedSource = &SourceIdentity{UID: source.UID, Generation: source.Generation}
				}
				_, err = r.EnsureRecovery(ctx, request)
				if !errors.Is(err, ErrRecoveryPrecondition) || changeID == "" {
					t.Fatalf("expected rejection after planned change, id=%q err=%v", changeID, err)
				}
				record, err := store.Latest(changeID)
				if err != nil || record.Status != changesafety.StatusApplyFailed {
					t.Fatalf("planned change not closed: %#v, %v", record, err)
				}
				if pending, err := store.Pending(); err != nil || len(pending) != 0 {
					t.Fatalf("rejected recovery remains pending: %#v, %v", pending, err)
				}
				if err := kubeClient.Get(ctx, types.NamespacedName{Namespace: "models", Name: "qwen-recovery"}, &infernexv1alpha1.InferNexService{}); !apierrors.IsNotFound(err) {
					t.Fatalf("recovery created after preconditions changed: %v", err)
				}
			})
		}
	}
}

func TestExistingRecoveryStillRequiresSourceOptIn(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := infernexv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	source, profile := recoverySource("models", "qwen", "approved"), approvedProfile()
	source.Annotations[autoRecoveryAnnotation] = "false"
	target := recoveryService("models", "qwen-recovery", source.Name, profile.Name)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(source, profile, target).Build()
	r, err := New(kubeClient, "templates")
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.EnsureRecovery(ctx, Request{Namespace: "models", SourceName: source.Name, Profile: profile.Name})
	if !errors.Is(err, ErrRecoveryPrecondition) || result.Action == "unchanged" {
		t.Fatalf("existing recovery bypassed current opt-in: result=%#v err=%v", result, err)
	}
	if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(target), &infernexv1alpha1.InferNexService{}); err != nil {
		t.Fatalf("existing recovery was removed: %v", err)
	}
}

func TestRecoveryAcceptsMatchingObservedSource(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := infernexv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	source, profile := recoverySource("models", "qwen", "approved"), approvedProfile()
	source.Annotations[autoRecoveryAnnotation] = " TRUE "
	source.Annotations[recoveryNameAnnotation] = " qwen-custom "
	source.Annotations[recoveryProfileAnnotation] = " approved "
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(source, profile).Build()
	r, err := New(kubeClient, "templates")
	if err != nil {
		t.Fatal(err)
	}
	request := Request{
		Namespace: " models ", SourceName: source.Name, Profile: " approved ", Name: " qwen-custom ",
		ExpectedSource: &SourceIdentity{UID: source.UID, Generation: source.Generation},
	}
	first, err := r.EnsureRecovery(ctx, request)
	if err != nil || first.Action != "created" || first.Name != "qwen-custom" {
		t.Fatalf("matching observed source rejected: result=%#v err=%v", first, err)
	}
	second, err := r.EnsureRecovery(ctx, request)
	if err != nil || second.Action != "unchanged" || second.ChangeID != first.ChangeID {
		t.Fatalf("matching observed recovery not idempotent: result=%#v err=%v", second, err)
	}
}
