/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

// Package collectorrun executes versioned, bounded diagnostic profiles against
// automatically discovered Pod/container targets and retains their output as
// local evidence.
package collectorrun

import (
	"context"
	"fmt"
	"strings"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/diagnosticexec"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Target is a concrete container identity. UID prevents a replacement Pod
// from being confused with the process that produced earlier evidence.
type Target struct {
	Channel   string `json:"channel"`
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	UID       string `json:"uid"`
	Container string `json:"container"`
}

// Source resolves current targets and runs one compiled-in diagnostic profile.
type Source interface {
	ListTargets(context.Context, string, string, string, string) ([]Target, error)
	Collect(context.Context, Target, string, int) (diagnosticexec.Result, error)
}

type KubernetesSource struct {
	client kubernetes.Interface
	runner *diagnosticexec.Runner
}

func NewKubernetesSource(client kubernetes.Interface, runner *diagnosticexec.Runner) (*KubernetesSource, error) {
	if client == nil || runner == nil {
		return nil, fmt.Errorf("Kubernetes client and diagnostic runner are required")
	}
	return &KubernetesSource{client: client, runner: runner}, nil
}

func (s *KubernetesSource) ListTargets(ctx context.Context, channel, namespace, selector, requestedContainer string) ([]Target, error) {
	switch channel {
	case "local", "host-root":
		return []Target{{Channel: channel, Pod: "management-node", UID: "management-node", Container: channel}}, nil
	case "pod":
	default:
		return nil, fmt.Errorf("collector channel must be pod, local, or host-root")
	}
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(selector) == "" {
		return nil, fmt.Errorf("namespace and label selector are required")
	}
	pods, err := s.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 100})
	if err != nil {
		return nil, fmt.Errorf("list collector target Pods: %w", err)
	}
	result := []Target{}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil {
			continue
		}
		for _, container := range pod.Spec.Containers {
			if requestedContainer != "" && container.Name != requestedContainer {
				continue
			}
			result = append(result, Target{Channel: "pod", Namespace: namespace, Pod: pod.Name, UID: string(pod.UID), Container: container.Name})
		}
	}
	return result, nil
}

func (s *KubernetesSource) Collect(ctx context.Context, target Target, profile string, deviceID int) (diagnosticexec.Result, error) {
	return s.runner.Run(ctx, diagnosticexec.Request{
		Channel: target.Channel, Probe: profile, Namespace: target.Namespace, Pod: target.Pod,
		Container: target.Container, DeviceID: deviceID,
	})
}
