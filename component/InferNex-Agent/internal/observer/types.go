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

package observer

import "context"

// Observer is the stable InferNex domain boundary exposed to agent runtimes.
// It intentionally does not expose generic Kubernetes reads.
type Observer interface {
	ListServices(context.Context, string) (ServiceList, error)
	InspectService(context.Context, string, string) (ServiceDetail, error)
	GetTopology(context.Context, string, string) (Topology, error)
	GetEvents(context.Context, string, string, int, int) (EventEvidence, error)
}

type ServiceList struct {
	Namespace         string           `json:"namespace"`
	TotalServices     int              `json:"totalServices"`
	ServicesTruncated bool             `json:"servicesTruncated"`
	Services          []ServiceSummary `json:"services"`
}

type ServiceSummary struct {
	Namespace          string             `json:"namespace"`
	Name               string             `json:"name"`
	Mode               string             `json:"mode,omitempty"`
	Ready              bool               `json:"ready"`
	Generation         int64              `json:"generation"`
	ObservedGeneration int64              `json:"observedGeneration"`
	Model              *ModelSummary      `json:"model,omitempty"`
	Components         []ComponentSummary `json:"components,omitempty"`
	Conditions         []ConditionSummary `json:"conditions,omitempty"`
}

type ServiceDetail struct {
	Service  ServiceSummary `json:"service"`
	Source   *SourceSummary `json:"source,omitempty"`
	BaseRefs []string       `json:"baseRefs,omitempty"`
}

type ModelSummary struct {
	Name string `json:"name,omitempty"`
	URI  string `json:"uri,omitempty"`
}

type SourceSummary struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
}

type ComponentSummary struct {
	Name    string `json:"name"`
	Ready   bool   `json:"ready"`
	Message string `json:"message,omitempty"`
}

type ConditionSummary struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason,omitempty"`
	Message            string `json:"message,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
	LastTransitionTime string `json:"lastTransitionTime,omitempty"`
}

type Topology struct {
	Service       ServiceSummary    `json:"service"`
	Workloads     []WorkloadSummary `json:"workloads"`
	TotalPods     int               `json:"totalPods"`
	PodsTruncated bool              `json:"podsTruncated"`
	Pods          []PodSummary      `json:"pods"`
}

type WorkloadSummary struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Component string `json:"component,omitempty"`
	Desired   int32  `json:"desired"`
	Ready     int32  `json:"ready"`
	GroupSize *int32 `json:"groupSize,omitempty"`
}

type PodSummary struct {
	Name      string `json:"name"`
	Component string `json:"component,omitempty"`
	Node      string `json:"node,omitempty"`
	Phase     string `json:"phase"`
	Ready     bool   `json:"ready"`
	Restarts  int32  `json:"restarts"`
	Reason    string `json:"reason,omitempty"`
}

type EventEvidence struct {
	Service         ServiceReference `json:"service"`
	SinceMinutes    int              `json:"sinceMinutes"`
	TotalEvents     int              `json:"totalEvents"`
	EventsTruncated bool             `json:"eventsTruncated"`
	Events          []EventSummary   `json:"events"`
}

type ServiceReference struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type EventSummary struct {
	Timestamp string `json:"timestamp,omitempty"`
	Type      string `json:"type,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Action    string `json:"action,omitempty"`
	Note      string `json:"note,omitempty"`
	Count     int32  `json:"count"`
	Reporter  string `json:"reporter,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Name      string `json:"name,omitempty"`
	Component string `json:"component,omitempty"`
}
