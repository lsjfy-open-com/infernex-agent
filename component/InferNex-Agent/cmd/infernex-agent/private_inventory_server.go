package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domainstore"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/mcpserver"
)

func privateInventoryServerOption(stateDirectory string) (mcpserver.Option, error) {
	store, err := domainstore.Open(stateDirectory, os.Geteuid())
	if err != nil {
		return nil, fmt.Errorf("open private inventory state: %w", err)
	}
	service, err := newPrivateInventoryService(store)
	if err != nil {
		return nil, fmt.Errorf("configure private inventory service: %w", err)
	}
	return mcpserver.WithPrivateInventory(service, localPrivatePrincipal()), nil
}

func servePrivateInventoryOnly(opts options) error {
	privateOption, err := privateInventoryServerOption(opts.privateStateDirectory)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := mcpserver.New(nil, version, mcpserver.WithInferNexBridge(false), privateOption)
	return server.Run(ctx, &mcp.StdioTransport{})
}
