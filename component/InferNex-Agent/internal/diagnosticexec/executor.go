/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

// Package diagnosticexec provides bounded active-read probes. It deliberately
// does not expose an arbitrary command string to the model.
package diagnosticexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
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

const (
	defaultTimeout = 30 * time.Second
	maxOutputBytes = 256 * 1024
)

var sshAliasPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Request selects a fixed probe and a pre-authorized execution channel.
type Request struct {
	Channel   string `json:"channel"`
	Probe     string `json:"probe"`
	Namespace string `json:"namespace,omitempty"`
	Pod       string `json:"pod,omitempty"`
	Container string `json:"container,omitempty"`
	SSHTarget string `json:"sshTarget,omitempty"`
	DeviceID  int    `json:"deviceId,omitempty"`
}

// Result is bounded evidence from an active-read diagnostic probe.
type Result struct {
	Status      string `json:"status"`
	Channel     string `json:"channel"`
	Probe       string `json:"probe"`
	Target      string `json:"target"`
	ActionClass string `json:"actionClass"`
	Command     string `json:"command"`
	Output      string `json:"output,omitempty"`
	Stderr      string `json:"stderr,omitempty"`
	ExitCode    int    `json:"exitCode"`
	Truncated   bool   `json:"truncated"`
	DurationMS  int64  `json:"durationMs"`
	Error       string `json:"error,omitempty"`
}

// Runner runs only the probes compiled into this package. SSH destinations are
// operator-provided aliases; the model cannot supply addresses or credentials.
type Runner struct {
	client        kubernetes.Interface
	restConfig    *rest.Config
	sshConfigPath string
	sshTargets    []string
	rootSocket    string
	timeout       time.Duration
}

type RunnerOption func(*Runner)

// WithRootHelper enables the isolated root-only host collector channel. The
// socket server still accepts only compiled-in profiles and device ids.
func WithRootHelper(socketPath string) RunnerOption {
	return func(runner *Runner) { runner.rootSocket = strings.TrimSpace(socketPath) }
}

func New(client kubernetes.Interface, config *rest.Config, sshConfigPath string, sshTargets []string, options ...RunnerOption) (*Runner, error) {
	if client == nil || config == nil {
		return nil, fmt.Errorf("Kubernetes client and REST config are required")
	}
	cleanTargets := make([]string, 0, len(sshTargets))
	for _, target := range sshTargets {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		if !sshAliasPattern.MatchString(target) {
			return nil, fmt.Errorf("invalid SSH target alias %q", target)
		}
		if !slices.Contains(cleanTargets, target) {
			cleanTargets = append(cleanTargets, target)
		}
	}
	runner := &Runner{client: client, restConfig: rest.CopyConfig(config), sshConfigPath: strings.TrimSpace(sshConfigPath), sshTargets: cleanTargets, timeout: defaultTimeout}
	for _, option := range options {
		option(runner)
	}
	if runner.rootSocket != "" && !validRootHelperSocket(runner.rootSocket) {
		return nil, fmt.Errorf("root collector socket must be an absolute .sock path below /run/infernex-agent")
	}
	return runner, nil
}

func (r *Runner) Probes() []string {
	return []string{
		"system-summary", "filesystem-usage", "network-links", "npu-inventory", "cann-version",
		"hccn-device", "hccn-pfc-stats", "hccl-root-info", "hccl-test-layout",
	}
}

func (r *Runner) SSHTargets() []string { return append([]string(nil), r.sshTargets...) }

func (r *Runner) Channels() []string {
	channels := []string{"local", "pod", "ssh"}
	if r.rootSocket != "" {
		channels = append(channels, "host-root")
	}
	return channels
}

