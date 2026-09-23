/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

// Package delegation protects the deliberately restricted MCP endpoint used
// by independently developed diagnostic subagents.
package delegation

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
)

const maxTokenBytes = 4096

// ReadBearerToken reads one protected opaque token. It deliberately refuses
// multiline or short credentials so a configuration file cannot accidentally
// become the credential.
func ReadBearerToken(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("diagnostic subagent token file is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat diagnostic subagent token: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxTokenBytes {
		return "", fmt.Errorf("diagnostic subagent token must be a regular file no larger than %d bytes", maxTokenBytes)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 {
		return "", fmt.Errorf("diagnostic subagent token file must not be group- or world-writable")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read diagnostic subagent token: %w", err)
	}
	token := strings.TrimRight(string(payload), "\r\n")
	if len(token) < 32 || strings.ContainsAny(token, "\r\n\x00") {
		return "", fmt.Errorf("diagnostic subagent token must be a single opaque line of at least 32 characters")
	}
	return token, nil
}

// Protect requires a bearer token and rejects excess concurrent requests
// instead of allowing an external subagent to exhaust the main Agent.
func Protect(next http.Handler, token string, maxConcurrent int) (http.Handler, error) {
	if next == nil || len(token) < 32 {
		return nil, fmt.Errorf("handler and a bearer token of at least 32 characters are required")
	}
	if maxConcurrent < 1 || maxConcurrent > 64 {
		return nil, fmt.Errorf("diagnostic subagent concurrency must be between 1 and 64")
	}
	semaphore := make(chan struct{}, maxConcurrent)
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		provided := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		if len(provided) != len(token) || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			response.Header().Set("WWW-Authenticate", `Bearer realm="infernex-diagnostic-subagent"`)
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		select {
		case semaphore <- struct{}{}:
			defer func() { <-semaphore }()
		default:
			response.Header().Set("Retry-After", "1")
			http.Error(response, "diagnostic delegation concurrency budget exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(response, request)
	}), nil
}
