/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/diagnosticexec"
)

func runCollectorHelper(args []string) error {
	flags := flag.NewFlagSet("infernex-agent collector-helper", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	socketPath := flags.String("socket", "/run/infernex-agent/collector.sock", "protected Unix socket path")
	socketGroup := flags.String("group", "infernex-agent", "group allowed to call the helper")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected collector-helper arguments")
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("collector-helper must run as root")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return diagnosticexec.ServeRootHelper(ctx, *socketPath, *socketGroup)
}
