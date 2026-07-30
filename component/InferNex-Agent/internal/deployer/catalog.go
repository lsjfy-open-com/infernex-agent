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

package deployer

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	infernexv1alpha1 "gitcode.com/openFuyao/InferNex/api/v1alpha1"
)

const (
	modelName = "SmolLM2-135M-Instruct"
	modelURI  = "hf://bartowski/SmolLM2-135M-Instruct-GGUF@09816acd5d99df7be770d85ea30822623dab342c/SmolLM2-135M-Instruct-Q4_K_M.gguf"
	modelURL  = "https://huggingface.co/bartowski/SmolLM2-135M-Instruct-GGUF/resolve/09816acd5d99df7be770d85ea30822623dab342c/SmolLM2-135M-Instruct-Q4_K_M.gguf?download=true"
	modelSHA  = "2e8040ceae7815abe0dcb3540b9995eaa1fa0d2ca9e797d0a635ae4433c68c2d"

	serverImage = "ghcr.io/ggml-org/llama.cpp:server-b9445@sha256:8dd148c53936b6e8b0e75309841e66eab13adc50b004a5e86ab1fec477c17d8e"
)

func tinyModelService(namespace string, name string) *infernexv1alpha1.InferNexService {
	replicas := int32(1)
	disabled := false
	runAsUser := int64(65532)
	modelPath := "/models/model.gguf"
	downloadScript := "curl --fail --location --retry 5 --retry-all-errors " +
		"--output " + modelPath + " '" + modelURL + "'\n" +
		"echo '" + modelSHA + "  " + modelPath + "' | sha256sum --check --strict"

	lockedDown := corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		ReadOnlyRootFilesystem:   ptr.To(true),
		RunAsNonRoot:             ptr.To(true),
		RunAsUser:                &runAsUser,
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}

	return &infernexv1alpha1.InferNexService{
		TypeMeta: metav1.TypeMeta{
			APIVersion: infernexv1alpha1.GroupVersion.String(),
			Kind:       "InferNexService",
		},
		ObjectMeta: objectMeta(namespace, name),
		Spec: infernexv1alpha1.InferNexServiceSpec{
			Model: &infernexv1alpha1.LLMModelSpec{
				Name: modelName,
				URI:  modelURI,
			},
			Engine: &infernexv1alpha1.InferenceEngineSpec{
				InferenceEngineWorkloadSpec: infernexv1alpha1.InferenceEngineWorkloadSpec{
					Replicas: &replicas,
					Template: &corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							AutomountServiceAccountToken: ptr.To(false),
							SecurityContext: &corev1.PodSecurityContext{
								RunAsNonRoot: ptr.To(true),
								RunAsUser:    &runAsUser,
								FSGroup:      &runAsUser,
								SeccompProfile: &corev1.SeccompProfile{
									Type: corev1.SeccompProfileTypeRuntimeDefault,
								},
							},
							InitContainers: []corev1.Container{{
								Name:            "download-model",
								Image:           serverImage,
								ImagePullPolicy: corev1.PullIfNotPresent,
								Command:         []string{"/bin/sh", "-ec"},
								Args:            []string{downloadScript},
								SecurityContext: lockedDown.DeepCopy(),
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("25m"),
										corev1.ResourceMemory: resource.MustParse("16Mi"),
									},
									Limits: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("250m"),
										corev1.ResourceMemory: resource.MustParse("64Mi"),
									},
								},
								VolumeMounts: []corev1.VolumeMount{{
									Name:      "model",
									MountPath: "/models",
								}},
							}},
							Containers: []corev1.Container{{
								Name:            "llama-server",
								Image:           serverImage,
								ImagePullPolicy: corev1.PullIfNotPresent,
								Args: []string{
									"--model", modelPath,
									"--host", "0.0.0.0",
									"--port", "8080",
									"--ctx-size", "512",
									"--threads", "2",
									"--parallel", "1",
								},
								Ports: []corev1.ContainerPort{{
									Name:          "http",
									ContainerPort: 8080,
									Protocol:      corev1.ProtocolTCP,
								}},
								SecurityContext: lockedDown.DeepCopy(),
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("250m"),
										corev1.ResourceMemory: resource.MustParse("256Mi"),
									},
									Limits: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("2"),
										corev1.ResourceMemory: resource.MustParse("768Mi"),
									},
								},
								StartupProbe: &corev1.Probe{
									ProbeHandler: corev1.ProbeHandler{
										HTTPGet: &corev1.HTTPGetAction{
											Path: "/health",
											Port: intstr.FromString("http"),
										},
									},
									PeriodSeconds:    2,
									FailureThreshold: 90,
								},
								ReadinessProbe: &corev1.Probe{
									ProbeHandler: corev1.ProbeHandler{
										HTTPGet: &corev1.HTTPGetAction{
											Path: "/health",
											Port: intstr.FromString("http"),
										},
									},
									PeriodSeconds:    3,
									FailureThreshold: 3,
								},
								LivenessProbe: &corev1.Probe{
									ProbeHandler: corev1.ProbeHandler{
										HTTPGet: &corev1.HTTPGetAction{
											Path: "/health",
											Port: intstr.FromString("http"),
										},
									},
									PeriodSeconds:    10,
									FailureThreshold: 3,
								},
								VolumeMounts: []corev1.VolumeMount{{
									Name:      "model",
									MountPath: "/models",
									ReadOnly:  true,
								}},
							}},
							Volumes: []corev1.Volume{{
								Name: "model",
								VolumeSource: corev1.VolumeSource{
									EmptyDir: &corev1.EmptyDirVolumeSource{
										SizeLimit: resource.NewQuantity(
											300*1024*1024,
											resource.BinarySI,
										),
									},
								},
							}},
						},
					},
				},
			},
			IntelligentGatewayRouting: &infernexv1alpha1.IntelligentGatewayRoutingSpec{
				Router: &infernexv1alpha1.ComponentSpec{Enabled: &disabled},
			},
			Components: &infernexv1alpha1.InfernexComponentsSpec{
				CacheIndexer: &infernexv1alpha1.CacheIndexerComponentSpec{
					Enabled: &disabled,
				},
				Mooncake: &infernexv1alpha1.MooncakeComponentSpec{
					Enabled: &disabled,
				},
				PDOrchestrator: &infernexv1alpha1.PDOrchestratorComponentSpec{
					ElasticScaler:        &infernexv1alpha1.EnabledComponentSpec{Enabled: &disabled},
					Tidal:                &infernexv1alpha1.EnabledComponentSpec{Enabled: &disabled},
					ResourceScalingGroup: &infernexv1alpha1.EnabledComponentSpec{Enabled: &disabled},
				},
				EagleEye: &infernexv1alpha1.EagleEyeComponentSpec{
					HardwareMonitor:            &infernexv1alpha1.EnabledComponentSpec{Enabled: &disabled},
					HardwareDiagnosis:          &infernexv1alpha1.EnabledComponentSpec{Enabled: &disabled},
					NetworkPerformanceExporter: &infernexv1alpha1.EnabledComponentSpec{Enabled: &disabled},
				},
			},
		},
	}
}
