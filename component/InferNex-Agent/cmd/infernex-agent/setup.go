/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package main

import (
	"fmt"
	"os"
	"os/exec"
)

// runSetup keeps the product entry point in the static binary while reusing
// the audited, rollback-aware host configuration helper. The helper owns file
// permissions, endpoint testing, service restart, and configuration rollback.
func runSetup(args []string) error {
	const configurator = "/opt/infernex-agent/bin/configure-model.sh"
	if _, err := os.Stat(configurator); err != nil {
		return fmt.Errorf("host Agent is not installed; missing %s", configurator)
	}
	commandArgs := append([]string{"--interactive"}, args...)
	command := exec.Command(configurator, commandArgs...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Env = os.Environ()
	if err := command.Run(); err != nil {
		return fmt.Errorf("configure Agent model interface: %w", err)
	}
	return nil
}
