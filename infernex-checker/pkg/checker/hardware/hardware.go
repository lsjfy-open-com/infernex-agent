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

// Package hardware implements the Hardware Layer checks (H-01 through H-08),
// covering NPU driver and firmware status, Ascend Device Plugin readiness, NPU
// availability, host file integrity, hccn.conf correctness, intra-node HCCS
// communication, and cross-node RoCE RDMA connectivity.
package hardware

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"openfuyao/infernex-checker/pkg/executor"
	"openfuyao/infernex-checker/pkg/log"
	"openfuyao/infernex-checker/pkg/types"
)

const (
	regexMinMatchLen      = 2               // minimum length of a regex submatch result (full match + 1 group)
	hccnPingPacketCount   = 128             // number of packets used in hccn_tool ping checks
	hccnPingMaxRetries    = 3               // max retry attempts for hccn_tool ping to tolerate transient failures
	hccnPingRetryInterval = 1 * time.Second // wait between ping retries to tolerate transient cold-start failures
	npuStatusGroupIdx     = 2               // regex capture group index for NPU health status field
	npuIDGroupIdx         = 1               // regex capture group index for NPU ID field
	hccnConfSplitN        = 2               // max parts when splitting hccn.conf key=value lines
	crossNodeMinCount     = 2               // minimum number of 910-series nodes required for cross-node check
)

// newSSHRunner is the default factory for creating a CommandRunner from a NodeInfo.
// Tests override this variable to inject a fake runner without a real SSH connection.
var newSSHRunner = executor.NewSSHRunner

// NodeCheckContext holds per-node check context, storing data collected during checks for reuse
type NodeCheckContext struct {
	Node        types.NodeInfo
	Is910Series bool
	PhyIDs      []int             // physical NPU IDs from /dev/davinci*
	HccnIPs     map[string]string // NIC name -> IP
	TLSStates   map[string]string // NIC name -> TLS state
}

