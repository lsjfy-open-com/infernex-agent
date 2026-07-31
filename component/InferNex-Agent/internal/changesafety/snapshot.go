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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infernexv1alpha1 "gitcode.com/openFuyao/InferNex/api/v1alpha1"
)

const (
	changeIDAnnotation = "agent.infernex.io/change-id"
	managedByLabel     = "app.kubernetes.io/managed-by"
	managedByAgent     = "infernex-agent"
)

type ResourceSnapshot struct {
	APIVersion      string          `json:"apiVersion"`
	Kind            string          `json:"kind"`
	Namespace       string          `json:"namespace"`
	Name            string          `json:"name"`
	UID             types.UID       `json:"uid,omitempty"`
	ResourceVersion string          `json:"resourceVersion,omitempty"`
	Object          json.RawMessage `json:"object"`
}

// ClusterSnapshot is a restorable source-of-truth baseline for the namespaces
// placed under Agent management. Derived Deployments, Pods, and Services are
// deliberately not restored: InferNex Bridge reconciles them from these CRs.
type ClusterSnapshot struct {
	APIVersion string             `json:"apiVersion"`
	Kind       string             `json:"kind"`
	ID         string             `json:"id"`
	Purpose    string             `json:"purpose"`
	CapturedAt time.Time          `json:"capturedAt"`
	Namespaces []string           `json:"namespaces"`
	Resources  []ResourceSnapshot `json:"resources"`
	SHA256     string             `json:"sha256"`
}

type RestoreResult struct {
	SnapshotID string   `json:"snapshotId"`
	Created    []string `json:"created,omitempty"`
	Deleted    []string `json:"deleted,omitempty"`
	Unchanged  []string `json:"unchanged,omitempty"`
	Skipped    []string `json:"skipped,omitempty"`
}

func Capture(
	ctx context.Context,
	kubeClient client.Client,
	namespaces []string,
	purpose string,
) (ClusterSnapshot, error) {
	namespaces = normalizedNamespaces(namespaces)
	if len(namespaces) == 0 {
		return ClusterSnapshot{}, fmt.Errorf("at least one snapshot namespace is required")
	}
	id, err := NewID()
	if err != nil {
		return ClusterSnapshot{}, err
	}
	snapshot := ClusterSnapshot{
		APIVersion: "agent.infernex.io/v1alpha1",
		Kind:       "InferNexClusterSnapshot",
		ID:         id,
		Purpose:    strings.TrimSpace(purpose),
		CapturedAt: time.Now().UTC(),
		Namespaces: namespaces,
		Resources:  make([]ResourceSnapshot, 0),
	}
	for _, namespace := range namespaces {
		services := &infernexv1alpha1.InferNexServiceList{}
		if err := kubeClient.List(ctx, services, client.InNamespace(namespace)); err != nil {
			return ClusterSnapshot{}, fmt.Errorf(
				"list InferNexService resources in %s: %w",
				namespace,
				err,
			)
		}
		for index := range services.Items {
			service := services.Items[index].DeepCopy()
			raw, err := json.Marshal(service)
			if err != nil {
				return ClusterSnapshot{}, fmt.Errorf(
					"encode InferNexService %s/%s: %w",
					namespace,
					service.Name,
					err,
				)
			}
			snapshot.Resources = append(snapshot.Resources, ResourceSnapshot{
				APIVersion:      infernexv1alpha1.GroupVersion.String(),
				Kind:            "InferNexService",
				Namespace:       namespace,
				Name:            service.Name,
				UID:             service.UID,
				ResourceVersion: service.ResourceVersion,
				Object:          raw,
			})
		}
	}
	sort.Slice(snapshot.Resources, func(left, right int) bool {
		if snapshot.Resources[left].Namespace != snapshot.Resources[right].Namespace {
			return snapshot.Resources[left].Namespace < snapshot.Resources[right].Namespace
		}
		return snapshot.Resources[left].Name < snapshot.Resources[right].Name
	})
	checksum, err := snapshotChecksum(snapshot)
	if err != nil {
		return ClusterSnapshot{}, err
	}
	snapshot.SHA256 = checksum
	return snapshot, nil
}

func WriteSnapshot(path string, snapshot ClusterSnapshot) error {
	if err := VerifySnapshot(snapshot); err != nil {
		return err
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create snapshot directory: %w", err)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		return fmt.Errorf("protect snapshot directory: %w", err)
	}
	contents, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cluster snapshot: %w", err)
	}
	contents = append(contents, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create cluster snapshot: %w", err)
	}
	if _, err = file.Write(contents); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return fmt.Errorf("write cluster snapshot: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close cluster snapshot: %w", closeErr)
	}
	return nil
}

func ReadSnapshot(path string) (ClusterSnapshot, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return ClusterSnapshot{}, fmt.Errorf("read cluster snapshot: %w", err)
	}
	var snapshot ClusterSnapshot
	if err := json.Unmarshal(contents, &snapshot); err != nil {
		return ClusterSnapshot{}, fmt.Errorf("decode cluster snapshot: %w", err)
	}
	if err := VerifySnapshot(snapshot); err != nil {
		return ClusterSnapshot{}, err
	}
	return snapshot, nil
}

