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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	infernexv1alpha1 "gitcode.com/openFuyao/InferNex/api/v1alpha1"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/analyzer"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/changesafety"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/dashboard"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/deployer"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/diagnostics"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/experiment"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/kube"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/mcpserver"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/observer"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/remediator"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/supervisor"
)

var (
	version = "0.3.0-dev"
	commit  = "unknown"
)

type options struct {
	transport                    string
	listen                       string
	dashboardListen              string
	kubeconfig                   string
	enableDeployment             bool
	enableTestCatalog            bool
	deploymentNamespace          string
	deploymentTemplateNS         string
	deploymentSourceNamespaces   string
	stateDir                     string
	deploymentTimeout            time.Duration
	scanNamespaces               string
	scanInterval                 time.Duration
	eventSinceMinutes            int
	eventLimit                   int
	maxAnalysesPerScan           int
	maxDiagnosticsPerScan        int
	openAIBaseURL                string
	openAIModel                  string
	openAIAPIKeyFile             string
	openAITimeout                time.Duration
	enableAutoRecovery           bool
	recoveryTemplateNS           string
	recoveryMinScans             int
	enableDiagnostics            bool
	enableExperiments            bool
	experimentTemplateNS         string
	experimentTimeout            time.Duration
	experimentSoak               time.Duration
	experimentDiagnosticInterval time.Duration
}

func main() {
	if err := run(); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		slog.Error("infernex-agent stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "cluster-state":
			return runClusterState(os.Args[2:])
		case "chat":
			return runChat(os.Args[2:])
		case "serve":
			return runServer(os.Args[2:])
		case "doctor":
			return runDoctor(os.Args[2:])
		case "candidate":
			return runCandidate(os.Args[2:])
		case "version":
			return runVersion(os.Args[2:])
		case "setup":
			return runSetup(os.Args[2:])
		case "install-diagnose":
			return runInstallDiagnose(os.Args[2:])
		}
	}
	return runServer(os.Args[1:])
}

func runServer(args []string) error {
	opts, err := parseServerOptions(args)
	if err != nil {
		return err
	}
	return serveAgent(opts)
}

