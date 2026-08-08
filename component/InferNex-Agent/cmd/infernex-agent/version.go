/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
)

type versionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	GoVersion string `json:"goVersion"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	CGO       string `json:"cgoEnabled"`
}

func currentVersionInfo() versionInfo {
	info := versionInfo{
		Version:   version,
		Commit:    commit,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		CGO:       "unknown",
	}
	if build, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range build.Settings {
			switch setting.Key {
			case "CGO_ENABLED":
				info.CGO = setting.Value
			case "vcs.revision":
				if strings.TrimSpace(info.Commit) == "" || info.Commit == "unknown" {
					info.Commit = setting.Value
				}
			}
		}
	}
	return info
}

func runVersion(args []string) error {
	flags := flag.NewFlagSet("infernex-agent version", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	jsonOutput := flags.Bool("json", false, "print machine-readable build metadata")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	info := currentVersionInfo()
	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(info)
	}
	fmt.Fprintf(
		os.Stdout,
		"InferNex Agent %s (%s/%s, commit %s, cgo %s)\n",
		info.Version,
		info.OS,
		info.Arch,
		info.Commit,
		info.CGO,
	)
	return nil
}
