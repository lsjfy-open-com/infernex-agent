/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 * You can use this software according to the terms and conditions of the Mulan PSL v2.
 */

package kubeops

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"
)

func TestLiveWorkloadYAMLProjectsSafeCurrentFields(t *testing.T) {
	replicas := int32(2)
	pod := &corev1.PodSpec{
		Containers: []corev1.Container{{
			Name: "model", Image: "registry.example/qwen:v1", Command: []string{"--password=private-command"},
			Env: []corev1.EnvVar{{Name: "API_KEY", Value: "private-env"}},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("4Gi")},
			},
		}},
		Volumes: []corev1.Volume{{Name: "private-volume", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "private-secret"}}}},
	}
	text := liveWorkloadYAML("Deployment", "models", "qwen", &replicas, map[string]*corev1.PodSpec{"": pod})
	for _, forbidden := range []string{"private-command", "private-env", "private-volume", "private-secret", "API_KEY", "env:", "volumes:"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("unsafe field %q in YAML: %s", forbidden, text)
		}
	}
	for _, expected := range []string{"kind: Deployment", "replicas: 2", "image: registry.example/qwen:v1", "cpu: 500m", "memory: 4Gi"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing %q in YAML: %s", expected, text)
		}
	}
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("invalid YAML: %v", err)
	}
	if got := PublicWorkloadImages([]string{"https://user:password@example.com/model:1"})[0]; got != "<redacted image reference>" {
		t.Fatalf("credential-bearing image leaked: %s", got)
	}
}
