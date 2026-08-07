/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"

	infernexv1alpha1 "gitcode.com/openFuyao/InferNex/api/v1alpha1"
	infernexchat "gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/chat"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/kube"
)

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type doctorReport struct {
	Version string        `json:"version"`
	Commit  string        `json:"commit"`
	Checks  []doctorCheck `json:"checks"`
	Ready   bool          `json:"ready"`
}

type stringListFlag []string

func (values *stringListFlag) String() string { return strings.Join(*values, ",") }

func (values *stringListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("namespace must not be empty")
	}
	*values = append(*values, value)
	return nil
}

func runDoctor(args []string) error {
	flags := flag.NewFlagSet("infernex-agent doctor", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", "/etc/infernex-agent/agent.conf", "installed Agent configuration")
	kubeconfigOverride := flags.String("kubeconfig", "", "override kubeconfig from the Agent configuration")
	jsonOutput := flags.Bool("json", false, "print machine-readable results")
	skipLocal := flags.Bool("skip-local", false, "skip the currently running Agent health check")
	skipModel := flags.Bool("skip-model", false, "skip the configured OpenAI-compatible model probe")
	timeout := flags.Duration("timeout", 15*time.Second, "overall external-check deadline")
	namespaces := stringListFlag{}
	flags.Var(&namespaces, "namespace", "override scan namespace; repeatable")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if *timeout < time.Second || *timeout > 5*time.Minute {
		return fmt.Errorf("--timeout must be between 1s and 5m")
	}

	opts, err := parseServerOptions([]string{"--config", *configPath})
	if err != nil {
		return err
	}
	if strings.TrimSpace(*kubeconfigOverride) != "" {
		opts.kubeconfig = strings.TrimSpace(*kubeconfigOverride)
	}
	if len(namespaces) > 0 {
		opts.scanNamespaces = strings.Join(namespaces, ",")
	}

	report := doctorReport{Version: version, Commit: commit, Ready: true}
	add := func(name, status, detail string) {
		report.Checks = append(report.Checks, doctorCheck{Name: name, Status: status, Detail: detail})
		if status == "fail" {
			report.Ready = false
		}
	}

	build := currentVersionInfo()
	buildStatus := "pass"
	buildDetail := fmt.Sprintf("%s/%s; CGO_ENABLED=%s", runtime.GOOS, runtime.GOARCH, build.CGO)
	if runtime.GOOS != "linux" || build.CGO != "0" {
		buildStatus = "warn"
		buildDetail += "; official server candidates are static Linux binaries"
	}
	add("binary", buildStatus, buildDetail)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	restConfig, configErr := kube.Config(opts.kubeconfig)
	if configErr != nil {
		add("kubernetes", "fail", configErr.Error())
	} else {
		restConfig.Timeout = *timeout
		discoveryClient, discoveryErr := discovery.NewDiscoveryClientForConfig(restConfig)
		if discoveryErr != nil {
			add("kubernetes", "fail", discoveryErr.Error())
		} else if serverVersion, versionErr := discoveryClient.ServerVersion(); versionErr != nil {
			add("kubernetes", "fail", versionErr.Error())
		} else {
			add("kubernetes", "pass", "API server "+serverVersion.GitVersion)
		}

		groupVersion := infernexv1alpha1.GroupVersion.String()
		bridgeAPIAvailable := true
		if _, resourceErr := discoveryClient.ServerResourcesForGroupVersion(groupVersion); resourceErr != nil {
			bridgeAPIAvailable = false
			add(
				"platform-mode",
				"warn",
				"generic Kubernetes/Helm compatibility mode; InferNex Bridge API "+groupVersion+" is not installed",
			)
		} else {
			add("platform-mode", "pass", "InferNex Bridge API "+groupVersion+" is discoverable")
		}

		dynamicClient, dynamicErr := dynamic.NewForConfig(restConfig)
		if dynamicErr != nil {
			add("infernex-services", "fail", dynamicErr.Error())
		} else if bridgeAPIAvailable {
			serviceResource := schema.GroupVersionResource{
				Group:    infernexv1alpha1.GroupVersion.Group,
				Version:  infernexv1alpha1.GroupVersion.Version,
				Resource: "infernexservices",
			}
			targetNamespaces := parseNamespaces(opts.scanNamespaces)
			if len(targetNamespaces) == 0 {
				add("infernex-services", "warn", "no scan namespace is configured")
			}
			for _, namespace := range targetNamespaces {
				services, listErr := dynamicClient.Resource(serviceResource).Namespace(namespace).List(ctx, metav1.ListOptions{Limit: 1})
				if listErr != nil {
					add("namespace/"+namespace, "fail", listErr.Error())
				} else {
					add("namespace/"+namespace, "pass", fmt.Sprintf("InferNexService list allowed; sampled %d", len(services.Items)))
				}
			}
			if opts.enableDeployment && !opts.enableTestCatalog {
				profileResource := schema.GroupVersionResource{
					Group:    infernexv1alpha1.GroupVersion.Group,
					Version:  infernexv1alpha1.GroupVersion.Version,
					Resource: "infernexserviceconfigs",
				}
				profiles, listErr := dynamicClient.Resource(profileResource).
					Namespace(opts.deploymentTemplateNS).
					List(ctx, metav1.ListOptions{Limit: 100})
				if listErr != nil {
					add("deployment-sources", "fail", listErr.Error())
				} else {
					add(
						"deployment-sources",
						"pass",
						fmt.Sprintf(
							"workspace %s; %d Bridge profiles sampled from %s",
							opts.deploymentNamespace,
							len(profiles.Items),
							opts.deploymentTemplateNS,
						),
					)
				}
			}
		} else {
			add(
				"infernex-services",
				"warn",
				"Bridge-specific service discovery is disabled; base Kubernetes/Helm asset adapters are pending",
			)
		}
	}

	if !*skipLocal && opts.transport == "streamable-http" {
		healthURL, healthErr := localHealthURL(opts.listen)
		if healthErr != nil {
			add("running-agent", "fail", healthErr.Error())
		} else if healthErr = probeHTTP(ctx, healthURL); healthErr != nil {
			add("running-agent", "fail", healthErr.Error())
		} else {
			add("running-agent", "pass", healthURL)
		}
	}

	if !*skipModel && strings.TrimSpace(opts.openAIBaseURL) != "" {
		apiKey, keyErr := openAIAPIKey(opts.openAIAPIKeyFile)
		if keyErr != nil {
			add("model", "fail", keyErr.Error())
		} else {
			model, modelErr := infernexchat.NewOpenAI(infernexchat.OpenAIConfig{
				BaseURL: opts.openAIBaseURL,
				Model:   opts.openAIModel,
				APIKey:  apiKey,
				Timeout: *timeout,
			})
			if modelErr == nil {
				_, modelErr = model.Complete(ctx, []infernexchat.Message{{Role: "user", Content: "Reply with OK."}}, nil)
			}
			if modelErr != nil {
				add("model", "fail", modelErr.Error())
			} else {
				add("model", "pass", "OpenAI-compatible chat completion succeeded; credentials hidden")
			}
		}
	} else if strings.TrimSpace(opts.openAIBaseURL) == "" {
		add("model", "warn", "model analysis is not configured")
	}

	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(report); err != nil {
			return err
		}
	} else {
		for _, check := range report.Checks {
			fmt.Fprintf(os.Stdout, "[%s] %s: %s\n", strings.ToUpper(check.Status), check.Name, check.Detail)
		}
	}
	if !report.Ready {
		return fmt.Errorf("doctor found one or more blocking failures")
	}
	return nil
}

func localHealthURL(address string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return "", fmt.Errorf("invalid Agent listen address %q: %w", address, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: "/healthz"}).String(), nil
}

func probeHTTP(ctx context.Context, endpoint string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("%s returned HTTP %d", endpoint, response.StatusCode)
	}
	return nil
}
