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
	"path/filepath"

	infernexskills "gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/skills"
)

const defaultSkillDirectories = "/opt/infernex-agent/skills,/etc/infernex-agent/skills.d"

func runSkills(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: infernex-agent skills list|validate")
	}
	switch args[0] {
	case "list":
		flags := flag.NewFlagSet("infernex-agent skills list", flag.ContinueOnError)
		directories := flags.String("directories", defaultSkillDirectories, "comma-separated Skill roots")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		registry, err := infernexskills.NewRegistry(parsePathList(*directories))
		if err != nil {
			return err
		}
		return writeSkillsJSON(registry.List())
	case "validate":
		flags := flag.NewFlagSet("infernex-agent skills validate", flag.ContinueOnError)
		path := flags.String("path", "", "Skill directory containing SKILL.md")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *path == "" {
			return fmt.Errorf("--path is required")
		}
		skill, err := infernexskills.Load(*path, "validation")
		if err != nil {
			return err
		}
		skillPath, err := filepath.Abs(*path)
		if err != nil {
			return err
		}
		return writeSkillsJSON(map[string]any{"valid": true, "path": skillPath, "skill": skill})
	default:
		return fmt.Errorf("unknown skills command %q; use list or validate", args[0])
	}
}

func writeSkillsJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
