/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package diagnosticexec

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const maxRootHelperRequestBytes = 8 * 1024

var rootHelperProbes = map[string]bool{
	"npu-inventory": true, "cann-version": true, "hccn-device": true,
	"hccn-pfc-stats": true, "hccl-root-info": true, "hccl-test-layout": true,
}

var rootHelperSlots = make(chan struct{}, 2)

type rootHelperRequest struct {
	Probe    string `json:"probe"`
	DeviceID int    `json:"deviceId"`
}

type rootHelperResponse struct {
	Result Result `json:"result"`
	Error  string `json:"error,omitempty"`
}

func validRootHelperSocket(path string) bool {
	clean := filepath.Clean(strings.TrimSpace(path))
	return filepath.IsAbs(clean) && filepath.Ext(clean) == ".sock" && filepath.Dir(clean) == "/run/infernex-agent"
}

// ServeRootHelper runs the deliberately small privileged execution plane. It
// has no model, Kubernetes, HTTP, file-read, or arbitrary-command interface.
func ServeRootHelper(ctx context.Context, socketPath, groupName string) error {
	if !validRootHelperSocket(socketPath) {
		return fmt.Errorf("root collector socket must be an absolute .sock path below /run/infernex-agent")
	}
	group, err := user.LookupGroup(strings.TrimSpace(groupName))
	if err != nil {
		return fmt.Errorf("lookup collector socket group: %w", err)
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return fmt.Errorf("parse collector socket group id: %w", err)
	}
	directory := filepath.Dir(socketPath)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return fmt.Errorf("create collector runtime directory: %w", err)
	}
	if err := os.Chown(directory, 0, gid); err != nil {
		return fmt.Errorf("protect collector runtime directory: %w", err)
	}
	if info, statErr := os.Lstat(socketPath); statErr == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("refusing to replace non-socket root collector path")
		}
		if err := os.Remove(socketPath); err != nil {
			return fmt.Errorf("remove stale collector socket: %w", err)
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on collector socket: %w", err)
	}
	defer listener.Close()
	defer os.Remove(socketPath)
	if err := os.Chmod(socketPath, 0o660); err != nil {
		return err
	}
	if err := os.Chown(socketPath, 0, gid); err != nil {
		return err
	}
	go func() { <-ctx.Done(); _ = listener.Close() }()
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept collector request: %w", err)
		}
		go handleRootHelperConnection(ctx, connection)
	}
}

func handleRootHelperConnection(ctx context.Context, connection net.Conn) {
	defer connection.Close()
	select {
	case rootHelperSlots <- struct{}{}:
		defer func() { <-rootHelperSlots }()
	default:
		_ = json.NewEncoder(connection).Encode(rootHelperResponse{Error: "root collector concurrency budget is exhausted"})
		return
	}
	_ = connection.SetDeadline(time.Now().Add(45 * time.Second))
	decoder := json.NewDecoder(io.LimitReader(connection, maxRootHelperRequestBytes))
	decoder.DisallowUnknownFields()
	var request rootHelperRequest
	if err := decoder.Decode(&request); err != nil {
		_ = json.NewEncoder(connection).Encode(rootHelperResponse{Error: "invalid collector request"})
		return
	}
	request.Probe = strings.ToLower(strings.TrimSpace(request.Probe))
	if !rootHelperProbes[request.Probe] {
		_ = json.NewEncoder(connection).Encode(rootHelperResponse{Error: "profile is not allowed by the root collector"})
		return
	}
	commands, err := probeCommands(request.Probe, request.DeviceID)
	if err != nil {
		_ = json.NewEncoder(connection).Encode(rootHelperResponse{Error: err.Error()})
		return
	}
	runner := &Runner{timeout: defaultTimeout}
	var result Result
	for _, command := range commands {
		result, err = runner.runCommand(ctx, "host-root", "management-node", request.Probe, command, command.name, command.args...)
		if err == nil {
			break
		}
	}
	response := rootHelperResponse{Result: result}
	if err != nil {
		response.Error = boundedRootError(err)
	}
	_ = json.NewEncoder(connection).Encode(response)
}

func callRootHelper(ctx context.Context, socketPath, probe string, deviceID int) (Result, error) {
	dialer := net.Dialer{Timeout: 5 * time.Second}
	connection, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return Result{}, fmt.Errorf("connect root collector helper: %w", err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(45 * time.Second))
	if err := json.NewEncoder(connection).Encode(rootHelperRequest{Probe: probe, DeviceID: deviceID}); err != nil {
		return Result{}, fmt.Errorf("send root collector request: %w", err)
	}
	var response rootHelperResponse
	if err := json.NewDecoder(bufio.NewReader(connection)).Decode(&response); err != nil {
		return Result{}, fmt.Errorf("read root collector response: %w", err)
	}
	if response.Error != "" {
		return response.Result, fmt.Errorf("root collector: %s", response.Error)
	}
	return response.Result, nil
}

func boundedRootError(err error) string {
	value := err.Error()
	if len(value) > 2048 {
		return value[:2048]
	}
	return value
}
