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

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infernexv1alpha1 "gitcode.com/openFuyao/InferNex/api/v1alpha1"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/changesafety"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/kube"
)

type namespaceFlags []string

func (values *namespaceFlags) String() string {
	return strings.Join(*values, ",")
}

func (values *namespaceFlags) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("namespace cannot be empty")
	}
	*values = append(*values, value)
	return nil
}

func runClusterState(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("cluster-state requires backup, restore, or verify")
	}
	switch args[0] {
	case "backup":
		return runClusterStateBackup(args[1:])
	case "restore":
		return runClusterStateRestore(args[1:])
	case "verify":
		return runClusterStateVerify(args[1:])
	default:
		return fmt.Errorf("unsupported cluster-state command %q", args[0])
	}
}

func runClusterStateBackup(args []string) error {
	flags := flag.NewFlagSet("cluster-state backup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	kubeconfig := flags.String("kubeconfig", "", "Kubeconfig path")
	output := flags.String("output", "", "New snapshot file")
	purpose := flags.String("purpose", "manual", "Snapshot purpose")
	var namespaces namespaceFlags
	flags.Var(&namespaces, "namespace", "Managed namespace to capture; repeatable")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse cluster-state backup arguments: %w", err)
	}
	if strings.TrimSpace(*output) == "" {
		return fmt.Errorf("cluster-state backup requires --output")
	}
	if len(namespaces) == 0 {
		return fmt.Errorf("cluster-state backup requires at least one --namespace")
	}
	kubeClient, err := clusterStateClient(*kubeconfig)
	if err != nil {
		return err
	}
	snapshot, err := changesafety.Capture(
		context.Background(),
		kubeClient,
		namespaces,
		*purpose,
	)
	if err != nil {
		return fmt.Errorf("capture cluster state: %w", err)
	}
	if err := changesafety.WriteSnapshot(*output, snapshot); err != nil {
		return err
	}
	return writeJSON(os.Stdout, map[string]any{
		"snapshotId": snapshot.ID,
		"output":     *output,
		"namespaces": snapshot.Namespaces,
		"resources":  len(snapshot.Resources),
		"sha256":     snapshot.SHA256,
	})
}

func runClusterStateRestore(args []string) error {
	flags := flag.NewFlagSet("cluster-state restore", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	kubeconfig := flags.String("kubeconfig", "", "Kubeconfig path")
	input := flags.String("input", "", "Snapshot file")
	confirm := flags.Bool("confirm", false, "Confirm restoration of Agent-managed resources")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse cluster-state restore arguments: %w", err)
	}
	if strings.TrimSpace(*input) == "" {
		return fmt.Errorf("cluster-state restore requires --input")
	}
	if !*confirm {
		return fmt.Errorf("cluster-state restore requires --confirm")
	}
	snapshot, err := changesafety.ReadSnapshot(*input)
	if err != nil {
		return err
	}
	kubeClient, err := clusterStateClient(*kubeconfig)
	if err != nil {
		return err
	}
	result, err := changesafety.Restore(context.Background(), kubeClient, snapshot)
	if err != nil {
		return fmt.Errorf("restore cluster state: %w", err)
	}
	return writeJSON(os.Stdout, result)
}

func runClusterStateVerify(args []string) error {
	flags := flag.NewFlagSet("cluster-state verify", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	input := flags.String("input", "", "Snapshot file")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse cluster-state verify arguments: %w", err)
	}
	if strings.TrimSpace(*input) == "" {
		return fmt.Errorf("cluster-state verify requires --input")
	}
	snapshot, err := changesafety.ReadSnapshot(*input)
	if err != nil {
		return err
	}
	return writeJSON(os.Stdout, map[string]any{
		"snapshotId": snapshot.ID,
		"valid":      true,
		"namespaces": snapshot.Namespaces,
		"resources":  len(snapshot.Resources),
		"sha256":     snapshot.SHA256,
	})
}

func clusterStateClient(kubeconfig string) (client.Client, error) {
	restConfig, err := kube.Config(strings.TrimSpace(kubeconfig))
	if err != nil {
		return nil, fmt.Errorf("build Kubernetes client config: %w", err)
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("register Kubernetes scheme: %w", err)
	}
	if err := infernexv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("register InferNex scheme: %w", err)
	}
	kubeClient, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	return kubeClient, nil
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("write command result: %w", err)
	}
	return nil
}
