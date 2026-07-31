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
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	infernexv1alpha1 "gitcode.com/openFuyao/InferNex/api/v1alpha1"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/analyzer"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/dashboard"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/deployer"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/kube"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/mcpserver"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/observer"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/remediator"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/supervisor"
)

var version = "0.3.0-dev"

type options struct {
	transport          string
	listen             string
	dashboardListen    string
	kubeconfig         string
	enableDeployment   bool
	scanNamespaces     string
	scanInterval       time.Duration
	eventSinceMinutes  int
	eventLimit         int
	maxAnalysesPerScan int
	openAIBaseURL      string
	openAIModel        string
	openAIAPIKeyFile   string
	openAITimeout      time.Duration
	enableAutoRecovery bool
	recoveryTemplateNS string
	recoveryMinScans   int
}

func main() {
	if err := run(); err != nil {
		slog.Error("infernex-agent stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	opts := options{}
	flag.StringVar(&opts.transport, "transport", "streamable-http", "MCP transport: streamable-http or stdio")
	flag.StringVar(&opts.listen, "listen-address", ":8080", "HTTP listen address")
	flag.StringVar(
		&opts.dashboardListen,
		"dashboard-listen-address",
		"",
		"Dashboard HTTP listen address; empty disables the dashboard",
	)
	flag.StringVar(&opts.kubeconfig, "kubeconfig", "", "Path to kubeconfig; in-cluster credentials are preferred when omitted")
	flag.BoolVar(
		&opts.enableDeployment,
		"enable-deployment",
		false,
		"Enable constrained catalog deploy/delete tools; disabled by default",
	)
	flag.StringVar(
		&opts.scanNamespaces,
		"scan-namespaces",
		"",
		"Comma-separated namespaces for continuous InferNex scans; empty disables scanning",
	)
	flag.DurationVar(&opts.scanInterval, "scan-interval", time.Minute, "Continuous scan interval")
	flag.IntVar(&opts.eventSinceMinutes, "event-since-minutes", 60, "Recent event lookback for supervisor scans")
	flag.IntVar(&opts.eventLimit, "event-limit", 25, "Maximum recent events collected for one service")
	flag.IntVar(
		&opts.maxAnalysesPerScan,
		"max-analyses-per-scan",
		10,
		"Maximum new OpenAI analyses in one scan; unchanged evidence is cached",
	)
	flag.StringVar(
		&opts.openAIBaseURL,
		"openai-base-url",
		"",
		"OpenAI-compatible base URL; requires --openai-model and enables advisory analysis",
	)
	flag.StringVar(&opts.openAIModel, "openai-model", "", "OpenAI-compatible model name")
	flag.StringVar(
		&opts.openAIAPIKeyFile,
		"openai-api-key-file",
		"",
		"Read the OpenAI-compatible API key from this file; intended for host/systemd installs",
	)
	flag.DurationVar(&opts.openAITimeout, "openai-timeout", time.Minute, "OpenAI-compatible request timeout")
	flag.BoolVar(
		&opts.enableAutoRecovery,
		"enable-auto-recovery",
		false,
		"Create a new recovery InferNexService from an approved profile after consecutive critical scans",
	)
	flag.StringVar(
		&opts.recoveryTemplateNS,
		"recovery-template-namespace",
		"",
		"Namespace containing approved InferNexServiceConfig recovery profiles",
	)
	flag.IntVar(
		&opts.recoveryMinScans,
		"recovery-min-critical-scans",
		3,
		"Consecutive critical scans required before ensuring a recovery service",
	)
	flag.Parse()

	restConfig, err := kube.Config(opts.kubeconfig)
	if err != nil {
		return fmt.Errorf("build Kubernetes client config: %w", err)
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return fmt.Errorf("register Kubernetes scheme: %w", err)
	}
	if err := infernexv1alpha1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("register InferNex scheme: %w", err)
	}
	if err := lwsv1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("register LeaderWorkerSet scheme: %w", err)
	}

	kubeClient, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("create Kubernetes client: %w", err)
	}

	domainObserver := observer.New(kubeClient)
	serverOptions := make([]mcpserver.Option, 0, 1)
	if opts.enableDeployment {
		serverOptions = append(serverOptions, mcpserver.WithDeployer(deployer.New(kubeClient)))
	}
	server := mcpserver.New(domainObserver, version, serverOptions...)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	domainAnalyzer, err := buildAnalyzer(opts)
	if err != nil {
		return err
	}
	var domainRemediator supervisor.Remediator
	if opts.enableAutoRecovery {
		profileRemediator, remediatorErr := remediator.New(kubeClient, opts.recoveryTemplateNS)
		if remediatorErr != nil {
			return fmt.Errorf("configure recovery remediator: %w", remediatorErr)
		}
		domainRemediator = profileRemediator
	}
	snapshotStore := supervisor.NewSnapshotStore(version, opts.scanInterval, domainAnalyzer != nil)
	namespaces := parseNamespaces(opts.scanNamespaces)
	if len(namespaces) > 0 {
		scanner, scannerErr := supervisor.New(
			domainObserver,
			domainAnalyzer,
			domainRemediator,
			snapshotStore,
			supervisor.Config{
				Namespaces:         namespaces,
				Interval:           opts.scanInterval,
				EventSinceMinutes:  opts.eventSinceMinutes,
				EventLimit:         opts.eventLimit,
				MaxAnalysesPerScan: opts.maxAnalysesPerScan,
				MinCriticalScans:   opts.recoveryMinScans,
			},
		)
		if scannerErr != nil {
			return fmt.Errorf("configure supervisor: %w", scannerErr)
		}
		go scanner.Run(ctx)
	}

	switch opts.transport {
	case "stdio":
		if strings.TrimSpace(opts.dashboardListen) != "" {
			return fmt.Errorf("dashboard HTTP listener requires streamable-http transport")
		}
		return server.Run(ctx, &mcp.StdioTransport{})
	case "streamable-http":
		var dashboardHandler http.Handler
		if strings.TrimSpace(opts.dashboardListen) != "" {
			dashboardHandler = dashboard.New(snapshotStore)
		}
		return serveHTTP(ctx, server, opts.listen, opts.dashboardListen, dashboardHandler)
	default:
		return fmt.Errorf("unsupported transport %q", opts.transport)
	}
}