func VerifySnapshot(snapshot ClusterSnapshot) error {
	if snapshot.APIVersion != "agent.infernex.io/v1alpha1" ||
		snapshot.Kind != "InferNexClusterSnapshot" {
		return fmt.Errorf("unsupported cluster snapshot type %s %s", snapshot.APIVersion, snapshot.Kind)
	}
	if !validID(snapshot.ID) {
		return fmt.Errorf("invalid snapshot id %q", snapshot.ID)
	}
	if snapshot.CapturedAt.IsZero() || len(snapshot.Namespaces) == 0 {
		return fmt.Errorf("cluster snapshot metadata is incomplete")
	}
	actual, err := snapshotChecksum(snapshot)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actual, snapshot.SHA256) {
		return fmt.Errorf("cluster snapshot checksum mismatch")
	}
	return nil
}

// Restore returns only Agent-managed source resources to this baseline. It
// never deletes resources lacking both Agent ownership and a change-id, so a
// cluster-wide backup cannot erase unrelated work performed after capture.
func Restore(
	ctx context.Context,
	kubeClient client.Client,
	snapshot ClusterSnapshot,
) (RestoreResult, error) {
	if err := VerifySnapshot(snapshot); err != nil {
		return RestoreResult{}, err
	}
	result := RestoreResult{SnapshotID: snapshot.ID}
	baseline := make(map[types.NamespacedName]ResourceSnapshot, len(snapshot.Resources))
	for _, resource := range snapshot.Resources {
		if resource.Kind != "InferNexService" ||
			resource.APIVersion != infernexv1alpha1.GroupVersion.String() {
			return result, fmt.Errorf(
				"unsupported snapshot resource %s %s",
				resource.APIVersion,
				resource.Kind,
			)
		}
		baseline[types.NamespacedName{
			Namespace: resource.Namespace,
			Name:      resource.Name,
		}] = resource
	}

	for _, namespace := range snapshot.Namespaces {
		current := &infernexv1alpha1.InferNexServiceList{}
		if err := kubeClient.List(ctx, current, client.InNamespace(namespace)); err != nil {
			return result, fmt.Errorf("list current InferNexService resources in %s: %w", namespace, err)
		}
		for index := range current.Items {
			service := &current.Items[index]
			key := types.NamespacedName{Namespace: namespace, Name: service.Name}
			if _, existed := baseline[key]; existed {
				continue
			}
			label := key.String()
			if service.Labels[managedByLabel] != managedByAgent ||
				strings.TrimSpace(service.Annotations[changeIDAnnotation]) == "" {
				result.Skipped = append(result.Skipped, label)
				continue
			}
			if err := kubeClient.Delete(ctx, service); err != nil && !apierrors.IsNotFound(err) {
				return result, fmt.Errorf("delete post-snapshot InferNexService %s: %w", key, err)
			}
			result.Deleted = append(result.Deleted, label)
		}
	}

	for key, resource := range baseline {
		current := &infernexv1alpha1.InferNexService{}
		if err := kubeClient.Get(ctx, key, current); err == nil {
			var original infernexv1alpha1.InferNexService
			if err := json.Unmarshal(resource.Object, &original); err != nil {
				return result, fmt.Errorf("decode baseline InferNexService %s: %w", key, err)
			}
			if equality.Semantic.DeepEqual(current.Spec, original.Spec) {
				result.Unchanged = append(result.Unchanged, key.String())
			} else {
				// Updates are not currently an Agent capability. Refuse a blind
				// overwrite if another controller or operator changed the spec.
				result.Skipped = append(result.Skipped, key.String())
			}
			continue
		} else if !apierrors.IsNotFound(err) {
			return result, fmt.Errorf("get baseline InferNexService %s: %w", key, err)
		}

		var original infernexv1alpha1.InferNexService
		if err := json.Unmarshal(resource.Object, &original); err != nil {
			return result, fmt.Errorf("decode baseline InferNexService %s: %w", key, err)
		}
		if original.Labels[managedByLabel] != managedByAgent {
			result.Skipped = append(result.Skipped, key.String())
			continue
		}
		prepareForCreate(&original)
		if err := kubeClient.Create(ctx, &original); err != nil {
			return result, fmt.Errorf("recreate baseline InferNexService %s: %w", key, err)
		}
		result.Created = append(result.Created, key.String())
	}
	sort.Strings(result.Created)
	sort.Strings(result.Deleted)
	sort.Strings(result.Unchanged)
	sort.Strings(result.Skipped)
	return result, nil
}

func prepareForCreate(service *infernexv1alpha1.InferNexService) {
	service.ResourceVersion = ""
	service.UID = ""
	service.Generation = 0
	service.CreationTimestamp = metav1.Time{}
	service.DeletionTimestamp = nil
	service.DeletionGracePeriodSeconds = nil
	service.ManagedFields = nil
	service.Finalizers = nil
	service.Status = infernexv1alpha1.InferNexServiceStatus{}
}

func snapshotChecksum(snapshot ClusterSnapshot) (string, error) {
	snapshot.SHA256 = ""
	contents, err := json.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("encode cluster snapshot checksum input: %w", err)
	}
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:]), nil
}

func normalizedNamespaces(namespaces []string) []string {
	values := make(map[string]struct{}, len(namespaces))
	for _, namespace := range namespaces {
		namespace = strings.TrimSpace(namespace)
		if namespace != "" {
			values[namespace] = struct{}{}
		}
	}
	result := make([]string, 0, len(values))
	for namespace := range values {
		result = append(result, namespace)
	}
	sort.Strings(result)
	return result
}
