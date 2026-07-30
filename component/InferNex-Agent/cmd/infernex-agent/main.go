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
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	infernexv1alpha1 "gitcode.com/openFuyao/InferNex/api/v1alpha1"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/kube"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/mcpserver"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/observer"
)

var version = "0.1.0-dev"

type options struct {
	transport  string
	listen     string
	kubeconfig string
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
	flag.StringVar(&opts.kubeconfig, "kubeconfig", "", "Path to kubeconfig; in-cluster credentials are preferred when omitted")
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
	server := mcpserver.New(domainObserver, version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch opts.transport {
	case "stdio":
		return server.Run(ctx, &mcp.StdioTransport{})
	case "streamable-http":
		return serveHTTP(ctx, server, opts.listen)
	default:
		return fmt.Errorf("unsupported transport %q", opts.transport)
	}
}

func serveHTTP(ctx context.Context, server *mcp.Server, listenAddress string) error {
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpserver.StreamableHTTPHandler(server))
	mux.HandleFunc("/healthz", healthHandler)
	mux.HandleFunc("/readyz", healthHandler)

	httpServer := &http.Server{
		Addr:              listenAddress,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("serving InferNex MCP tools", "address", listenAddress, "path", "/mcp")
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
}

func healthHandler(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write([]byte("ok\n"))
}
