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

// Package deploymentplan estimates whether a caller-supplied, homogeneous
// workload profile fits on the current Kubernetes nodes. It is deliberately
// read-only: a Plan is neither a reservation nor an authorization to deploy.
package deploymentplan

import corev1 "k8s.io/api/core/v1"

const (
	ProfileVersion = "infernex.openfuyao.io/v1alpha1"
	PlanVersion    = "infernex.openfuyao.io/v1alpha1"

	StatusSchedulable  = "resource-fit"
	StatusInsufficient = "insufficient"
	StatusBlocked      = "blocked"
)

// Profile describes one replica. CPU and memory are Kubernetes Quantity
// strings. ExtendedResources are integer device counts per replica.
//
// PersistentVolumeClaims, Affinity, ResourceClaims and PodResources are
// represented so callers get an explicit blocked result instead of silently
// losing constraints that this first planner slice cannot model.
type Profile struct {
	Version                string                       `json:"version"`
	Image                  string                       `json:"image"`
	CPU                    string                       `json:"cpu"`
	Memory                 string                       `json:"memory"`
	ExtendedResources      map[string]int64             `json:"extendedResources,omitempty"`
	NodeSelector           map[string]string            `json:"nodeSelector,omitempty"`
	Tolerations            []corev1.Toleration          `json:"tolerations,omitempty"`
	PersistentVolumeClaims []string                     `json:"persistentVolumeClaims,omitempty"`
	Affinity               *corev1.Affinity             `json:"affinity,omitempty"`
	ResourceClaims         []corev1.PodResourceClaim    `json:"resourceClaims,omitempty"`
	PodResources           *corev1.ResourceRequirements `json:"podResources,omitempty"`
}

type Request struct {
	Namespace string  `json:"namespace"`
	Replicas  int     `json:"replicas"`
	Profile   Profile `json:"profile"`
}

type Placement struct {
	NodeName           string            `json:"nodeName"`
	NodeUID            string            `json:"nodeUid"`
	Replicas           int               `json:"replicas"`
	RemainingResources map[string]string `json:"remainingResources"`
}

type ResourceEstimate struct {
	PerReplica     map[string]string `json:"perReplica"`
	TotalRequested map[string]string `json:"totalRequested"`
}

type Plan struct {
	Version             string           `json:"version"`
	Status              string           `json:"status"`
	ProfileHash         string           `json:"profileHash"`
	SnapshotHash        string           `json:"snapshotHash,omitempty"`
	PlanHash            string           `json:"planHash"`
	Namespace           string           `json:"namespace"`
	RequestedReplicas   int              `json:"requestedReplicas"`
	PlaceableReplicas   int              `json:"placeableReplicas"`
	ResourceEstimate    ResourceEstimate `json:"resourceEstimate"`
	Placements          []Placement      `json:"placements"`
	Reasons             []string         `json:"reasons,omitempty"`
	Warnings            []string         `json:"warnings"`
	TrafficVerified     bool             `json:"trafficVerified"`
	PerformanceVerified bool             `json:"performanceVerified"`
	ReservationCreated  bool             `json:"reservationCreated"`
}
