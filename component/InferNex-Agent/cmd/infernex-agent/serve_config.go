/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const maxAgentConfigBytes = 1024 * 1024

func mergeServerConfigArgs(args []string) ([]string, string, error) {
	configPath, err := optionValue(args, "--config")
	if err != nil || configPath == "" {
		return args, configPath, err
	}
	configArgs, err := readAgentArgumentFile(configPath)
	if err != nil {
		return nil, configPath, err
	}
	merged := make([]string, 0, len(configArgs)+len(args))
	merged = append(merged, configArgs...)
	merged = append(merged, args...)
	return merged, configPath, nil
}

func optionValue(args []string, name string) (string, error) {
	value := ""
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == name:
			if index+1 >= len(args) {
				return "", fmt.Errorf("%s requires a value", name)
			}
			index++
			value = args[index]
		case strings.HasPrefix(argument, name+"="):
			value = strings.TrimPrefix(argument, name+"=")
		}
	}
	return strings.TrimSpace(value), nil
}

func readAgentArgumentFile(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open Agent configuration %s: %w", path, err)
	}
	defer file.Close()

	arguments := make([]string, 0, 32)
	scanner := bufio.NewScanner(io.LimitReader(file, maxAgentConfigBytes+1))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "--") || strings.ContainsAny(line, "\r\n\x00") {
			return nil, fmt.Errorf("Agent configuration %s contains an invalid argument", path)
		}
		arguments = append(arguments, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Agent configuration %s: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat Agent configuration %s: %w", path, err)
	}
	if info.Size() > maxAgentConfigBytes {
		return nil, fmt.Errorf("Agent configuration %s exceeds %d bytes", path, maxAgentConfigBytes)
	}
	if len(arguments) == 0 {
		return nil, errors.New("Agent configuration contains no arguments")
	}
	return arguments, nil
}
