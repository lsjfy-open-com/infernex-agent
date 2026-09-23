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

func TestPublicImageRetainsOnlyStrictDigestReferences(t *testing.T) {
	digest := strings.Repeat("a", 64)
	cases := []struct {
		name  string
		image string
		keep  bool
	}{
		{"repository digest", "registry.example:5000/team/qwen@sha256:" + digest, true},
		{"short repository digest", "qwen@sha256:" + digest, true},
		{"tag and digest ambiguous", "qwen:v1@sha256:" + digest, false},
		{"userinfo form", "https://user:password@registry.example/team/qwen@sha256:" + digest, false},
		{"credential before digest", "user:password@sha256:" + digest, false},
		{"extra at sign", "registry.example/user@password/qwen@sha256:" + digest, false},
		{"short digest", "registry.example/team/qwen@sha256:abcd", false},
		{"invalid digest", "registry.example/team/qwen@sha256:" + strings.Repeat("g", 64), false},
		{"invalid repository", "registry.example/../qwen@sha256:" + digest, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := publicImage(tc.image)
			if tc.keep && got != tc.image {
				t.Fatalf("valid digest reference was hidden: %s", got)
			}
			if !tc.keep && got != "<redacted image reference>" {
				t.Fatalf("unsafe reference was exposed: %s", got)
			}
		})
	}
	pod := &corev1.PodSpec{Containers: []corev1.Container{{Name: "model", Image: cases[0].image}}}
	if got := liveWorkloadYAML("Deployment", "models", "qwen", nil, map[string]*corev1.PodSpec{"": pod}); !strings.Contains(got, cases[0].image) {
		t.Fatalf("digest missing from YAML projection: %s", got)
	}
}