func buildAnalyzer(opts options) (supervisor.Analyzer, error) {
	baseURL := strings.TrimSpace(opts.openAIBaseURL)
	model := strings.TrimSpace(opts.openAIModel)
	if baseURL == "" && model == "" {
		return nil, nil
	}
	if baseURL == "" || model == "" {
		return nil, fmt.Errorf("--openai-base-url and --openai-model must be configured together")
	}
	apiKey, err := openAIAPIKey(opts.openAIAPIKeyFile)
	if err != nil {
		return nil, err
	}
	domainAnalyzer, err := analyzer.NewOpenAI(analyzer.OpenAIConfig{
		BaseURL: baseURL,
		Model:   model,
		APIKey:  apiKey,
		Timeout: opts.openAITimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("configure OpenAI-compatible analyzer: %w", err)
	}
	return domainAnalyzer, nil
}

func openAIAPIKey(filePath string) (string, error) {
	const maxAPIKeyBytes = 64 * 1024

	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return os.Getenv("INFERNEX_OPENAI_API_KEY"), nil
	}
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return "", fmt.Errorf("stat OpenAI API key file: %w", err)
	}
	if !fileInfo.Mode().IsRegular() {
		return "", fmt.Errorf("OpenAI API key file must be a regular file")
	}
	if fileInfo.Size() > maxAPIKeyBytes {
		return "", fmt.Errorf("OpenAI API key file exceeds %d bytes", maxAPIKeyBytes)
	}
	contents, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("read OpenAI API key file: %w", err)
	}
	apiKey := strings.TrimRight(string(contents), "\r\n")
	if strings.ContainsAny(apiKey, "\r\n\x00") {
		return "", fmt.Errorf("OpenAI API key file must contain exactly one text line")
	}
	return apiKey, nil
}

func parseNamespaces(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.Split(value, ",")
}

func serveHTTP(
	ctx context.Context,
	server *mcp.Server,
	listenAddress string,
	dashboardListenAddress string,
	dashboardHandler http.Handler,
) error {
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpserver.StreamableHTTPHandler(server))
	mux.HandleFunc("/healthz", healthHandler)
	mux.HandleFunc("/readyz", healthHandler)

	servers := []*http.Server{newHTTPServer(listenAddress, mux)}
	names := []string{"MCP"}
	if dashboardHandler != nil {
		if strings.TrimSpace(dashboardListenAddress) == strings.TrimSpace(listenAddress) {
			return fmt.Errorf("MCP and dashboard listen addresses must differ")
		}
		servers = append(servers, newHTTPServer(dashboardListenAddress, dashboardHandler))
		names = append(names, "dashboard")
	}

	errCh := make(chan error, len(servers))
	for index, httpServer := range servers {
		go func(name string, serverToRun *http.Server) {
			slog.Info("serving InferNex HTTP endpoint", "name", name, "address", serverToRun.Addr)
			errCh <- serverToRun.ListenAndServe()
		}(names[index], httpServer)
	}

	var serveErr error
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, httpServer := range servers {
		if err := httpServer.Shutdown(shutdownCtx); err != nil && serveErr == nil {
			serveErr = err
		}
	}
	return serveErr
}

func newHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
}

func healthHandler(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write([]byte("ok\n"))
}