// CheckHardware runs full Hardware Layer checks and returns results for all nodes
func CheckHardware(nodes []types.NodeInfo) *types.HardwareResult {
	result := &types.HardwareResult{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	ctxMap := make(map[string]*NodeCheckContext)
	var ctxMu sync.Mutex

	for _, node := range nodes {
		wg.Add(1)
		go func(n types.NodeInfo) {
			defer wg.Done()
			nodeResult, ctx := checkSingleNode(n)

			mu.Lock()
			result.Nodes = append(result.Nodes, nodeResult)
			mu.Unlock()

			if ctx != nil {
				ctxMu.Lock()
				ctxMap[n.Name] = ctx
				ctxMu.Unlock()
			}
		}(node)
	}
	wg.Wait()

	// collect 910-series nodes that passed single-node checks
	// no lock needed here: all goroutines have finished, ctxMap is no longer written to
	var passedCtxs []*NodeCheckContext
	for _, nodeResult := range result.Nodes {
		if nodeResult.Status == types.StatusPassed && nodeResult.Is910Series {
			if ctx, ok := ctxMap[nodeResult.Name]; ok {
				passedCtxs = append(passedCtxs, ctx)
			}
		}
	}

	// H-08 cross-node check
	result.CrossNodes = checkCrossNodes(passedCtxs)

	return result
}

// checkSingleNode runs H-01~H-07 on a single node
func checkSingleNode(node types.NodeInfo) (types.NodeResult, *NodeCheckContext) {
	result := types.NodeResult{
		Name:   node.Name,
		Status: types.StatusPassed,
	}

	runner, cleanup, err := newSSHRunner(node)
	if err != nil {
		log.Error(fmt.Sprintf("node %s SSH connection failed: %v", node.Name, err))
		result.Status = types.StatusFailed
		result.Checks = append(result.Checks, types.CheckResult{
			ID:         "SSH",
			Status:     types.StatusFailed,
			Message:    fmt.Sprintf("SSH connection failed: %v", err),
			Suggestion: "Please check node IP, port, username, and key/password configuration",
		})
		return result, nil
	}
	defer cleanup()

	ctx := &NodeCheckContext{
		Node:      node,
		HccnIPs:   make(map[string]string),
		TLSStates: make(map[string]string),
	}

	// H-01
	h01 := checkH01(runner)
	result.Checks = append(result.Checks, h01)
	if h01.Status == types.StatusFailed {
		result.Status = types.StatusFailed
		return result, nil
	}

	// H-02
	h02 := checkH02(runner, node.Name)
	result.Checks = append(result.Checks, h02)
	if h02.Status == types.StatusFailed {
		result.Status = types.StatusFailed
		return result, nil
	}

	// H-03
	h03, is910 := checkH03(runner)
	result.Checks = append(result.Checks, h03)
	result.Is910Series = is910
	ctx.Is910Series = is910
	if h03.Status == types.StatusFailed {
		result.Status = types.StatusFailed
		return result, nil
	}

	// H-04
	npuSmiOut, _, _ := runner.Run("npu-smi info")
	h04 := checkH04(npuSmiOut)
	result.Checks = append(result.Checks, h04)
	npuTotal, _, _ := parseNPUCount(npuSmiOut)

	// H-05
	h05 := checkH05(runner)
	result.Checks = append(result.Checks, h05)
	if h05.Status == types.StatusFailed {
		result.Status = types.StatusFailed
		return result, nil
	}

	// skip H-06~H-08 for non-910 series
	if !is910 {
		result.SkippedHCCS = true
		result.SkipReason = "non-910 series, H-06~H-08 will be skipped"
		return result, ctx
	}
	return check910SeriesNode(runner, node.Name, npuTotal, result, ctx)
}

// checkH01 checks NPU driver and firmware installation status
func checkH01(ssh executor.CommandRunner) types.CheckResult {
	stdout, _, err := ssh.Run("lsmod | grep drv_pcie_host")
	if err != nil || strings.TrimSpace(stdout) == "" {
		log.Info(fmt.Sprintf("node %s H-01: no lsmod output", ssh.NodeName()))
		return types.CheckResult{
			ID:      "H-01",
			Status:  types.StatusFailed,
			Message: "NPU driver module not detected (no lsmod output)",
			Suggestion: "Please confirm whether the node has an NPU card; if so, " +
				"refer to the Ascend driver/firmware installation guide",
		}
	}

	_, _, err = ssh.Run("npu-smi info")
	if err != nil {
		log.Info(fmt.Sprintf("node %s H-01: npu-smi info failed", ssh.NodeName()))
		return types.CheckResult{
			ID:         "H-01",
			Status:     types.StatusFailed,
			Message:    "Driver module loaded but npu-smi info execution failed",
			Suggestion: "Driver module load anomaly, recommend reinstalling the driver/firmware package",
		}
	}

	return types.CheckResult{
		ID:      "H-01",
		Status:  types.StatusPassed,
		Message: "NPU driver and firmware installed",
	}
}

// checkH02 checks Ascend Device Plugin installation status
func checkH02(ssh executor.CommandRunner, nodeName string) types.CheckResult {
	stdout, _, _ := ssh.Run("kubectl get pods -A -o wide | grep ascend-device-plugin")
	lines := strings.Split(stdout, "\n")
	pluginRunning := false
	for _, line := range lines {
		if strings.Contains(line, nodeName) {
			if strings.Contains(line, "Running") {
				pluginRunning = true
			} else {
				return types.CheckResult{
					ID:         "H-02",
					Status:     types.StatusFailed,
					Message:    "Ascend Device Plugin Pod is not in Running state",
					Suggestion: "Refer to the Ascend Device Plugin installation guide and confirm the DaemonSet is correctly deployed",
				}
			}
		}
	}

	if !pluginRunning {
		return types.CheckResult{
			ID:         "H-02",
			Status:     types.StatusFailed,
			Message:    fmt.Sprintf("Ascend Device Plugin Pod not found on node %s", nodeName),
			Suggestion: "Refer to the Ascend Device Plugin installation guide and confirm the DaemonSet is correctly deployed",
		}
	}

	// check if NPU resource is registered
	stdout, _, err := ssh.Run(fmt.Sprintf("kubectl get node %s -o jsonpath='{.status.capacity}'", nodeName))
	if err != nil || !strings.Contains(stdout, "huawei.com/Ascend") {
		return types.CheckResult{
			ID:     "H-02",
			Status: types.StatusFailed,
			Message: "Ascend Device Plugin is Running but NPU resource not registered " +
				"(no huawei.com/Ascend field in capacity)",
			Suggestion: "Refer to the Ascend Device Plugin installation guide " +
				"and confirm the DaemonSet is correctly deployed",
		}
	}

	return types.CheckResult{
		ID:      "H-02",
		Status:  types.StatusPassed,
		Message: "Ascend Device Plugin Running and NPU resource registered",
	}
}

// checkH03 checks NPU model; returns check result and whether it is 910 series
func checkH03(ssh executor.CommandRunner) (types.CheckResult, bool) {
	stdout, _, err := ssh.Run("npu-smi info")
	if err != nil {
		return types.CheckResult{
			ID:         "H-03",
			Status:     types.StatusFailed,
			Message:    "npu-smi info execution failed, unable to get NPU model",
			Suggestion: "Please confirm npu-smi tool is available",
		}, false
	}

	model := parseNPUModel(stdout)
	if model == "" {
		return types.CheckResult{
			ID:         "H-03",
			Status:     types.StatusFailed,
			Message:    "Unable to parse NPU model from npu-smi info output",
			Suggestion: "Please confirm npu-smi tool is available and output format is correct",
		}, false
	}

	is910 := strings.Contains(model, "910")
	if !is910 {
		return types.CheckResult{
			ID:      "H-03",
			Status:  types.StatusWarning,
			Message: fmt.Sprintf("NPU model is non-910 series: %s", model),
			Suggestion: "The pre-flight checks are designed for 910 series; " +
				"non-910 series may have different check items and H-06~H-08 will be skipped",
		}, false
	}

	return types.CheckResult{
		ID:      "H-03",
		Status:  types.StatusPassed,
		Message: fmt.Sprintf("NPU model: %s", model),
	}, true
}

// parseNPUModel extracts the NPU model name from npu-smi info output.
// Matches lines like: | 0     910B4               | OK  ...
// Returns the model name of the first card found.
func parseNPUModel(output string) string {
	// strip ANSI escape sequences
	ansiEscape := regexp.MustCompile(`\x1B(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])`)
	clean := ansiEscape.ReplaceAllString(output, "")

	// match "| <id>  <model> | OK/Warning/Alarm/Critical/UNKNOWN ..."
	re := regexp.MustCompile(`\|\s+\d+\s+(\S+)\s+\|\s+(OK|Warning|Alarm|Critical|UNKNOWN)`)
	m := re.FindStringSubmatch(clean)
	if len(m) >= regexMinMatchLen {
		return m[npuIDGroupIdx]
	}
	return ""
}

// checkH04 reports NPU available count (info only) using pre-fetched npu-smi output
func checkH04(stdout string) types.CheckResult {
	if stdout == "" {
		return types.CheckResult{
			ID:      "H-04",
			Status:  types.StatusInfo,
			Message: "Unable to get NPU available count",
		}
	}

	total, available, unhealthy := parseNPUCount(stdout)
	msg := fmt.Sprintf("NPU available: %d/%d", available, total)
	if len(unhealthy) > 0 {
		msg += fmt.Sprintf(", unhealthy: %s", strings.Join(unhealthy, ", "))
	}
	detail := map[string]interface{}{
		"available": available,
		"total":     total,
		"unhealthy": unhealthy,
	}

	if available == 0 {
		return types.CheckResult{
			ID:         "H-04",
			Status:     types.StatusInfo,
			Message:    msg + ", no NPU cards currently available",
			Detail:     detail,
			Suggestion: "Consider releasing occupied resources or adjusting workloads if insufficient",
		}
	}

	return types.CheckResult{
		ID:      "H-04",
		Status:  types.StatusInfo,
		Message: msg,
		Detail:  detail,
	}
}

// parseNPUCount parses total and available NPU card counts from npu-smi info output.
// Total is counted from device rows: | <id>  <model> | OK/Warning/Alarm/Critical/UNKNOWN ... |
// Occupied is determined from process rows: | <npu> <chip> | <pid> | <name> | <mem> |
func parseNPUCount(output string) (total, available int, unhealthy []string) {
	// strip ANSI escape sequences
	ansiEscape := regexp.MustCompile(`\x1B(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])`)
	clean := ansiEscape.ReplaceAllString(output, "")

	// count total NPUs and detect unhealthy ones
	deviceRe := regexp.MustCompile(`\|\s+(\d+)\s+\S+\s+\|\s+(OK|Warning|Alarm|Critical|UNKNOWN)`)
	deviceMatches := deviceRe.FindAllStringSubmatch(clean, -1)
	for _, m := range deviceMatches {
		total++
		if m[npuStatusGroupIdx] != "OK" {
			unhealthy = append(unhealthy, fmt.Sprintf("NPU%s(%s)", m[npuIDGroupIdx], m[npuStatusGroupIdx]))
		}
	}

	// count occupied NPUs from process table: "| <npu> <chip> | <pid> | <name> | <mem> |"
	processRe := regexp.MustCompile(`\|\s+(\d+)\s+\d+\s+\|\s+(\d+)\s+\|\s+\S+\s+\|\s+\d+\s+\|`)
	matches := processRe.FindAllStringSubmatch(clean, -1)
	occupiedIDs := map[string]bool{}
	for _, m := range matches {
		occupiedIDs[m[1]] = true
	}

	available = total - len(occupiedIDs) - len(unhealthy)
	if available < 0 {
		available = 0
	}
	return
}

// checkH05 checks whether key host files and directories exist
func checkH05(ssh executor.CommandRunner) types.CheckResult {
	paths := []string{
		"/usr/local/dcmi",
		"/usr/local/Ascend/driver/lib64",
		"/usr/local/bin/npu-smi",
		"/usr/local/Ascend/driver/version.info",
		"/etc/ascend_install.info",
		"/etc/hccn.conf",
	}

	var missing []string
	for _, p := range paths {
		_, _, err := ssh.Run(fmt.Sprintf("ls %s", p))
		if err != nil {
			missing = append(missing, p)
		}
	}

	if len(missing) > 0 {
		return types.CheckResult{
			ID:         "H-05",
			Status:     types.StatusFailed,
			Message:    fmt.Sprintf("Missing paths or files: %s", strings.Join(missing, ", ")),
			Suggestion: "Please verify environment completeness and supply the missing files or directories",
		}
	}

	return types.CheckResult{
		ID:      "H-05",
		Status:  types.StatusPassed,
		Message: "Key host files and directories are complete",
	}
}

func check910SeriesNode(ssh executor.CommandRunner,
	nodeName string, npuTotal int,
	result types.NodeResult, ctx *NodeCheckContext) (types.NodeResult, *NodeCheckContext) {
	phyIDs, err := getPhyIDs(ssh)
	if err != nil || len(phyIDs) == 0 {
		log.Error(fmt.Sprintf("node %s failed to get NPU IDs: %v", nodeName, err))
		result.Status = types.StatusFailed
		result.Checks = append(result.Checks, types.CheckResult{
			ID:         "H-06",
			Status:     types.StatusFailed,
			Message:    "Failed to get NPU physical IDs from /dev/davinci*",
			Suggestion: "Please confirm NPU devices are present and driver is loaded",
		})
		return result, nil
	}
	if npuTotal > 0 && len(phyIDs) != npuTotal {
		result.Status = types.StatusFailed
		result.Checks = append(result.Checks, types.CheckResult{
			ID:     "H-06",
			Status: types.StatusFailed,
			Message: fmt.Sprintf("NPU device file count mismatch: "+
				"npu-smi reports %d cards but /dev/davinci* found %d IDs %v",
				npuTotal, len(phyIDs), phyIDs),
			Suggestion: "Please check driver installation and device file integrity",
		})
		return result, nil
	}
	ctx.PhyIDs = phyIDs

	// H-06
	h06, hccnIPs := checkH06(ssh, phyIDs)
	result.Checks = append(result.Checks, h06)
	if h06.Status == types.StatusFailed {
		result.Status = types.StatusFailed
		return result, nil
	}
	ctx.HccnIPs = hccnIPs

	// H-07
	h07, tlsStates := checkH07(ssh, phyIDs, hccnIPs)
	result.Checks = append(result.Checks, h07...)
	ctx.TLSStates = tlsStates
	for _, c := range h07 {
		if c.Status == types.StatusFailed {
			result.Status = types.StatusFailed
			return result, nil
		}
	}
	return result, ctx
}

// getPhyIDs returns the list of NPU physical IDs from /dev/davinci* devices,
// excluding /dev/davinci_manager and similar non-numeric entries.
func getPhyIDs(ssh executor.CommandRunner) ([]int, error) {
	stdout, _, err := ssh.Run("ls /dev/davinci*")
	if err != nil {
		return nil, fmt.Errorf("failed to list /dev/davinci*: %w", err)
	}
	re := regexp.MustCompile(`/dev/davinci(\d+)$`)
	var ids []int
	for _, field := range strings.Fields(stdout) {
		m := re.FindStringSubmatch(field)
		if m != nil {
			id, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// checkH06 checks hccn.conf configuration correctness; returns check result and parsed IP map
func checkH06(ssh executor.CommandRunner, phyIDs []int) (types.CheckResult, map[string]string) {
	stdout, _, err := ssh.Run("cat /etc/hccn.conf")
	if err != nil {
		return types.CheckResult{
			ID:         "H-06",
			Status:     types.StatusFailed,
			Message:    "Failed to read /etc/hccn.conf",
			Suggestion: "Please confirm the file exists and is readable",
		}, nil
	}

	ipMap, maskMap := parseHccnConf(stdout)

	// step 1: IP address completeness — each NPU ID must have a corresponding address entry
	if r := checkH06MissingIPs(phyIDs, ipMap); r.Status == types.StatusFailed {
		return r, nil
	}

	// step 2: netmask completeness — each NPU ID must have a corresponding netmask entry
	if r := checkH06MissingMasks(phyIDs, maskMap); r.Status == types.StatusFailed {
		return r, nil
	}

	// step 3: IP format and uniqueness
	if r := checkH06IPFormatAndUniqueness(ipMap); r.Status == types.StatusFailed {
		return r, nil
	}

	// step 4: subnet consistency using IP + netmask
	if r := checkH06SubnetConsistency(phyIDs, ipMap, maskMap); r.Status == types.StatusFailed {
		return r, nil
	}

	return types.CheckResult{
		ID:      "H-06",
		Status:  types.StatusPassed,
		Message: "hccn.conf configuration is correct",
	}, ipMap
}

// parseHccnConf parses hccn.conf and returns a NIC id -> IP map
// parseHccnConf parses hccn.conf and returns a NIC id -> IP map and a NIC id -> netmask map
func parseHccnConf(content string) (ipMap, maskMap map[string]string) {
	ipMap = map[string]string{}
	maskMap = map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		parts := strings.SplitN(line, "=", hccnConfSplitN)
		if len(parts) != hccnConfSplitN {
			continue
		}
		val := strings.TrimSpace(parts[1])
		if strings.HasPrefix(line, "address_") {
			id := strings.TrimPrefix(parts[0], "address_")
			ipMap[id] = val
		} else if strings.HasPrefix(line, "netmask_") {
			id := strings.TrimPrefix(parts[0], "netmask_")
			maskMap[id] = val
		} else {
			// other fields in hccn.conf are not used, skip
		}
	}
	return
}

func checkH06MissingIPs(phyIDs []int, ipMap map[string]string) types.CheckResult {
	var missingIPs []string
	for _, i := range phyIDs {
		id := strconv.Itoa(i)
		if ip, ok := ipMap[id]; !ok || ip == "" {
			missingIPs = append(missingIPs, fmt.Sprintf("address_%d", i))
		}
	}
	if len(missingIPs) > 0 {
		return types.CheckResult{
			ID:     "H-06",
			Status: types.StatusFailed,
			Message: fmt.Sprintf("Missing IP configuration for: %s",
				strings.Join(missingIPs, ", ")),
			Suggestion: "Add missing IP configuration for the listed NICs",
		}
	}
	return types.CheckResult{}
}

func checkH06MissingMasks(phyIDs []int, maskMap map[string]string) types.CheckResult {
	var missingMasks []string
	for _, i := range phyIDs {
		id := strconv.Itoa(i)
		if mask, ok := maskMap[id]; !ok || mask == "" {
			missingMasks = append(missingMasks, fmt.Sprintf("netmask_%d", i))
		}
	}
	if len(missingMasks) > 0 {
		return types.CheckResult{
			ID:     "H-06",
			Status: types.StatusFailed,
			Message: fmt.Sprintf("Missing netmask configuration for: %s",
				strings.Join(missingMasks, ", ")),
			Suggestion: "Add missing netmask configuration for the listed NICs",
		}
	}
	return types.CheckResult{}
}

func checkH06IPFormatAndUniqueness(ipMap map[string]string) types.CheckResult {
	seen := map[string]string{}
	for id, ip := range ipMap {
		if net.ParseIP(ip) == nil {
			return types.CheckResult{
				ID:         "H-06",
				Status:     types.StatusFailed,
				Message:    fmt.Sprintf("NIC address_%s has an invalid IP format: %s", id, ip),
				Suggestion: "Fix the malformed IP address",
			}
		}
		if prev, dup := seen[ip]; dup {
			return types.CheckResult{
				ID:         "H-06",
				Status:     types.StatusFailed,
				Message:    fmt.Sprintf("address_%s and address_%s have duplicate IP: %s", id, prev, ip),
				Suggestion: "Resolve NIC IP conflict and ensure each NIC has a unique IP",
			}
		}
		seen[ip] = id
	}
	return types.CheckResult{}
}

func checkH06SubnetConsistency(phyIDs []int, ipMap, maskMap map[string]string) types.CheckResult {
	var networks []string
	var networkNICs []string
	for _, i := range phyIDs {
		id := strconv.Itoa(i)
		ip := net.ParseIP(ipMap[id]).To4()
		mask := net.IPMask(net.ParseIP(maskMap[id]).To4())
		if ip == nil || mask == nil {
			continue
		}
		network := ip.Mask(mask).String()
		networks = append(networks, network)
		networkNICs = append(networkNICs, fmt.Sprintf("address_%s(%s)", id, network))
	}
	if len(networks) > 1 {
		first := networks[0]
		for _, n := range networks[1:] {
			if n != first {
				return types.CheckResult{
					ID:     "H-06",
					Status: types.StatusFailed,
					Message: fmt.Sprintf("NICs are not on the same subnet: %s",
						strings.Join(networkNICs, ", ")),
					Suggestion: "Unify IP planning to ensure all NICs are on the same subnet",
				}
			}
		}
	}
	return types.CheckResult{}
}

// checkH07 checks single-node HCCS communication; returns check results and TLS state map
func checkH07(ssh executor.CommandRunner,
	phyIDs []int,
	ipMap map[string]string) ([]types.CheckResult, map[string]string) {
	// step 1: TLS switch state consistency
	r1, tlsStates := checkH07TLSConsistency(ssh, phyIDs)
	if r1.Status == types.StatusFailed {
		return []types.CheckResult{r1}, tlsStates
	}

	// step 2: NIC-to-NIC connectivity
	r2 := checkH07Connectivity(ssh, phyIDs, ipMap)
	return []types.CheckResult{r1, r2}, tlsStates

}

func checkH07TLSConsistency(ssh executor.CommandRunner, phyIDs []int) (types.CheckResult, map[string]string) {
	tlsStates := map[string]string{}

	// step 1: TLS switch state consistency
	firstTLS := ""
	var inconsistent []string
	for _, i := range phyIDs {
		out, stderr, err := ssh.Run(fmt.Sprintf("hccn_tool -i %d -tls -g", i))
		if err != nil && !strings.Contains(strings.ToLower(stderr), "no certificate found") {
			return types.CheckResult{
				ID:         "H-07",
				Status:     types.StatusFailed,
				Message:    fmt.Sprintf("Step 1: Failed to get TLS state for NPU%d: %v", i, err),
				Suggestion: "Please check hccn_tool availability and NPU status",
			}, tlsStates
		}
		state := parseTLSState(out + stderr)
		tlsStates[strconv.Itoa(i)] = state
		if firstTLS == "" {
			firstTLS = state
		} else if normalizeTLS(state) != normalizeTLS(firstTLS) {
			inconsistent = append(inconsistent, fmt.Sprintf("NPU%d=%s", i, state))
		} else {
			// TLS state is consistent with the first, no action needed
		}
	}

	if len(inconsistent) > 0 {
		return types.CheckResult{
			ID:     "H-07",
			Status: types.StatusFailed,
			Message: fmt.Sprintf("NIC TLS switch states are inconsistent: %s",
				strings.Join(inconsistent, ", ")),
			Suggestion: "Unify TLS switch configuration across all NICs",
		}, tlsStates
	}

	return types.CheckResult{
		ID:      "H-07",
		Status:  types.StatusPassed,
		Message: fmt.Sprintf("Step 1: NIC TLS switch states are consistent (%s)", firstTLS),
	}, tlsStates
}

func parseTLSState(output string) string {
	lower := strings.ToLower(output)
	if strings.Contains(lower, "no certificate found") {
		return "no_cert"
	}
	re := regexp.MustCompile(`tls switch\[(\d+)]`)
	m := re.FindStringSubmatch(output)
	if len(m) >= regexMinMatchLen {
		if m[1] == "1" {
			return "enable"
		}
		return "disable"
	}
	return "unknown"
}

func pingWithRetry(ssh executor.CommandRunner, phyID int, targetIP string) bool {
	for attempt := 0; attempt < hccnPingMaxRetries; attempt++ {
		stdout, _, err := ssh.Run(fmt.Sprintf("hccn_tool -i %d -ping -g address %s pkt %d",
			phyID, targetIP, hccnPingPacketCount))
		pingErr := err != nil ||
			strings.Contains(stdout, "Command execute failed") ||
			strings.Contains(stdout, "100.00% packet loss")
		if !pingErr {
			return true
		}
		if attempt < hccnPingMaxRetries-1 {
			time.Sleep(hccnPingRetryInterval)
		}
	}
	return false
}

func checkH07Connectivity(ssh executor.CommandRunner, phyIDs []int, ipMap map[string]string) types.CheckResult {
	var pingFailed []string
	for _, i := range phyIDs {
		for nic, ip := range ipMap {
			if ip == "" || nic == strconv.Itoa(i) {
				continue
			}
			if !pingWithRetry(ssh, i, ip) {
				pingFailed = append(pingFailed, fmt.Sprintf("NPU%d->%s(%s)", i, nic, ip))
			}
		}
	}

	if len(pingFailed) > 0 {
		return types.CheckResult{
			ID:     "H-07",
			Status: types.StatusFailed,
			Message: fmt.Sprintf("Step 2: The following NIC pairs failed ping: %s",
				strings.Join(pingFailed, ", ")),
			Suggestion: "Please check: whether port link is UP, whether optical modules are present, " +
				"whether configured IPs conflict with the intra-board communication subnet",
		}
	}

	return types.CheckResult{
		ID:      "H-07",
		Status:  types.StatusPassed,
		Message: "Step 2: Single-node NIC-to-NIC connectivity is normal",
	}
}

func normalizeTLS(state string) string {
	if state == "no_cert" {
		return "disable"
	}
	return state
}

// checkCrossNodes H-08 cross-node NIC-side RoCE RDMA communication
func checkCrossNodes(ctxs []*NodeCheckContext) []types.CheckResult {
	if len(ctxs) < crossNodeMinCount {
		return []types.CheckResult{{
			ID:     "H-08",
			Status: types.StatusSkipped,
			Message: fmt.Sprintf("Only %d 910-series node(s) passed single-node checks, "+
				"skipping cross-node check (at least %d required)", len(ctxs), crossNodeMinCount),
		}}
	}

	// step 1: cross-node IP uniqueness
	r1 := crossNodeIPUniqueness(ctxs)
	if r1.Status == types.StatusFailed {
		return []types.CheckResult{r1}
	}

	// step 2: cross-node TLS consistency
	r2 := crossNodeTLSConsistency(ctxs)
	if r2.Status == types.StatusFailed {
		return []types.CheckResult{r1, r2}
	}

	// step 3: cross-node connectivity
	return []types.CheckResult{r1, r2, crossNodeConnectivity(ctxs)}
}

func crossNodeIPUniqueness(ctxs []*NodeCheckContext) types.CheckResult {
	globalIPs := map[string]string{} // ip -> "nodeName NICid"
	var dupIPs []string
	for _, ctx := range ctxs {
		for nic, ip := range ctx.HccnIPs {
			key := fmt.Sprintf("%s NIC%s", ctx.Node.Name, nic)
			if prev, dup := globalIPs[ip]; dup {
				dupIPs = append(dupIPs, fmt.Sprintf("%s and %s share IP %s", prev, key, ip))
			} else {
				globalIPs[ip] = key
			}
		}
	}
	if len(dupIPs) > 0 {
		return types.CheckResult{
			ID:         "H-08",
			Status:     types.StatusFailed,
			Message:    fmt.Sprintf("Step 1: Duplicate IPs across nodes: %s", strings.Join(dupIPs, "; ")),
			Suggestion: "Re-plan NIC IPs across all nodes to ensure global uniqueness",
		}
	}
	return types.CheckResult{
		ID:      "H-08",
		Status:  types.StatusPassed,
		Message: "Step 1: Cross-node NIC IPs are unique",
	}
}

func crossNodeTLSConsistency(ctxs []*NodeCheckContext) types.CheckResult {
	firstTLS := ""
	firstTLSLabel := ""
	var inconsistentTLS []string
	for _, ctx := range ctxs {
		for id, state := range ctx.TLSStates {
			label := fmt.Sprintf("%s NPU%s=%s", ctx.Node.Name, id, state)
			if firstTLS == "" {
				firstTLS = state
				firstTLSLabel = label
			} else if normalizeTLS(state) != normalizeTLS(firstTLS) {
				inconsistentTLS = append(inconsistentTLS, fmt.Sprintf("%s (expected %s)", label, firstTLSLabel))
			} else {
				// TLS state is consistent with the first, no action needed
			}
		}
	}
	if len(inconsistentTLS) > 0 {
		return types.CheckResult{
			ID:     "H-08",
			Status: types.StatusFailed,
			Message: fmt.Sprintf("Step 2: Cross-node TLS configuration inconsistent: %s",
				strings.Join(inconsistentTLS, "; ")),
			Suggestion: "Unify TLS switch configuration across all nodes",
		}
	}
	return types.CheckResult{
		ID:      "H-08",
		Status:  types.StatusPassed,
		Message: fmt.Sprintf("Step 2: Cross-node TLS configuration is consistent (%s)", firstTLS),
	}
}

func crossNodePingOne(src *NodeCheckContext, ctxs []*NodeCheckContext) []string {
	runner, cleanup, err := newSSHRunner(src.Node)
	if err != nil {
		log.Error(fmt.Sprintf("H-08 failed to connect to node %s: %v", src.Node.Name, err))
		return nil
	}
	defer cleanup()

	var failed []string
	for _, dstCtx := range ctxs {
		if dstCtx.Node.Name == src.Node.Name {
			continue
		}
		failed = append(failed, pingToDestination(runner, src, dstCtx)...)
	}
	return failed
}

type pingPath struct {
	srcName  string
	srcPhyID int
	dstName  string
	dstNic   string
	dstIP    string
}

func pingToDestination(ssh executor.CommandRunner, src *NodeCheckContext, dstCtx *NodeCheckContext) []string {
	var failed []string
	for dstNic, dstIP := range dstCtx.HccnIPs {
		for _, i := range src.PhyIDs {
			if result := pingOnePath(ssh, pingPath{
				srcName:  src.Node.Name,
				srcPhyID: i,
				dstName:  dstCtx.Node.Name,
				dstNic:   dstNic,
				dstIP:    dstIP,
			}); result != "" {
				failed = append(failed, result)
			}
		}
	}
	return failed
}

func pingOnePath(ssh executor.CommandRunner, path pingPath) string {
	for attempt := 0; attempt < hccnPingMaxRetries; attempt++ {
		stdout, _, err := ssh.Run(fmt.Sprintf("hccn_tool -i %d -ping -g address %s pkt %d",
			path.srcPhyID, path.dstIP, hccnPingPacketCount))
		pingFailed := err != nil ||
			strings.Contains(stdout, "Command execute failed") ||
			strings.Contains(stdout, "100.00% packet loss")
		if !pingFailed {
			return ""
		}
		if attempt < hccnPingMaxRetries-1 {
			// wait before retrying to tolerate transient RoCE/HCCS link fluctuations
			time.Sleep(hccnPingRetryInterval)
		}
	}
	return fmt.Sprintf("%s:NPU%d -> %s:%s(%s)",
		path.srcName, path.srcPhyID, path.dstName, path.dstNic, path.dstIP)
}

func crossNodeConnectivity(ctxs []*NodeCheckContext) types.CheckResult {
	var pingFailed []string
	for _, srcCtx := range ctxs {
		pingFailed = append(pingFailed, crossNodePingOne(srcCtx, ctxs)...)
	}
	if len(pingFailed) > 0 {
		return types.CheckResult{
			ID:     "H-08",
			Status: types.StatusFailed,
			Message: fmt.Sprintf("Step 3: Cross-node connectivity anomaly: "+
				"%s", strings.Join(pingFailed, "; ")),
			Suggestion: "Please check RoCE network configuration, physical links, and switch settings",
		}
	}
	return types.CheckResult{
		ID:      "H-08",
		Status:  types.StatusPassed,
		Message: "Step 3: Communication between all nodes is normal",
	}
}
