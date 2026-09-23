/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package plogcapture

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

const maxChunkBytes int64 = 256 * 1024

var defaultPlogRoots = []string{
	"/root/ascend/log",
	"/home/HwHiAiUser/ascend/log",
	"/var/log/ascend",
	"/var/log/npu",
}

type PodContainer struct {
	Namespace string
	Pod       string
	UID       string
	Container string
	Roots     []string
}

type TargetSnapshot struct {
	PodJSON     []byte
	CurrentLog  []byte
	PreviousLog []byte
	Errors      []string
}

type SnapshotSource interface {
	Snapshot(context.Context, PodContainer) (TargetSnapshot, error)
}

type Source interface {
	ListTargets(context.Context, string, string, string) ([]PodContainer, error)
	ListFiles(context.Context, PodContainer) ([]string, error)
	FileSize(context.Context, PodContainer, string) (int64, error)
	ReadChunk(context.Context, PodContainer, string, int64, int64) ([]byte, error)
}

type KubernetesSource struct {
	client     kubernetes.Interface
	restConfig *rest.Config
	roots      []string
}

func NewKubernetesSource(client kubernetes.Interface, config *rest.Config) (*KubernetesSource, error) {
	if client == nil || config == nil {
		return nil, fmt.Errorf("Kubernetes client and REST config are required")
	}
	return &KubernetesSource{client: client, restConfig: rest.CopyConfig(config), roots: append([]string(nil), defaultPlogRoots...)}, nil
}

func (s *KubernetesSource) ListTargets(ctx context.Context, namespace, selector, requestedContainer string) ([]PodContainer, error) {
	if strings.TrimSpace(namespace) == "" {
		return nil, fmt.Errorf("namespace is required")
	}
	pods, err := s.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 100})
	if err != nil {
		return nil, fmt.Errorf("list plog source Pods: %w", err)
	}
	result := []PodContainer{}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil {
			continue
		}
		for _, container := range pod.Spec.Containers {
			if requestedContainer != "" && container.Name != requestedContainer {
				continue
			}
			result = append(result, PodContainer{Namespace: namespace, Pod: pod.Name, UID: string(pod.UID), Container: container.Name, Roots: plogRootsForContainer(container)})
		}
	}
	return result, nil
}

func (s *KubernetesSource) ListFiles(ctx context.Context, target PodContainer) ([]string, error) {
	roots := target.Roots
	if len(roots) == 0 {
		roots = s.roots
	}
	seen := map[string]bool{}
	files := []string{}
	discoveryErrors := []error{}
	for _, root := range roots {
		stdout, stderr, err := s.exec(ctx, target, []string{"find", root, "-maxdepth", "6", "-type", "f", "-print0"}, 1024*1024)
		if err != nil {
			detail := strings.TrimSpace(string(stderr))
			if detail != "" {
				discoveryErrors = append(discoveryErrors, fmt.Errorf("find %s: %w: %s", root, err, detail))
			} else {
				discoveryErrors = append(discoveryErrors, fmt.Errorf("find %s: %w", root, err))
			}
			continue
		}
		for _, candidate := range bytes.Split(stdout, []byte{0}) {
			file := string(candidate)
			if file == "" || !withinPlogRoots(file, roots) || seen[file] {
				continue
			}
			seen[file] = true
			files = append(files, file)
			if len(files) >= 200 {
				return files, nil
			}
		}
	}
	if len(files) == 0 && len(discoveryErrors) > 0 {
		return nil, fmt.Errorf("container log discovery failed for every candidate root: %w", errors.Join(discoveryErrors...))
	}
	return files, nil
}

