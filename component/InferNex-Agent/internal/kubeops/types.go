/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 * You can use this software according to the terms and conditions of the Mulan PSL v2.
 * You may obtain a copy of the License at:
 *          http://license.coscl.org.cn/MulanPSL2
 * THIS SOFTWARE IS PROVIDED ON AN "AS IS" BASIS, WITHOUT WARRANTIES OF ANY KIND.
 */

package kubeops

import "context"

// Reader is the read-only Kubernetes/openFuyao boundary exposed to the agent.
// It intentionally excludes arbitrary object reads, Secrets, exec and mutations.
type Reader interface {
	DetectEnvironment(context.Context) (Environment, error)
	ClusterOverview(context.Context) (ClusterOverview, error)
	ListWorkloads(context.Context, WorkloadRequest) (WorkloadInventory, error)
	GetEvents(context.Context, EventRequest) (EventList, error)
	GetPodLogs(context.Context, PodLogRequest) (PodLogResult, error)
	ListHelmReleases(context.Context, HelmReleaseRequest) (HelmReleaseList, error)
}

type Environment struct {
	Platform        string          `json:"platform"`
	APIServer       string          `json:"apiServer,omitempty"`
	ClusterRoles    []string        `json:"clusterRoles"`
	Kubernetes      string          `json:"kubernetesVersion,omitempty"`
	Namespaces      []string        `json:"detectedNamespaces"`
	Capabilities    map[string]bool `json:"capabilities"`
	Evidence        []string        `json:"evidence"`
	Recommendations []string        `json:"recommendations"`
	Warnings        []string        `json:"warnings"`
}

type ClusterOverview struct {
	APIServer         string        `json:"apiServer,omitempty"`
	KubernetesVersion string        `json:"kubernetesVersion,omitempty"`
	Nodes             []NodeSummary `json:"nodes"`
	NamespaceCount    int           `json:"namespaceCount"`
	PodCount          int           `json:"podCount"`
	ReadyPodCount     int           `json:"readyPodCount"`
	PendingPodCount   int           `json:"pendingPodCount"`
	FailedPodCount    int           `json:"failedPodCount"`
	Warnings          []string      `json:"warnings"`
}

type NodeSummary struct {
	Name         string            `json:"name"`
	Ready        bool              `json:"ready"`
	OS           string            `json:"os,omitempty"`
	Architecture string            `json:"architecture,omitempty"`
	Kubelet      string            `json:"kubeletVersion,omitempty"`
	Capacity     map[string]string `json:"acceleratorCapacity,omitempty"`
	Allocatable  map[string]string `json:"acceleratorAllocatable,omitempty"`
	Taints       []string          `json:"taints,omitempty"`
}

type WorkloadRequest struct {
	Namespace     string `json:"namespace,omitempty"`
	LabelSelector string `json:"labelSelector,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

type WorkloadInventory struct {
	Namespace     string            `json:"namespace,omitempty"`
	LabelSelector string            `json:"labelSelector,omitempty"`
	Total         int               `json:"total"`
	Truncated     bool              `json:"truncated"`
	Workloads     []WorkloadSummary `json:"workloads"`
	Pods          []PodSummary      `json:"pods"`
	Services      []ServiceSummary  `json:"services"`
	Warnings      []string          `json:"warnings"`
}

type WorkloadSummary struct {
	Kind        string            `json:"kind"`
	Namespace   string            `json:"namespace"`
	Name        string            `json:"name"`
	Desired     int32             `json:"desired"`
	Ready       int32             `json:"ready"`
	Available   int32             `json:"available,omitempty"`
	Images      []string          `json:"images,omitempty"`
	Selector    string            `json:"selector,omitempty"`
	HelmRelease string            `json:"helmRelease,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

type PodSummary struct {
	Namespace   string   `json:"namespace"`
	Name        string   `json:"name"`
	Node        string   `json:"node,omitempty"`
	Phase       string   `json:"phase"`
	Ready       bool     `json:"ready"`
	Restarts    int32    `json:"restarts"`
	Reason      string   `json:"reason,omitempty"`
	Owner       string   `json:"owner,omitempty"`
	Containers  []string `json:"containers,omitempty"`
	HelmRelease string   `json:"helmRelease,omitempty"`
}

type ServiceSummary struct {
	Namespace   string            `json:"namespace"`
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	ClusterIP   string            `json:"clusterIP,omitempty"`
	Ports       []string          `json:"ports,omitempty"`
	Selector    map[string]string `json:"selector,omitempty"`
	HelmRelease string            `json:"helmRelease,omitempty"`
}

type EventRequest struct {
	Namespace    string `json:"namespace,omitempty"`
	Kind         string `json:"kind,omitempty"`
	Name         string `json:"name,omitempty"`
	SinceMinutes int    `json:"sinceMinutes,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}

type EventList struct {
	Namespace    string         `json:"namespace,omitempty"`
	SinceMinutes int            `json:"sinceMinutes"`
	Total        int            `json:"total"`
	Truncated    bool           `json:"truncated"`
	Events       []EventSummary `json:"events"`
}

type EventSummary struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Object    string `json:"object"`
	Source    string `json:"source,omitempty"`
	Count     int32  `json:"count,omitempty"`
	Message   string `json:"message,omitempty"`
}

type PodLogRequest struct {
	Namespace    string `json:"namespace"`
	Pod          string `json:"pod"`
	Container    string `json:"container,omitempty"`
	Previous     bool   `json:"previous,omitempty"`
	SinceMinutes int    `json:"sinceMinutes,omitempty"`
	TailLines    int64  `json:"tailLines,omitempty"`
}

type PodLogResult struct {
	Namespace    string      `json:"namespace"`
	Pod          string      `json:"pod"`
	Previous     bool        `json:"previous"`
	SinceMinutes int         `json:"sinceMinutes"`
	Streams      []LogStream `json:"streams"`
	Warnings     []string    `json:"warnings"`
}

type LogStream struct {
	Container string `json:"container"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

type HelmReleaseRequest struct {
	Namespace string `json:"namespace,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type HelmReleaseList struct {
	Namespace string               `json:"namespace,omitempty"`
	Total     int                  `json:"total"`
	Truncated bool                 `json:"truncated"`
	Releases  []HelmReleaseSummary `json:"releases"`
	Warnings  []string             `json:"warnings"`
}

type HelmReleaseSummary struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Revision  int    `json:"revision"`
	Status    string `json:"status,omitempty"`
	Storage   string `json:"storage"`
}