func parseServerOptions(args []string) (options, error) {
	opts := options{}
	mergedArgs, configPath, err := mergeServerConfigArgs(args)
	if err != nil {
		return options{}, err
	}
	flags := flag.NewFlagSet("infernex-agent serve", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.String("config", configPath, "read server arguments from an Agent configuration file")
	flags.StringVar(&opts.transport, "transport", "streamable-http", "MCP transport: streamable-http or stdio")
	flags.StringVar(&opts.listen, "listen-address", ":8080", "HTTP listen address")
	flags.StringVar(
		&opts.dashboardListen,
		"dashboard-listen-address",
		"",
		"Dashboard HTTP listen address; empty disables the dashboard",
	)
	flags.BoolVar(
		&opts.enableTestCatalog,
		"enable-test-catalog",
		false,
		"Expose the built-in CPU Kind fixture instead of production deployment-source tools",
	)
	flags.StringVar(&opts.kubeconfig, "kubeconfig", "", "Path to kubeconfig; in-cluster credentials are preferred when omitted")
	flags.BoolVar(
		&opts.enableDeployment,
		"enable-deployment",
		false,
		"Enable constrained catalog deploy/delete tools; disabled by default",
	)
	flags.StringVar(
		&opts.deploymentNamespace,
		"deployment-namespace",
		"infernex-agent-workspace",
		"Agent-managed namespace for conversational deployments",
	)
	flags.StringVar(
		&opts.deploymentTemplateNS,
		"deployment-template-namespace",
		"infernex-bridge-system",
		"Namespace containing existing InferNexServiceConfig deployment profiles",
	)
	flags.StringVar(
		&opts.deploymentSourceNamespaces,
		"deployment-source-namespaces",
		"",
		"Comma-separated namespaces containing stable deployment baselines; defaults to scan namespaces",
	)
	flags.StringVar(
		&opts.stateDir,
		"state-dir",
		"/var/lib/infernex-agent",
		"Protected persistent directory for change records and rollback state",
	)
	flags.DurationVar(
		&opts.deploymentTimeout,
		"deployment-readiness-timeout",
		10*time.Minute,
		"Rollback a newly created catalog service if it is not Ready within this duration",
	)
	flags.StringVar(
		&opts.scanNamespaces,
		"scan-namespaces",
		"",
		"Comma-separated namespaces for continuous InferNex scans; empty disables scanning",
	)
	flags.DurationVar(&opts.scanInterval, "scan-interval", time.Minute, "Continuous scan interval")
	flags.IntVar(&opts.eventSinceMinutes, "event-since-minutes", 60, "Recent event lookback for supervisor scans")
	flags.IntVar(&opts.eventLimit, "event-limit", 25, "Maximum recent events collected for one service")
	flags.IntVar(
		&opts.maxAnalysesPerScan,
		"max-analyses-per-scan",
		10,
		"Maximum new OpenAI analyses in one scan; unchanged evidence is cached",
	)
	flags.IntVar(
		&opts.maxDiagnosticsPerScan,
		"max-diagnostics-per-scan",
		10,
		"Maximum degraded services whose Pod logs are collected in one supervisor scan",
	)
	flags.StringVar(
		&opts.openAIBaseURL,
		"openai-base-url",
		"",
		"OpenAI-compatible base URL; requires --openai-model and enables advisory analysis",
	)
	flags.StringVar(&opts.openAIModel, "openai-model", "", "OpenAI-compatible model name")
	flags.StringVar(
		&opts.openAIAPIKeyFile,
		"openai-api-key-file",
		"",
		"Read the OpenAI-compatible API key from this file; intended for host/systemd installs",
	)
	flags.DurationVar(
		&opts.openAITimeout, "openai-timeout", 3*time.Minute,
		"OpenAI-compatible per-attempt request timeout",
	)
	flags.BoolVar(
		&opts.enableAutoRecovery,
		"enable-auto-recovery",
		false,
		"Create a new recovery InferNexService from an approved profile after consecutive critical scans",
	)
	flags.StringVar(
		&opts.recoveryTemplateNS,
		"recovery-template-namespace",
		"",
		"Namespace containing approved InferNexServiceConfig recovery profiles",
	)
	flags.IntVar(
		&opts.recoveryMinScans,
		"recovery-min-critical-scans",
		3,
		"Consecutive critical scans required before ensuring a recovery service",
	)
	flags.BoolVar(
		&opts.enableDiagnostics,
		"enable-log-diagnostics",
		false,
		"Read bounded logs only from Pods owned by scanned InferNexServices and correlate cross-component incidents",
	)
	flags.BoolVar(
		&opts.enableExperiments,
		"enable-experiments",
		false,
		"Enable durable progressive experiments using approved sparse InferNexServiceConfig feature profiles",
	)
	flags.StringVar(
		&opts.experimentTemplateNS,
		"experiment-template-namespace",
		"infernex-bridge-system",
		"Namespace containing approved experiment feature profiles",
	)
	flags.DurationVar(
		&opts.experimentTimeout,
		"experiment-readiness-timeout",
		20*time.Minute,
		"Maximum duration for one experiment stage to pass readiness, diagnostics, and soak gates",
	)
	flags.DurationVar(
		&opts.experimentSoak,
		"experiment-soak-duration",
		5*time.Minute,
		"Continuous healthy duration required before an experiment candidate becomes the next stable baseline",
	)
	flags.DurationVar(
		&opts.experimentDiagnosticInterval,
		"experiment-diagnostic-interval",
		30*time.Second,
		"Interval between candidate-versus-baseline log diagnostic comparisons during soak",
	)
	if err := flags.Parse(mergedArgs); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if opts.enableExperiments && !opts.enableDiagnostics {
		return options{}, fmt.Errorf("--enable-experiments requires --enable-log-diagnostics")
	}
	if opts.enableTestCatalog && !opts.enableDeployment {
		return options{}, fmt.Errorf("--enable-test-catalog requires --enable-deployment")
	}
	return opts, nil
}

func serveAgent(opts options) error {

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
	serverOptions := make([]mcpserver.Option, 0, 3)
	namespaces := parseNamespaces(opts.scanNamespaces)
	serverOptions = append(serverOptions, mcpserver.WithNamespaces(namespaces))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var changeStore changesafety.Store
	if opts.enableDeployment || opts.enableAutoRecovery || opts.enableExperiments {
		fileStore, err := changesafety.NewFileStore(filepath.Join(opts.stateDir, "changes"))
		if err != nil {
			return fmt.Errorf("configure persistent change store: %w", err)
		}
		changeStore = fileStore
	}
	if opts.enableDeployment {
		sourceNamespaces := parseNamespaces(opts.deploymentSourceNamespaces)
		if len(sourceNamespaces) == 0 {
			sourceNamespaces = namespaces
		}
		deployerOptions := []deployer.Option{
			deployer.WithStore(changeStore),
			deployer.WithReadiness(opts.deploymentTimeout, 2*time.Second),
		}
		if !opts.enableTestCatalog {
			deployerOptions = append(deployerOptions, deployer.WithDeploymentScope(
				opts.deploymentNamespace,
				opts.deploymentTemplateNS,
				sourceNamespaces,
			))
		}
		domainDeployer := deployer.New(kubeClient, deployerOptions...)
		if err := domainDeployer.Start(ctx); err != nil {
			return fmt.Errorf("resume deployment safety monitoring: %w", err)
		}
		serverOptions = append(serverOptions, mcpserver.WithDeployer(domainDeployer))
		if opts.enableTestCatalog {
			serverOptions = append(serverOptions, mcpserver.WithTestCatalog())
		}
	}

	var domainDiagnoser diagnostics.Diagnoser
	if opts.enableDiagnostics {
		clientset, clientsetErr := kubernetes.NewForConfig(restConfig)
		if clientsetErr != nil {
			return fmt.Errorf("create Kubernetes log client: %w", clientsetErr)
		}
		collector, collectorErr := diagnostics.New(
			kubeClient,
			diagnostics.NewKubernetesLogReader(clientset),
			domainObserver,
		)
		if collectorErr != nil {
			return fmt.Errorf("configure service diagnostics: %w", collectorErr)
		}
		domainDiagnoser = collector
		serverOptions = append(serverOptions, mcpserver.WithDiagnoser(collector))
	}

	var domainExperiments experiment.Manager
	if opts.enableExperiments {
		planStore, storeErr := experiment.NewFileStore(filepath.Join(opts.stateDir, "experiments"))
		if storeErr != nil {
			return fmt.Errorf("configure persistent experiment store: %w", storeErr)
		}
		controller, controllerErr := experiment.New(
			kubeClient,
			changeStore,
			planStore,
			domainDiagnoser,
			experiment.Config{
				TemplateNamespace:  opts.experimentTemplateNS,
				ReadinessTimeout:   opts.experimentTimeout,
				SoakDuration:       opts.experimentSoak,
				PollInterval:       5 * time.Second,
				DiagnosticInterval: opts.experimentDiagnosticInterval,
				DiagnosticsMinutes: opts.eventSinceMinutes,
			},
		)
		if controllerErr != nil {
			return fmt.Errorf("configure progressive experiments: %w", controllerErr)
		}
		if controllerErr := controller.Start(ctx); controllerErr != nil {
			return fmt.Errorf("resume progressive experiments: %w", controllerErr)
		}
		domainExperiments = controller
		serverOptions = append(serverOptions, mcpserver.WithExperiments(controller))
	}
	server := mcpserver.New(domainObserver, version, serverOptions...)

	domainAnalyzer, err := buildAnalyzer(opts)
	if err != nil {
		return err
	}
	var domainRemediator supervisor.Remediator
	if opts.enableAutoRecovery {
		profileRemediator, remediatorErr := remediator.New(
			kubeClient,
			opts.recoveryTemplateNS,
			remediator.WithStore(changeStore),
		)
		if remediatorErr != nil {
			return fmt.Errorf("configure recovery remediator: %w", remediatorErr)
		}
		if remediatorErr := profileRemediator.Start(ctx); remediatorErr != nil {
			return fmt.Errorf("resume recovery change records: %w", remediatorErr)
		}
		domainRemediator = profileRemediator
	}
	snapshotStore := supervisor.NewSnapshotStore(version, opts.scanInterval, domainAnalyzer != nil)
	if len(namespaces) > 0 {
		scanner, scannerErr := supervisor.New(
			domainObserver,
			domainAnalyzer,
			domainRemediator,
			snapshotStore,
			supervisor.Config{
				Namespaces:            namespaces,
				Interval:              opts.scanInterval,
				EventSinceMinutes:     opts.eventSinceMinutes,
				EventLimit:            opts.eventLimit,
				MaxAnalysesPerScan:    opts.maxAnalysesPerScan,
				MaxDiagnosticsPerScan: opts.maxDiagnosticsPerScan,
				MinCriticalScans:      opts.recoveryMinScans,
				Diagnoser:             domainDiagnoser,
			},
		)
		if scannerErr != nil {
			return fmt.Errorf("configure supervisor: %w", scannerErr)
		}
		go scanner.Run(ctx)
	} else {
		// A Bridge-less Helm installation intentionally has no InferNexService
		// namespaces to scan. The HTTP service is nevertheless ready to serve MCP
		// and the dashboard, so publish a valid empty snapshot instead of leaving
		// dashboard readiness waiting forever for a scanner that was not started.
		snapshotStore.Store(supervisor.Snapshot{
			GeneratedAt: time.Now().UTC(),
			Ready:       true,
			Namespaces:  make([]supervisor.NamespaceSnapshot, 0),
		})
		slog.Info("running without InferNex Bridge namespace scanner")
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
			dashboardOptions := make([]dashboard.Option, 0, 1)
			if domainExperiments != nil {
				dashboardOptions = append(dashboardOptions, dashboard.WithExperiments(domainExperiments))
			}
			dashboardHandler = dashboard.New(snapshotStore, dashboardOptions...)
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