func (s *KubernetesSource) FileSize(ctx context.Context, target PodContainer, file string) (int64, error) {
	if !withinPlogRoots(file, targetRoots(target, s.roots)) {
		return 0, fmt.Errorf("plog path is outside fixed roots")
	}
	stdout, stderr, err := s.exec(ctx, target, []string{"stat", "-c", "%s", "--", file}, 1024)
	if err != nil {
		return 0, fmt.Errorf("stat plog file: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(stdout)), 10, 64)
	if err != nil || size < 0 {
		return 0, fmt.Errorf("parse plog file size")
	}
	return size, nil
}

func (s *KubernetesSource) ReadChunk(ctx context.Context, target PodContainer, file string, offset, limit int64) ([]byte, error) {
	if !withinPlogRoots(file, targetRoots(target, s.roots)) {
		return nil, fmt.Errorf("plog path is outside fixed roots")
	}
	if offset < 0 || limit < 1 || limit > maxChunkBytes {
		return nil, fmt.Errorf("invalid plog offset or chunk limit")
	}
	stdout, stderr, err := s.exec(ctx, target, []string{"dd", "if=" + file, "bs=1", "skip=" + strconv.FormatInt(offset, 10), "count=" + strconv.FormatInt(limit, 10), "status=none"}, int(limit))
	if err != nil {
		return nil, fmt.Errorf("read plog chunk: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return stdout, nil
}

func targetRoots(target PodContainer, fallback []string) []string {
	if len(target.Roots) > 0 {
		return target.Roots
	}
	return fallback
}

func plogRootsForContainer(container corev1.Container) []string {
	roots := append([]string(nil), defaultPlogRoots...)
	seen := map[string]bool{}
	for _, root := range roots {
		seen[root] = true
	}
	for _, mount := range container.VolumeMounts {
		candidate := path.Clean(mount.MountPath)
		lower := strings.ToLower(candidate)
		if !path.IsAbs(candidate) || candidate == "/" || (!strings.Contains(lower, "ascend") && !strings.Contains(lower, "plog") && !strings.Contains(lower, "npu")) || seen[candidate] {
			continue
		}
		seen[candidate] = true
		roots = append([]string{candidate}, roots...)
	}
	return roots
}

func (s *KubernetesSource) Snapshot(ctx context.Context, target PodContainer) (TargetSnapshot, error) {
	podObject, err := s.client.CoreV1().Pods(target.Namespace).Get(ctx, target.Pod, metav1.GetOptions{})
	if err != nil {
		return TargetSnapshot{}, fmt.Errorf("get Pod snapshot: %w", err)
	}
	result := TargetSnapshot{}
	result.PodJSON, err = json.MarshalIndent(podObject, "", "  ")
	if err != nil {
		return TargetSnapshot{}, fmt.Errorf("encode Pod snapshot: %w", err)
	}
	limit := int64(1024 * 1024)
	result.CurrentLog, err = s.client.CoreV1().Pods(target.Namespace).GetLogs(target.Pod, &corev1.PodLogOptions{Container: target.Container, Timestamps: true, LimitBytes: &limit}).DoRaw(ctx)
	if err != nil {
		result.Errors = append(result.Errors, "current container log: "+err.Error())
	}
	for _, status := range podObject.Status.ContainerStatuses {
		if status.Name == target.Container && status.RestartCount > 0 {
			result.PreviousLog, err = s.client.CoreV1().Pods(target.Namespace).GetLogs(target.Pod, &corev1.PodLogOptions{Container: target.Container, Previous: true, Timestamps: true, LimitBytes: &limit}).DoRaw(ctx)
			if err != nil {
				result.Errors = append(result.Errors, "previous container log: "+err.Error())
			}
			break
		}
	}
	return result, nil
}

func withinPlogRoots(file string, roots []string) bool {
	if strings.ContainsAny(file, "\x00\r\n") || !path.IsAbs(file) || path.Clean(file) != file {
		return false
	}
	for _, root := range roots {
		if file == root || strings.HasPrefix(file, root+"/") {
			return true
		}
	}
	return false
}

func (s *KubernetesSource) exec(ctx context.Context, target PodContainer, command []string, limit int) ([]byte, []byte, error) {
	if target.Namespace == "" || target.Pod == "" || target.Container == "" {
		return nil, nil, fmt.Errorf("complete Pod target is required")
	}
	execContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req := s.client.CoreV1().RESTClient().Post().Resource("pods").Name(target.Pod).Namespace(target.Namespace).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: target.Container, Command: command, Stdout: true, Stderr: true}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(s.restConfig, "POST", req.URL())
	if err != nil {
		return nil, nil, err
	}
	stdout, stderr := &captureBuffer{limit: limit}, &captureBuffer{limit: 16 * 1024}
	err = executor.StreamWithContext(execContext, remotecommand.StreamOptions{Stdout: stdout, Stderr: stderr})
	return stdout.Bytes(), stderr.Bytes(), err
}

type captureBuffer struct {
	bytes.Buffer
	limit int
}

func (b *captureBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return original, nil
}
