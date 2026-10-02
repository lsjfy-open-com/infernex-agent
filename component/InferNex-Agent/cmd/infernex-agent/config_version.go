/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 * You can use this software according to the terms and conditions of the Mulan PSL v2.
 * You may obtain a copy of Mulan PSL v2 at:
 *          http://license.coscl.org.cn/MulanPSL2
 * THIS SOFTWARE IS PROVIDED ON AN "AS IS" BASIS, WITHOUT WARRANTIES OF ANY KIND,
 * EITHER EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO NON-INFRINGEMENT,
 * MERCHANTABILITY OR FIT FOR A PARTICULAR PURPOSE.
 */

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/configversion"
)

func runConfigVersion(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: config-version <record|show|list|verify> [flags]")
	}
	action := args[0]
	if action == "help" || action == "--help" || action == "-h" {
		fmt.Fprintln(os.Stdout, "usage: config-version <record|show|list|verify> [flags]; use subcommand --help for flags")
		return nil
	}
	fs := flag.NewFlagSet("config-version "+action, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	state := fs.String("state-dir", configversion.DefaultStateDir, "protected local record directory")
	switch action {
	case "record":
		name := fs.String("name", "config-version", "lowercase name (letters, digits, hyphens)")
		repo := fs.String("repo", "", "local Git repository")
		ref := fs.String("ref", "HEAD", "local Git commit/ref")
		file := fs.String("file", "", "relative path in commit")
		before := fs.String("before", "", "before ClusterSnapshot JSON")
		after := fs.String("after", "", "after ClusterSnapshot JSON")
		experiment := fs.String("experiment", "", "optional experiment evidence file (SHA256 binding only)")
		stage := fs.String("stage", "", "optional stage evidence file (SHA256 binding only)")
		change := fs.String("change", "", "optional change evidence file (SHA256 binding only)")
		slo := fs.String("slo", "", "optional SLO evidence file (SHA256 binding only)")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("unexpected arguments: %v", fs.Args())
		}
		if *repo == "" || *file == "" || *before == "" || *after == "" {
			return errors.New("--repo, --file, --before and --after are required")
		}
		evidence := map[string]string{"experiment": *experiment, "stage": *stage, "change": *change, "slo": *slo}
		r, err := configversion.RecordVersion(configversion.Input{StateDir: *state, Name: *name, Repo: *repo, Ref: *ref, File: *file, Before: *before, After: *after, Evidence: evidence})
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(r)
	case "show", "verify", "list":
		id := fs.String("id", "", "version ID for show or verify")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("unexpected arguments: %v", fs.Args())
		}
		if action == "list" {
			if *id != "" {
				return errors.New("--id is not valid for list")
			}
			r, err := configversion.List(*state)
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(r)
		}
		if *id == "" {
			return errors.New("--id is required")
		}
		if action == "show" {
			r, err := configversion.Show(*state, *id)
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(r)
		}
		r, err := configversion.Verify(*state, *id)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			Verified bool                 `json:"verified"`
			Record   configversion.Record `json:"record"`
		}{true, r})
	default:
		return fmt.Errorf("unknown config-version action %q", action)
	}
}