func (r *Runner) Run(ctx context.Context, request Request) (Result, error) {
	channel := strings.ToLower(strings.TrimSpace(request.Channel))
	probe := strings.ToLower(strings.TrimSpace(request.Probe))
	commands, err := probeCommands(probe, request.DeviceID)
	if err != nil {
		return Result{}, err
	}
	if channel == "ssh" {
		if r.sshConfigPath == "" || len(r.sshTargets) == 0 {
			return Result{}, fmt.Errorf("SSH diagnostics are not configured by the operator")
		}
		if !slices.Contains(r.sshTargets, request.SSHTarget) {
			return Result{}, fmt.Errorf("SSH target %q is not in the operator allow-list", request.SSHTarget)
		}
	}
	if channel == "host-root" && r.rootSocket == "" {
		return Result{}, fmt.Errorf("root host diagnostics are not configured")
	}
	var last Result
	var lastErr error
	for _, command := range commands {
		switch channel {
		case "local":
			last, lastErr = r.runLocal(ctx, probe, command)
		case "pod":
			last, lastErr = r.runPod(ctx, probe, request, command)
		case "ssh":
			last, lastErr = r.runSSH(ctx, probe, request.SSHTarget, command)
		case "host-root":
			last, lastErr = callRootHelper(ctx, r.rootSocket, probe, request.DeviceID)
		default:
			return Result{}, fmt.Errorf("channel must be one of local, pod, ssh, or host-root")
		}
		if lastErr == nil {
			return last, nil
		}
	}
	return last, lastErr
}

type commandSpec struct {
	name string
	args []string
}

func probeCommands(probe string, deviceID int) ([]commandSpec, error) {
	switch probe {
	case "system-summary":
		return []commandSpec{{name: "uname", args: []string{"-a"}}}, nil
	case "filesystem-usage":
		return []commandSpec{{name: "df", args: []string{"-h"}}}, nil
	case "network-links":
		return []commandSpec{{name: "ip", args: []string{"-brief", "link"}}}, nil
	case "npu-inventory":
		return []commandSpec{
			{name: "npu-smi", args: []string{"info"}},
			{name: "/usr/local/Ascend/driver/tools/npu-smi", args: []string{"info"}},
		}, nil
	case "cann-version":
		return []commandSpec{
			{name: "cat", args: []string{"/usr/local/Ascend/ascend-toolkit/latest/version.cfg"}},
			{name: "cat", args: []string{"/usr/local/Ascend/driver/version.info"}},
		}, nil
	case "hccn-device":
		if deviceID < 0 || deviceID > 63 {
			return nil, fmt.Errorf("deviceId must be between 0 and 63")
		}
		return []commandSpec{
			{name: "hccn_tool", args: []string{"-i", strconv.Itoa(deviceID), "-link", "-g"}},
			{name: "/usr/local/Ascend/driver/tools/hccn_tool", args: []string{"-i", strconv.Itoa(deviceID), "-link", "-g"}},
		}, nil
	case "hccn-pfc-stats":
		if deviceID < 0 || deviceID > 63 {
			return nil, fmt.Errorf("deviceId must be between 0 and 63")
		}
		// Keep the complete counter snapshot. PFC fields differ between driver
		// versions, so filtering belongs to the evidence analyzer rather than a
		// shell pipeline inside the target Pod or host.
		return []commandSpec{
			{name: "hccn_tool", args: []string{"-i", strconv.Itoa(deviceID), "-stat", "-g"}},
			{name: "/usr/local/Ascend/driver/tools/hccn_tool", args: []string{"-i", strconv.Itoa(deviceID), "-stat", "-g"}},
		}, nil
	case "hccl-root-info":
		return []commandSpec{{name: "cat", args: []string{"/etc/hccl_rootInfo.json"}}}, nil
	case "hccl-test-layout":
		// This is only a presence/layout preflight. Running an HCCL benchmark is
		// load-generating and must be handled as an approved benchmark task.
		return []commandSpec{
			{name: "ls", args: []string{"-la", "/usr/local/Ascend/ascend-toolkit/latest/tools/hccl_test"}},
			{name: "ls", args: []string{"-la", "/usr/local/Ascend/cann/tools/hccl_test"}},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported diagnostic probe %q", probe)
	}
}

func (r *Runner) runLocal(ctx context.Context, probe string, spec commandSpec) (Result, error) {
	return r.runCommand(ctx, "local", "management-node", probe, spec, spec.name, spec.args...)
}

func (r *Runner) runSSH(ctx context.Context, probe, target string, spec commandSpec) (Result, error) {
	args := []string{"-F", r.sshConfigPath, "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10", "--", target, spec.name}
	args = append(args, spec.args...)
	return r.runCommand(ctx, "ssh", target, probe, spec, "ssh", args...)
}

func (r *Runner) runCommand(ctx context.Context, channel, target, probe string, spec commandSpec, executable string, args ...string) (Result, error) {
	started := time.Now()
	probeCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	stdout := &limitedBuffer{limit: maxOutputBytes}
	stderr := &limitedBuffer{limit: maxOutputBytes / 4}
	command := exec.CommandContext(probeCtx, executable, args...)
	command.Stdout, command.Stderr = stdout, stderr
	err := command.Run()
	result := Result{Status: "succeeded", Channel: channel, Probe: probe, Target: target, ActionClass: "active-read", Command: displayCommand(spec), Output: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode(err), Truncated: stdout.truncated || stderr.truncated, DurationMS: time.Since(started).Milliseconds()}
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result, fmt.Errorf("%s probe %s failed: %w", channel, probe, err)
	}
	return result, nil
}

