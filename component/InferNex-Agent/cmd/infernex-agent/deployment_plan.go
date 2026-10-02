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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/deploymentplan"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/kube"
)

const maxDeploymentProfileBytes = 1 << 20

type deploymentPlanReaderFactory func(string) (client.Reader, error)

func runDeploymentPlan(args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return runDeploymentPlanWithIO(ctx, args, os.Stdout, os.Stderr, newDeploymentPlanReader)
}

func runDeploymentPlanWithIO(ctx context.Context, args []string, stdout, stderr io.Writer, readerFactory deploymentPlanReaderFactory) error {
	flags := flag.NewFlagSet("infernex-agent deployment-plan", flag.ContinueOnError)
	flags.SetOutput(stderr)
	profilePath := flags.String("profile", "", "path to a versioned deployment profile JSON file")
	replicas := flags.Int("replicas", 1, "number of identical replicas to estimate")
	namespace := flags.String("namespace", "default", "target Kubernetes namespace")
	kubeconfig := flags.String("kubeconfig", "", "path to an existing kubeconfig; in-cluster/default discovery is used when omitted")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse deployment-plan arguments: %w", err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if strings.TrimSpace(*profilePath) == "" {
		return fmt.Errorf("deployment-plan requires --profile FILE")
	}
	if path := strings.TrimSpace(*kubeconfig); path != "" {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("read kubeconfig %q: %w", path, err)
		}
		if info.IsDir() {
			return fmt.Errorf("kubeconfig %q is a directory", path)
		}
	}
	profile, err := readDeploymentProfile(strings.TrimSpace(*profilePath))
	if err != nil {
		return err
	}
	reader, err := readerFactory(strings.TrimSpace(*kubeconfig))
	if err != nil {
		return err
	}
	plan, err := deploymentplan.Generate(ctx, reader, deploymentplan.Request{
		Namespace: strings.TrimSpace(*namespace),
		Replicas:  *replicas,
		Profile:   profile,
	})
	if err != nil {
		return err
	}
	return writeJSON(stdout, plan)
}

func readDeploymentProfile(path string) (deploymentplan.Profile, error) {
	file, err := os.Open(path)
	if err != nil {
		return deploymentplan.Profile{}, fmt.Errorf("open deployment profile %q: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return deploymentplan.Profile{}, fmt.Errorf("stat deployment profile %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return deploymentplan.Profile{}, fmt.Errorf("deployment profile %q is not a regular file", path)
	}
	if info.Size() > maxDeploymentProfileBytes {
		return deploymentplan.Profile{}, fmt.Errorf("deployment profile %q exceeds %d bytes", path, maxDeploymentProfileBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxDeploymentProfileBytes+1))
	if err != nil {
		return deploymentplan.Profile{}, fmt.Errorf("read deployment profile %q: %w", path, err)
	}
	if len(data) > maxDeploymentProfileBytes {
		return deploymentplan.Profile{}, fmt.Errorf("deployment profile %q exceeds %d bytes", path, maxDeploymentProfileBytes)
	}
	return decodeDeploymentProfile(string(data))
}

func decodeDeploymentProfile(input string) (deploymentplan.Profile, error) {
	var profile deploymentplan.Profile
	decoder := json.NewDecoder(bytes.NewBufferString(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&profile); err != nil {
		return profile, fmt.Errorf("decode deployment profile JSON: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return profile, fmt.Errorf("decode deployment profile JSON: multiple JSON values are not allowed")
	}
	return profile, nil
}

func newDeploymentPlanReader(kubeconfig string) (client.Reader, error) {
	restConfig, err := kube.Config(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("build Kubernetes client config: %w", err)
	}
	restConfig.Timeout = 30 * time.Second
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("register core Kubernetes scheme: %w", err)
	}
	reader, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes deployment-plan reader: %w", err)
	}
	return reader, nil
}
