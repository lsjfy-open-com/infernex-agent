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

// Package output formats and renders check results, printing a human-readable
// terminal report and optionally writing a structured JSON result file.
package output

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"openfuyao/infernex-checker/pkg/types"
)

const (
	iconPass       = "✅"
	iconFail       = "❌"
	iconWarn       = "⚠️"
	iconInfo       = "ℹ️"
	outputFileMode = 0644 // owner read/write, group and others read-only
	iconSkip       = "⏭️"
	iconBlock      = "⛔"
	separator      = "────────────────────────────────"
)

// PrintReport prints the terminal report
func PrintReport(report *types.Report) {
	fmt.Println("\n=== InferNex Checker ===")

	if report.Hardware != nil {
		printHardware(report.Hardware)
	}

	// all-check mode: hardware layer failed, K8s and Config-Env layers were skipped
	if report.HardwareTerminated {
		fmt.Printf("\n%s All-check terminated: no nodes passed hardware layer, "+
			"skipping K8s and Config-Env layers\n", iconBlock)
	}

	if report.K8s != nil {
		printK8s(report.K8s)
	}
	if report.ConfigEnv != nil {
		printConfigEnv(report.ConfigEnv)
	}

	fmt.Println("\n" + separator)
	fmt.Printf("Result: %d passed, %d failed, %d info\n",
		report.Summary.Passed, report.Summary.Failed, report.Summary.Info)

	// collect info items
	var infoItems []string
	collectInfoItems(report, &infoItems)
	if len(infoItems) > 0 {
		fmt.Printf("\n%s Info items:\n", iconInfo)
		for _, item := range infoItems {
			fmt.Printf("  %s\n", item)
		}
	}
}

func printCheck(check types.CheckResult) {
	fmt.Printf("  %s %-4s  %s\n", statusIcon(check.Status), check.ID, check.Message)
	if (check.Status == types.StatusFailed || check.Status == types.StatusWarning ||
		check.Status == types.StatusInfo) && check.Suggestion != "" {
		fmt.Printf("    → %s\n", check.Suggestion)
	}
}

func printHardware(hw *types.HardwareResult) {
	for _, node := range hw.Nodes {
		fmt.Printf("\n[Hardware Layer - Single-Node Check: %s]\n", node.Name)
		for _, check := range node.Checks {
			printCheck(check)
		}
		if node.SkippedHCCS {
			fmt.Printf("  %s H-06~H-08 skipped  %s\n", iconSkip, node.SkipReason)
		}
	}

	if hw.CrossNodes != nil {
		// collect 910-series node names that passed single-node checks
		var passedNames []string
		for _, node := range hw.Nodes {
			if node.Is910Series && node.Status == types.StatusPassed {
				passedNames = append(passedNames, node.Name)
			}
		}
		title := "[Hardware Layer - Cross-Node Check]"
		if len(passedNames) > 0 {
			title += fmt.Sprintf(" (910-series nodes: %s)", strings.Join(passedNames, ", "))
		}
		fmt.Printf("\n%s\n", title)
		for _, check := range hw.CrossNodes {
			printCheck(check)
		}
	}
}

func printK8s(k8s *types.K8sResult) {
	fmt.Printf("\n[K8s Layer]\n")
	for _, check := range k8s.Checks {
		printCheck(check)
	}
	if k8s.Status == types.StatusFailed {
		// find the first failed check to identify what triggered termination
		terminatedBy := ""
		for _, check := range k8s.Checks {
			if check.Status == types.StatusFailed {
				terminatedBy = check.ID
				break
			}
		}
		fmt.Printf("  %s K8s layer terminated: %s failed, skipping subsequent checks\n", iconBlock, terminatedBy)
	}
}

func printConfigEnv(biz *types.ConfigEnvResult) {
	// group output by node
	nodeChecks := map[string][]types.CheckResult{}
	var nodeOrder []string
	for _, check := range biz.Checks {
		node := check.Node
		if _, exists := nodeChecks[node]; !exists {
			nodeOrder = append(nodeOrder, node)
		}
		nodeChecks[node] = append(nodeChecks[node], check)
	}

	for _, node := range nodeOrder {
		if node != "" {
			fmt.Printf("\n[Config-Env Layer: %s]\n", node)
		} else {
			fmt.Printf("\n[Config-Env Layer]\n")
		}
		for _, check := range nodeChecks[node] {
			printCheck(check)
		}
	}
}

func collectInfoItems(report *types.Report, items *[]string) {
	if report.Hardware != nil {
		*items = append(*items, collectHardwareInfoItems(report.Hardware)...)
	}
	if report.K8s != nil {
		*items = append(*items, collectK8sInfoItems(report.K8s)...)
	}
	if report.ConfigEnv != nil {
		*items = append(*items, collectConfigEnvInfoItems(report.ConfigEnv)...)
	}
}

func collectHardwareInfoItems(hw *types.HardwareResult) []string {
	var items []string
	for _, node := range hw.Nodes {
		for _, check := range node.Checks {
			if check.Status == types.StatusInfo {
				items = append(items, fmt.Sprintf("%s  %s  %s", check.ID, node.Name, check.Message))
			}
		}
	}
	return items
}

func collectK8sInfoItems(k8s *types.K8sResult) []string {
	var items []string
	for _, check := range k8s.Checks {
		if check.Status == types.StatusInfo {
			items = append(items, fmt.Sprintf("%s  %s", check.ID, check.Message))
		}
	}
	return items
}

func collectConfigEnvInfoItems(cfg *types.ConfigEnvResult) []string {
	var items []string
	for _, check := range cfg.Checks {
		if check.Status != types.StatusInfo {
			continue
		}
		if check.Node != "" {
			items = append(items, fmt.Sprintf("%s  %s  %s", check.ID, check.Node, check.Message))
		} else {
			items = append(items, fmt.Sprintf("%s  %s", check.ID, check.Message))
		}
	}
	return items
}

// WriteJSON writes the report to a JSON file
func WriteJSON(report *types.Report, path string) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize report: %w", err)
	}
	if err := os.WriteFile(path, data, outputFileMode); err != nil {
		return fmt.Errorf("failed to write JSON file: %w", err)
	}
	return nil
}

// BuildSummary counts check results
func BuildSummary(report *types.Report) types.Summary {
	var s types.Summary
	if report.Hardware != nil {
		for _, node := range report.Hardware.Nodes {
			for _, check := range node.Checks {
				countCheck(check.Status, &s)
			}
		}
		if report.Hardware.CrossNodes != nil {
			for _, check := range report.Hardware.CrossNodes {
				countCheck(check.Status, &s)
			}
		}
	}
	if report.K8s != nil {
		for _, check := range report.K8s.Checks {
			countCheck(check.Status, &s)
		}
	}
	if report.ConfigEnv != nil {
		for _, check := range report.ConfigEnv.Checks {
			countCheck(check.Status, &s)
		}
	}
	s.Total = s.Passed + s.Failed + s.Info
	return s
}

func countCheck(status types.CheckStatus, s *types.Summary) {
	switch status {
	case types.StatusPassed:
		s.Passed++
	case types.StatusFailed:
		s.Failed++
	case types.StatusInfo, types.StatusWarning:
		s.Info++
	default:
	}
}

func statusIcon(status types.CheckStatus) string {
	switch status {
	case types.StatusPassed:
		return iconPass
	case types.StatusFailed:
		return iconFail
	case types.StatusWarning:
		return iconWarn
	case types.StatusInfo:
		return iconInfo
	case types.StatusSkipped:
		return iconSkip
	default:
		return iconInfo
	}
}
