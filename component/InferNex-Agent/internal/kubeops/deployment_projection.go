/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 * You can use this software according to the terms and conditions of the Mulan PSL v2.
 * You may obtain a copy of Mulan PSL v2 at:
 *          http://license.coscl.org.cn/MulanPSL2
 * THIS SOFTWARE IS PROVIDED ON AN "AS IS" BASIS, WITHOUT WARRANTIES OF ANY KIND.
 */

package kubeops

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

// liveWorkloadYAML is a deliberately small projection of the current API object.
// Workload templates can contain credentials in env, volumes, annotations and
// command arguments, so the public dashboard must never serialize the full spec.
func liveWorkloadYAML(kind, namespace, name string, replicas *int32, pods map[string]*corev1.PodSpec) string {
	type container struct {
		Name      string         `json:"name"`
		Image     string         `json:"image"`
		Resources map[string]any `json:"resources,omitempty"`
	}
	type podTemplate struct {
		Containers     []container `json:"containers"`
		InitContainers []container `json:"initContainers,omitempty"`
	}
	projectContainers := func(items []corev1.Container) []container {
		out := make([]container, 0, len(items))
		for _, item := range items {
			entry := container{Name: item.Name, Image: publicImage(item.Image)}
			resources := map[string]any{}
			for _, source := range []struct {
				key   string
				items corev1.ResourceList
			}{{"requests", item.Resources.Requests}, {"limits", item.Resources.Limits}} {
				if len(source.items) == 0 {
					continue
				}
				values := map[string]string{}
				for key, quantity := range source.items {
					values[string(key)] = quantity.String()
				}
				resources[source.key] = values
			}
			if len(resources) > 0 {
				entry.Resources = resources
			}
			out = append(out, entry)
		}
		return out
	}
	spec := map[string]any{}
	if replicas != nil {
		spec["replicas"] = *replicas
	}
	for role, pod := range pods {
		if pod == nil {
			continue
		}
		if len(pod.Containers)+len(pod.InitContainers) > 20 {
			return ""
		}
		template := podTemplate{Containers: projectContainers(pod.Containers), InitContainers: projectContainers(pod.InitContainers)}
		if role == "" {
			spec["template"] = map[string]any{"spec": template}
		} else {
			group, _ := spec["leaderWorkerTemplate"].(map[string]any)
			if group == nil {
				group = map[string]any{}
				spec["leaderWorkerTemplate"] = group
			}
			group[role] = map[string]any{"spec": template}
		}
	}
	projected := map[string]any{
		"apiVersion": "apps/v1",
		"kind":       kind,
		"metadata":   map[string]string{"namespace": namespace, "name": name},
		"spec":       spec,
	}
	if kind == "LeaderWorkerSet" {
		projected["apiVersion"] = "leaderworkerset.x-k8s.io/v1"
	}
	contents, err := yaml.Marshal(projected)
	if err != nil || len(contents) > 32*1024 {
		return ""
	}
	return string(contents)
}

func publicImage(image string) string {
	// URL parameters and @ user-info can carry credentials. Exclude the whole
	// reference instead of guessing whether @ denotes a digest or user-info.
	if strings.Contains(image, "://") || strings.ContainsAny(image, "?#") ||
		strings.Contains(image, "@") ||
		secretPattern.MatchString(image) {
		return "<redacted image reference>"
	}
	return sanitize(image, 512)
}

// PublicWorkloadImages applies the same image policy to summary badges.
func PublicWorkloadImages(images []string) []string {
	out := make([]string, 0, len(images))
	for _, image := range images {
		out = append(out, publicImage(image))
	}
	return out
}