func (r *Runner) runPod(ctx context.Context, probe string, request Request, spec commandSpec) (Result, error) {
	namespace, pod := strings.TrimSpace(request.Namespace), strings.TrimSpace(request.Pod)
	if namespace == "" || pod == "" {
		return Result{}, fmt.Errorf("namespace and pod are required for the pod channel")
	}
	podObject, err := r.client.CoreV1().Pods(namespace).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return Result{}, fmt.Errorf("get target Pod: %w", err)
	}
	container := strings.TrimSpace(request.Container)
	if container == "" {
		if len(podObject.Spec.Containers) != 1 {
			return Result{}, fmt.Errorf("container is required when Pod has %d containers", len(podObject.Spec.Containers))
		}
		container = podObject.Spec.Containers[0].Name
	}
	if !podHasContainer(podObject, container) {
		return Result{}, fmt.Errorf("container %q does not exist in Pod", container)
	}
	started := time.Now()
	probeCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	command := append([]string{spec.name}, spec.args...)
	req := r.client.CoreV1().RESTClient().Post().Resource("pods").Name(pod).Namespace(namespace).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: container, Command: command, Stdout: true, Stderr: true}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(r.restConfig, "POST", req.URL())
	if err != nil {
		return Result{}, fmt.Errorf("create Pod exec transport: %w", err)
	}
	stdout := &limitedBuffer{limit: maxOutputBytes}
	stderr := &limitedBuffer{limit: maxOutputBytes / 4}
	err = executor.StreamWithContext(probeCtx, remotecommand.StreamOptions{Stdout: stdout, Stderr: stderr})
	result := Result{Status: "succeeded", Channel: "pod", Probe: probe, Target: namespace + "/" + pod + ":" + container, ActionClass: "active-read", Command: displayCommand(spec), Output: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode(err), Truncated: stdout.truncated || stderr.truncated, DurationMS: time.Since(started).Milliseconds()}
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result, fmt.Errorf("pod probe %s failed: %w", probe, err)
	}
	return result, nil
}

func podHasContainer(pod *corev1.Pod, name string) bool {
	for _, container := range pod.Spec.Containers {
		if container.Name == name {
			return true
		}
	}
	for _, container := range pod.Spec.InitContainers {
		if container.Name == name {
			return true
		}
	}
	return false
}

func displayCommand(spec commandSpec) string {
	return strings.Join(append([]string{spec.name}, spec.args...), " ")
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

type limitedBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, _ = b.Buffer.Write(p)
	return original, nil
}
