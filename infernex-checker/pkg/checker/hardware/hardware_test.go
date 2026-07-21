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

package hardware

import (
	"errors"
	"fmt"
	"testing"

	"openfuyao/infernex-checker/pkg/executor"
	"openfuyao/infernex-checker/pkg/types"
)

// mockRunner is a fake CommandRunner for unit tests.
type mockRunner struct {
	nodeName string
	// responses maps command substring -> (stdout, stderr, err)
	responses map[string]mockResponse
}

type mockResponse struct {
	stdout string
	stderr string
	err    error
}

func (m *mockRunner) Run(cmd string) (string, string, error) {
	for key, resp := range m.responses {
		if key == cmd {
			return resp.stdout, resp.stderr, resp.err
		}
	}
	// fallback: check if cmd contains key as substring
	for key, resp := range m.responses {
		if len(key) > 0 && contains(cmd, key) {
			return resp.stdout, resp.stderr, resp.err
		}
	}
	return "", "", nil
}

func (m *mockRunner) NodeName() string {
	return m.nodeName
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (s == sub || findSubstring(s, sub))
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// npuSmiOutput is a realistic npu-smi info output snippet used across tests.
const npuSmiOutput = `+-------------------------------------------------------------------------------------------+
| npu-smi 23.0.rc2                 Version: 23.0.rc2                                       |
+-------------------------------------------------------------------------------------------+
| NPU   Name                  Health   Power(W)   Temp(C)   Hugepages-Usage(page)   AICore |
| Chip                         Bus-Id        AIServer(MHz)                                  |
+==========================================================================================+
| 0     910B4               | OK      | 67.3      | 43       | 0      / 0              | 0  |
| 0     0x0000:C1:00.0       | 1800                                                        |
+-------------------------------------------------------------------------------------------+
| 1     910B4               | OK      | 65.1      | 42       | 0      / 0              | 0  |
| 0     0x0000:81:00.0       | 1800                                                        |
+-------------------------------------------------------------------------------------------+
+-------------------------------------------------------------------------------------------+
| NPU     Chip   Process id   Process name         Device memory(MB)                        |
+==========================================================================================+
+-------------------------------------------------------------------------------------------+
`

const npuSmiOccupiedOutput = `+-------------------------------------------------------------------------------------------+
| 0     910B4               | OK      | 67.3      | 43       | 0      / 0              | 0  |
| 0     0x0000:C1:00.0       | 1800                                                        |
+-------------------------------------------------------------------------------------------+
| 1     910B4               | OK      | 65.1      | 42       | 0      / 0              | 0  |
| 0     0x0000:81:00.0       | 1800                                                        |
+-------------------------------------------------------------------------------------------+
+-------------------------------------------------------------------------------------------+
| NPU     Chip   Process id   Process name         Device memory(MB)                        |
+==========================================================================================+
| 0     0      | 12345     | python              | 4096                                    |
+-------------------------------------------------------------------------------------------+
`

const npuSmiUnhealthyOutput = `+-------------------------------------------------------------------------------------------+
| 0     910B4               | Warning | 67.3      | 43       | 0      / 0              | 0  |
| 0     0x0000:C1:00.0       | 1800                                                        |
+-------------------------------------------------------------------------------------------+
| 1     910B4               | OK      | 65.1      | 42       | 0      / 0              | 0  |
| 0     0x0000:81:00.0       | 1800                                                        |
+-------------------------------------------------------------------------------------------+
`

// ---- parseNPUModel ----

func TestParseNPUModel_Found(t *testing.T) {
	model := parseNPUModel(npuSmiOutput)
	if model != "910B4" {
		t.Errorf("expected 910B4, got %q", model)
	}
}

func TestParseNPUModel_Empty(t *testing.T) {
	model := parseNPUModel("no match here")
	if model != "" {
		t.Errorf("expected empty, got %q", model)
	}
}

func TestParseNPUModel_AnsiStripped(t *testing.T) {
	ansiOutput := "\x1B[0m| 0     910B4               | OK  |\x1B[0m"
	model := parseNPUModel(ansiOutput)
	if model != "910B4" {
		t.Errorf("expected 910B4 after ANSI strip, got %q", model)
	}
}

// ---- parseNPUCount ----

func TestParseNPUCount_Normal(t *testing.T) {
	total, available, unhealthy := parseNPUCount(npuSmiOutput)
	if total != 2 {
		t.Errorf("expected total=2, got %d", total)
	}
	if available != 2 {
		t.Errorf("expected available=2, got %d", available)
	}
	if len(unhealthy) != 0 {
		t.Errorf("expected no unhealthy, got %v", unhealthy)
	}
}

func TestParseNPUCount_Occupied(t *testing.T) {
	total, available, unhealthy := parseNPUCount(npuSmiOccupiedOutput)
	if total != 2 {
		t.Errorf("expected total=2, got %d", total)
	}
	if available != 1 {
		t.Errorf("expected available=1, got %d", available)
	}
	if len(unhealthy) != 0 {
		t.Errorf("expected no unhealthy, got %v", unhealthy)
	}
}

func TestParseNPUCount_Unhealthy(t *testing.T) {
	total, available, unhealthy := parseNPUCount(npuSmiUnhealthyOutput)
	if total != 2 {
		t.Errorf("expected total=2, got %d", total)
	}
	if len(unhealthy) != 1 {
		t.Errorf("expected 1 unhealthy, got %v", unhealthy)
	}
	// available = total - occupied - unhealthy = 2 - 0 - 1 = 1
	if available != 1 {
		t.Errorf("expected available=1, got %d", available)
	}
}

func TestParseNPUCount_Empty(t *testing.T) {
	total, available, unhealthy := parseNPUCount("")
	if total != 0 || available != 0 || len(unhealthy) != 0 {
		t.Errorf("expected all zero/empty for empty input, got %d %d %v", total, available, unhealthy)
	}
}

// ---- parseHccnConf ----

func TestParseHccnConf_Normal(t *testing.T) {
	// No spaces around '=' so parts[0] == "address_0" and TrimPrefix gives "0"
	content := "address_0=192.168.1.1\nnetmask_0=255.255.255.0\naddress_1=192.168.1.2\nnetmask_1=255.255.255.0\nother_key=value\n"
	ipMap, maskMap := parseHccnConf(content)
	if ipMap["0"] != "192.168.1.1" {
		t.Errorf("expected 192.168.1.1 for address_0, got %q", ipMap["0"])
	}
	if maskMap["0"] != "255.255.255.0" {
		t.Errorf("expected 255.255.255.0 for netmask_0, got %q", maskMap["0"])
	}
	if ipMap["1"] != "192.168.1.2" {
		t.Errorf("expected 192.168.1.2 for address_1, got %q", ipMap["1"])
	}
}

func TestParseHccnConf_Empty(t *testing.T) {
	ipMap, maskMap := parseHccnConf("")
	if len(ipMap) != 0 || len(maskMap) != 0 {
		t.Error("expected empty maps for empty content")
	}
}

func TestParseHccnConf_NoEquals(t *testing.T) {
	content := "address_0 192.168.1.1\nnetmask_0 255.255.255.0\n"
	ipMap, maskMap := parseHccnConf(content)
	if len(ipMap) != 0 || len(maskMap) != 0 {
		t.Error("expected empty maps when no = sign")
	}
}

// ---- checkH06MissingIPs ----

func TestCheckH06MissingIPs_AllPresent(t *testing.T) {
	ipMap := map[string]string{"0": "192.168.1.1", "1": "192.168.1.2"}
	r := checkH06MissingIPs([]int{0, 1}, ipMap)
	if r.Status == types.StatusFailed {
		t.Errorf("expected no failure, got %v: %s", r.Status, r.Message)
	}
}

func TestCheckH06MissingIPs_Missing(t *testing.T) {
	ipMap := map[string]string{"0": "192.168.1.1"}
	r := checkH06MissingIPs([]int{0, 1}, ipMap)
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed, got %v", r.Status)
	}
}

func TestCheckH06MissingIPs_EmptyValue(t *testing.T) {
	ipMap := map[string]string{"0": "192.168.1.1", "1": ""}
	r := checkH06MissingIPs([]int{0, 1}, ipMap)
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed for empty IP, got %v", r.Status)
	}
}

// ---- checkH06MissingMasks ----

func TestCheckH06MissingMasks_AllPresent(t *testing.T) {
	maskMap := map[string]string{"0": "255.255.255.0", "1": "255.255.255.0"}
	r := checkH06MissingMasks([]int{0, 1}, maskMap)
	if r.Status == types.StatusFailed {
		t.Errorf("expected no failure, got %v: %s", r.Status, r.Message)
	}
}

func TestCheckH06MissingMasks_Missing(t *testing.T) {
	maskMap := map[string]string{"0": "255.255.255.0"}
	r := checkH06MissingMasks([]int{0, 1}, maskMap)
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed, got %v", r.Status)
	}
}

// ---- checkH06IPFormatAndUniqueness ----

func TestCheckH06IPFormatAndUniqueness_Valid(t *testing.T) {
	ipMap := map[string]string{"0": "192.168.1.1", "1": "192.168.1.2"}
	r := checkH06IPFormatAndUniqueness(ipMap)
	if r.Status == types.StatusFailed {
		t.Errorf("expected pass, got %v: %s", r.Status, r.Message)
	}
}

func TestCheckH06IPFormatAndUniqueness_Invalid(t *testing.T) {
	ipMap := map[string]string{"0": "not-an-ip"}
	r := checkH06IPFormatAndUniqueness(ipMap)
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed for invalid IP, got %v", r.Status)
	}
}

func TestCheckH06IPFormatAndUniqueness_Duplicate(t *testing.T) {
	ipMap := map[string]string{"0": "192.168.1.1", "1": "192.168.1.1"}
	r := checkH06IPFormatAndUniqueness(ipMap)
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed for duplicate IP, got %v", r.Status)
	}
}

// ---- checkH06SubnetConsistency ----

func TestCheckH06SubnetConsistency_SameSubnet(t *testing.T) {
	ipMap := map[string]string{"0": "192.168.1.1", "1": "192.168.1.2"}
	maskMap := map[string]string{"0": "255.255.255.0", "1": "255.255.255.0"}
	r := checkH06SubnetConsistency([]int{0, 1}, ipMap, maskMap)
	if r.Status == types.StatusFailed {
		t.Errorf("expected pass, got %v: %s", r.Status, r.Message)
	}
}

func TestCheckH06SubnetConsistency_DifferentSubnet(t *testing.T) {
	ipMap := map[string]string{"0": "192.168.1.1", "1": "10.0.0.1"}
	maskMap := map[string]string{"0": "255.255.255.0", "1": "255.255.255.0"}
	r := checkH06SubnetConsistency([]int{0, 1}, ipMap, maskMap)
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed for different subnet, got %v", r.Status)
	}
}

func TestCheckH06SubnetConsistency_SingleNIC(t *testing.T) {
	ipMap := map[string]string{"0": "192.168.1.1"}
	maskMap := map[string]string{"0": "255.255.255.0"}
	r := checkH06SubnetConsistency([]int{0}, ipMap, maskMap)
	if r.Status == types.StatusFailed {
		t.Errorf("expected pass for single NIC, got %v: %s", r.Status, r.Message)
	}
}

// ---- parseTLSState ----

func TestParseTLSState_Enable(t *testing.T) {
	out := "tls switch[1]"
	if parseTLSState(out) != "enable" {
		t.Errorf("expected enable")
	}
}

func TestParseTLSState_Disable(t *testing.T) {
	out := "tls switch[0]"
	if parseTLSState(out) != "disable" {
		t.Errorf("expected disable")
	}
}

func TestParseTLSState_NoCert(t *testing.T) {
	out := "No Certificate Found"
	if parseTLSState(out) != "no_cert" {
		t.Errorf("expected no_cert")
	}
}

func TestParseTLSState_Unknown(t *testing.T) {
	out := "some random output"
	if parseTLSState(out) != "unknown" {
		t.Errorf("expected unknown")
	}
}

// ---- normalizeTLS ----

func TestNormalizeTLS(t *testing.T) {
	if normalizeTLS("no_cert") != "disable" {
		t.Error("no_cert should normalize to disable")
	}
	if normalizeTLS("enable") != "enable" {
		t.Error("enable should stay enable")
	}
	if normalizeTLS("disable") != "disable" {
		t.Error("disable should stay disable")
	}
}

// ---- checkH01 ----

func TestCheckH01_Pass(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"lsmod | grep drv_pcie_host": {stdout: "drv_pcie_host 12345 0"},
			"npu-smi info":               {stdout: npuSmiOutput},
		},
	}
	r := checkH01(m)
	if r.Status != types.StatusPassed {
		t.Errorf("expected passed, got %v: %s", r.Status, r.Message)
	}
}

func TestCheckH01_NoLsmod(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"lsmod | grep drv_pcie_host": {stdout: ""},
		},
	}
	r := checkH01(m)
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed, got %v", r.Status)
	}
}

func TestCheckH01_LsmodErr(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"lsmod | grep drv_pcie_host": {err: errors.New("cmd error")},
		},
	}
	r := checkH01(m)
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed, got %v", r.Status)
	}
}

func TestCheckH01_NpuSmiErr(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"lsmod | grep drv_pcie_host": {stdout: "drv_pcie_host"},
			"npu-smi info":               {err: errors.New("not found")},
		},
	}
	r := checkH01(m)
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed, got %v", r.Status)
	}
}

// ---- checkH02 ----

func TestCheckH02_Pass(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"kubectl get pods -A -o wide | grep ascend-device-plugin": {
				stdout: "kube-system   ascend-device-plugin-node1   1/1   Running   0   node1",
			},
			"kubectl get node node1 -o jsonpath='{.status.capacity}'": {
				stdout: `{"cpu":"96","huawei.com/Ascend910B4":"8"}`,
			},
		},
	}
	r := checkH02(m, "node1")
	if r.Status != types.StatusPassed {
		t.Errorf("expected passed, got %v: %s", r.Status, r.Message)
	}
}

func TestCheckH02_PluginNotFound(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"kubectl get pods -A -o wide | grep ascend-device-plugin": {stdout: ""},
		},
	}
	r := checkH02(m, "node1")
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed, got %v", r.Status)
	}
}

func TestCheckH02_PluginNotRunning(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"kubectl get pods -A -o wide | grep ascend-device-plugin": {
				stdout: "kube-system   ascend-device-plugin-node1   0/1   Pending   0   node1",
			},
		},
	}
	r := checkH02(m, "node1")
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed, got %v", r.Status)
	}
}

func TestCheckH02_ResourceNotRegistered(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"kubectl get pods -A -o wide | grep ascend-device-plugin": {
				stdout: "kube-system   ascend-device-plugin-node1   1/1   Running   0   node1",
			},
			"kubectl get node node1 -o jsonpath='{.status.capacity}'": {
				stdout: `{"cpu":"96"}`,
			},
		},
	}
	r := checkH02(m, "node1")
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed, got %v", r.Status)
	}
}

// ---- checkH03 ----

func TestCheckH03_910Series(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"npu-smi info": {stdout: npuSmiOutput},
		},
	}
	r, is910 := checkH03(m)
	if r.Status != types.StatusPassed {
		t.Errorf("expected passed, got %v: %s", r.Status, r.Message)
	}
	if !is910 {
		t.Error("expected is910=true")
	}
}

func TestCheckH03_NonSeries(t *testing.T) {
	nonSeries := `| 0     310P3               | OK      | 67.3      | 43       | 0      / 0              | 0  |`
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"npu-smi info": {stdout: nonSeries},
		},
	}
	r, is910 := checkH03(m)
	if r.Status != types.StatusWarning {
		t.Errorf("expected warning for non-910, got %v", r.Status)
	}
	if is910 {
		t.Error("expected is910=false")
	}
}

func TestCheckH03_NpuSmiErr(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"npu-smi info": {err: errors.New("not found")},
		},
	}
	r, is910 := checkH03(m)
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed, got %v", r.Status)
	}
	if is910 {
		t.Error("expected is910=false")
	}
}

func TestCheckH03_NoModel(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"npu-smi info": {stdout: "no model here"},
		},
	}
	r, is910 := checkH03(m)
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed, got %v", r.Status)
	}
	if is910 {
		t.Error("expected is910=false")
	}
}

// ---- checkH04 ----

func TestCheckH04_Normal(t *testing.T) {
	r := checkH04(npuSmiOutput)
	if r.Status != types.StatusInfo {
		t.Errorf("expected info, got %v", r.Status)
	}
}

func TestCheckH04_Empty(t *testing.T) {
	r := checkH04("")
	if r.Status != types.StatusInfo {
		t.Errorf("expected info, got %v", r.Status)
	}
}

func TestCheckH04_NoAvailable(t *testing.T) {
	// All NPUs occupied
	allOccupied := `| 0     910B4               | OK      | 67.3      | 43       | 0      / 0              | 0  |
| 0     0      | 12345     | python              | 4096                                    |
`
	r := checkH04(allOccupied)
	if r.Status != types.StatusInfo {
		t.Errorf("expected info, got %v", r.Status)
	}
}

// ---- checkH05 ----

func TestCheckH05_Pass(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"/usr/local/dcmi":                       {stdout: "ok"},
			"/usr/local/Ascend/driver/lib64":        {stdout: "ok"},
			"/usr/local/bin/npu-smi":                {stdout: "ok"},
			"/usr/local/Ascend/driver/version.info": {stdout: "ok"},
			"/etc/ascend_install.info":              {stdout: "ok"},
			"/etc/hccn.conf":                        {stdout: "ok"},
		},
	}
	r := checkH05(m)
	if r.Status != types.StatusPassed {
		t.Errorf("expected passed, got %v: %s", r.Status, r.Message)
	}
}

func TestCheckH05_MissingFile(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"/etc/hccn.conf": {err: errors.New("no such file")},
		},
	}
	r := checkH05(m)
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed, got %v", r.Status)
	}
}

// ---- checkH06 ----

func goodHccnConf() string {
	return `address_0=192.168.1.1
netmask_0=255.255.255.0
address_1=192.168.1.2
netmask_1=255.255.255.0
`
}

func TestCheckH06_Pass(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"cat /etc/hccn.conf": {stdout: goodHccnConf()},
		},
	}
	r, ipMap := checkH06(m, []int{0, 1})
	if r.Status != types.StatusPassed {
		t.Errorf("expected passed, got %v: %s", r.Status, r.Message)
	}
	if ipMap["0"] != "192.168.1.1" {
		t.Errorf("unexpected ipMap: %v", ipMap)
	}
}

func TestCheckH06_ReadFail(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"cat /etc/hccn.conf": {err: errors.New("read error")},
		},
	}
	r, _ := checkH06(m, []int{0, 1})
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed, got %v", r.Status)
	}
}

func TestCheckH06_MissingIP(t *testing.T) {
	conf := `address_0 = 192.168.1.1
netmask_0 = 255.255.255.0
netmask_1 = 255.255.255.0
`
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"cat /etc/hccn.conf": {stdout: conf},
		},
	}
	r, _ := checkH06(m, []int{0, 1})
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed, got %v", r.Status)
	}
}

func TestCheckH06_MissingMask(t *testing.T) {
	conf := `address_0 = 192.168.1.1
address_1 = 192.168.1.2
netmask_0 = 255.255.255.0
`
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"cat /etc/hccn.conf": {stdout: conf},
		},
	}
	r, _ := checkH06(m, []int{0, 1})
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed, got %v", r.Status)
	}
}

func TestCheckH06_BadIP(t *testing.T) {
	conf := `address_0 = bad-ip
netmask_0 = 255.255.255.0
address_1 = 192.168.1.2
netmask_1 = 255.255.255.0
`
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"cat /etc/hccn.conf": {stdout: conf},
		},
	}
	r, _ := checkH06(m, []int{0, 1})
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed for bad IP, got %v", r.Status)
	}
}

func TestCheckH06_DifferentSubnet(t *testing.T) {
	conf := `address_0 = 192.168.1.1
netmask_0 = 255.255.255.0
address_1 = 10.0.0.1
netmask_1 = 255.255.255.0
`
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"cat /etc/hccn.conf": {stdout: conf},
		},
	}
	r, _ := checkH06(m, []int{0, 1})
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed for different subnet, got %v", r.Status)
	}
}

// ---- checkH07TLSConsistency ----

func TestCheckH07TLSConsistency_Consistent(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"hccn_tool -i 0 -tls -g": {stdout: "tls switch[1]"},
			"hccn_tool -i 1 -tls -g": {stdout: "tls switch[1]"},
		},
	}
	r, tlsStates := checkH07TLSConsistency(m, []int{0, 1})
	if r.Status != types.StatusPassed {
		t.Errorf("expected passed, got %v: %s", r.Status, r.Message)
	}
	if tlsStates["0"] != "enable" || tlsStates["1"] != "enable" {
		t.Errorf("unexpected tls states: %v", tlsStates)
	}
}

func TestCheckH07TLSConsistency_Inconsistent(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"hccn_tool -i 0 -tls -g": {stdout: "tls switch[1]"},
			"hccn_tool -i 1 -tls -g": {stdout: "tls switch[0]"},
		},
	}
	r, _ := checkH07TLSConsistency(m, []int{0, 1})
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed, got %v", r.Status)
	}
}

func TestCheckH07TLSConsistency_NoCertTreatedAsDisable(t *testing.T) {
	// no_cert normalizes to disable, so disable + no_cert should be consistent
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"hccn_tool -i 0 -tls -g": {stdout: "tls switch[0]"},
			"hccn_tool -i 1 -tls -g": {stderr: "No Certificate Found"},
		},
	}
	r, _ := checkH07TLSConsistency(m, []int{0, 1})
	if r.Status != types.StatusPassed {
		t.Errorf("expected passed (no_cert==disable), got %v: %s", r.Status, r.Message)
	}
}

func TestCheckH07TLSConsistency_Error(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"hccn_tool -i 0 -tls -g": {err: errors.New("command failed"), stderr: "error"},
		},
	}
	r, _ := checkH07TLSConsistency(m, []int{0})
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed, got %v", r.Status)
	}
}

// ---- checkH07Connectivity ----

func TestCheckH07Connectivity_Pass(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"hccn_tool": {stdout: "ping success, 0.00% packet loss"},
		},
	}
	ipMap := map[string]string{"0": "192.168.1.1", "1": "192.168.1.2"}
	r := checkH07Connectivity(m, []int{0, 1}, ipMap)
	if r.Status != types.StatusPassed {
		t.Errorf("expected passed, got %v: %s", r.Status, r.Message)
	}
}

func TestCheckH07Connectivity_PingFail(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"hccn_tool": {stdout: "100.00% packet loss"},
		},
	}
	ipMap := map[string]string{"0": "192.168.1.1", "1": "192.168.1.2"}
	r := checkH07Connectivity(m, []int{0, 1}, ipMap)
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed, got %v", r.Status)
	}
}

func TestCheckH07Connectivity_CommandExecuteFailed(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"hccn_tool": {stdout: "Command execute failed"},
		},
	}
	ipMap := map[string]string{"0": "192.168.1.1", "1": "192.168.1.2"}
	r := checkH07Connectivity(m, []int{0}, ipMap)
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed, got %v", r.Status)
	}
}

// ---- crossNodeIPUniqueness ----

func TestCrossNodeIPUniqueness_Unique(t *testing.T) {
	ctxs := []*NodeCheckContext{
		{Node: types.NodeInfo{Name: "node1"}, HccnIPs: map[string]string{"0": "192.168.1.1"}},
		{Node: types.NodeInfo{Name: "node2"}, HccnIPs: map[string]string{"0": "192.168.1.2"}},
	}
	r := crossNodeIPUniqueness(ctxs)
	if r.Status != types.StatusPassed {
		t.Errorf("expected passed, got %v: %s", r.Status, r.Message)
	}
}

func TestCrossNodeIPUniqueness_Duplicate(t *testing.T) {
	ctxs := []*NodeCheckContext{
		{Node: types.NodeInfo{Name: "node1"}, HccnIPs: map[string]string{"0": "192.168.1.1"}},
		{Node: types.NodeInfo{Name: "node2"}, HccnIPs: map[string]string{"0": "192.168.1.1"}},
	}
	r := crossNodeIPUniqueness(ctxs)
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed, got %v", r.Status)
	}
}

// ---- crossNodeTLSConsistency ----

func TestCrossNodeTLSConsistency_Consistent(t *testing.T) {
	ctxs := []*NodeCheckContext{
		{Node: types.NodeInfo{Name: "node1"}, TLSStates: map[string]string{"0": "enable"}},
		{Node: types.NodeInfo{Name: "node2"}, TLSStates: map[string]string{"0": "enable"}},
	}
	r := crossNodeTLSConsistency(ctxs)
	if r.Status != types.StatusPassed {
		t.Errorf("expected passed, got %v: %s", r.Status, r.Message)
	}
}

func TestCrossNodeTLSConsistency_Inconsistent(t *testing.T) {
	ctxs := []*NodeCheckContext{
		{Node: types.NodeInfo{Name: "node1"}, TLSStates: map[string]string{"0": "enable"}},
		{Node: types.NodeInfo{Name: "node2"}, TLSStates: map[string]string{"0": "disable"}},
	}
	r := crossNodeTLSConsistency(ctxs)
	if r.Status != types.StatusFailed {
		t.Errorf("expected failed, got %v", r.Status)
	}
}

func TestCrossNodeTLSConsistency_NoCertVsDisable(t *testing.T) {
	ctxs := []*NodeCheckContext{
		{Node: types.NodeInfo{Name: "node1"}, TLSStates: map[string]string{"0": "disable"}},
		{Node: types.NodeInfo{Name: "node2"}, TLSStates: map[string]string{"0": "no_cert"}},
	}
	r := crossNodeTLSConsistency(ctxs)
	if r.Status != types.StatusPassed {
		t.Errorf("expected passed (no_cert==disable), got %v: %s", r.Status, r.Message)
	}
}

// ---- checkCrossNodes (H-08) ----

func TestCheckCrossNodes_NotEnoughNodes(t *testing.T) {
	ctxs := []*NodeCheckContext{
		{Node: types.NodeInfo{Name: "node1"}, HccnIPs: map[string]string{},
			TLSStates: map[string]string{}},
	}
	results := checkCrossNodes(ctxs)
	if len(results) != 1 || results[0].Status != types.StatusSkipped {
		t.Errorf("expected single skipped result, got %v", results)
	}
}

func TestCheckCrossNodes_TwoNodes_IPConflict(t *testing.T) {
	// Two nodes with same IP -> should fail at step 1
	ctxs := []*NodeCheckContext{
		{Node: types.NodeInfo{Name: "node1"}, HccnIPs: map[string]string{"0": "192.168.1.1"},
			TLSStates: map[string]string{"0": "enable"}},
		{Node: types.NodeInfo{Name: "node2"}, HccnIPs: map[string]string{"0": "192.168.1.1"},
			TLSStates: map[string]string{"0": "enable"}},
	}
	results := checkCrossNodes(ctxs)
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != types.StatusFailed {
		t.Errorf("expected step1 failed, got %v", results[0].Status)
	}
}

// ---- CheckHardware (integration with mock SSHRunner) ----

func TestCheckHardware_SSHFail(t *testing.T) {
	origFactory := newSSHRunner
	defer func() { newSSHRunner = origFactory }()

	newSSHRunner = func(node types.NodeInfo) (executor.CommandRunner, func(), error) {
		return nil, func() {}, errors.New("ssh connection refused")
	}

	nodes := []types.NodeInfo{{Name: "node1", IP: "1.2.3.4", Port: 22}}
	result := CheckHardware(nodes)
	if len(result.Nodes) != 1 {
		t.Fatalf("expected 1 node result, got %d", len(result.Nodes))
	}
	if result.Nodes[0].Status != types.StatusFailed {
		t.Errorf("expected failed, got %v", result.Nodes[0].Status)
	}
}

func TestCheckHardware_H01Fail(t *testing.T) {
	origFactory := newSSHRunner
	defer func() { newSSHRunner = origFactory }()

	newSSHRunner = func(node types.NodeInfo) (executor.CommandRunner, func(), error) {
		m := &mockRunner{
			nodeName: node.Name,
			responses: map[string]mockResponse{
				"lsmod | grep drv_pcie_host": {stdout: ""},
			},
		}
		return m, func() {}, nil
	}

	nodes := []types.NodeInfo{{Name: "node1"}}
	result := CheckHardware(nodes)
	if result.Nodes[0].Status != types.StatusFailed {
		t.Errorf("expected failed, got %v", result.Nodes[0].Status)
	}
}

func TestCheckHardware_NonSeries_SkipsHCCS(t *testing.T) {
	origFactory := newSSHRunner
	defer func() { newSSHRunner = origFactory }()

	nonSeriesNpuSmi := `| 0     310P3               | OK      | 67.3      | 43       | 0      / 0              | 0  |`

	newSSHRunner = func(node types.NodeInfo) (executor.CommandRunner, func(), error) {
		m := &mockRunner{
			nodeName: node.Name,
			responses: map[string]mockResponse{
				"lsmod | grep drv_pcie_host": {stdout: "drv_pcie_host"},
				"npu-smi info":               {stdout: nonSeriesNpuSmi},
				"kubectl get pods -A -o wide | grep ascend-device-plugin": {
					stdout: fmt.Sprintf("kube-system   ascend-device-plugin-%s   1/1   Running   0   %s", node.Name, node.Name),
				},
				fmt.Sprintf("kubectl get node %s -o jsonpath='{.status.capacity}'", node.Name): {
					stdout: `{"huawei.com/Ascend310P3":"4"}`,
				},
				"ls /usr/local/dcmi":                       {stdout: "ok"},
				"ls /usr/local/Ascend/driver/lib64":        {stdout: "ok"},
				"ls /usr/local/bin/npu-smi":                {stdout: "ok"},
				"ls /usr/local/Ascend/driver/version.info": {stdout: "ok"},
				"ls /etc/ascend_install.info":              {stdout: "ok"},
				"ls /etc/hccn.conf":                        {stdout: "ok"},
			},
		}
		return m, func() {}, nil
	}

	nodes := []types.NodeInfo{{Name: "node1"}}
	result := CheckHardware(nodes)
	if len(result.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(result.Nodes))
	}
	if !result.Nodes[0].SkippedHCCS {
		t.Error("expected HCCS skipped for non-910 series")
	}
}

func TestCheckHardware_ZeroNodes(t *testing.T) {
	result := CheckHardware([]types.NodeInfo{})
	if result == nil {
		t.Error("expected non-nil result")
	}
	// cross-node check should return skipped
	if len(result.CrossNodes) != 1 || result.CrossNodes[0].Status != types.StatusSkipped {
		t.Errorf("expected single skipped cross-node result, got %v", result.CrossNodes)
	}
}

// ---- getPhyIDs ----

func TestGetPhyIDs_Normal(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"ls /dev/davinci*": {stdout: "/dev/davinci0 /dev/davinci1 /dev/davinci_manager"},
		},
	}
	ids, err := getPhyIDs(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("expected 2 ids, got %v", ids)
	}
}

func TestGetPhyIDs_Error(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"ls /dev/davinci*": {err: errors.New("no such file")},
		},
	}
	ids, err := getPhyIDs(m)
	if err == nil {
		t.Error("expected error")
	}
	if len(ids) != 0 {
		t.Errorf("expected empty ids, got %v", ids)
	}
}

func TestGetPhyIDs_OnlyManager(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"ls /dev/davinci*": {stdout: "/dev/davinci_manager"},
		},
	}
	ids, err := getPhyIDs(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected empty ids, got %v", ids)
	}
}

// ---- checkH07 ----

func TestCheckH07_TLSFailEarlyReturn(t *testing.T) {
	// TLS inconsistency should cause early return (only 1 result)
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"hccn_tool -i 0 -tls -g": {stdout: "tls switch[1]"},
			"hccn_tool -i 1 -tls -g": {stdout: "tls switch[0]"},
		},
	}
	ipMap := map[string]string{"0": "192.168.1.1", "1": "192.168.1.2"}
	results, tlsStates := checkH07(m, []int{0, 1}, ipMap)
	if len(results) != 1 {
		t.Errorf("expected 1 result on TLS fail, got %d", len(results))
	}
	if results[0].Status != types.StatusFailed {
		t.Errorf("expected failed, got %v", results[0].Status)
	}
	_ = tlsStates
}

func TestCheckH07_TLSPassConnectivityPass(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"hccn_tool -i 0 -tls -g": {stdout: "tls switch[1]"},
			"hccn_tool -i 1 -tls -g": {stdout: "tls switch[1]"},
			"hccn_tool":              {stdout: "0.00% packet loss"},
		},
	}
	ipMap := map[string]string{"0": "192.168.1.1", "1": "192.168.1.2"}
	results, _ := checkH07(m, []int{0, 1}, ipMap)
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
	if results[0].Status != types.StatusPassed {
		t.Errorf("expected TLS passed, got %v", results[0].Status)
	}
}

func TestCheckH07_TLSPassConnectivityFail(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"hccn_tool -i 0 -tls -g": {stdout: "tls switch[1]"},
			"hccn_tool -i 1 -tls -g": {stdout: "tls switch[1]"},
			"hccn_tool":              {stdout: "100.00% packet loss"},
		},
	}
	ipMap := map[string]string{"0": "192.168.1.1", "1": "192.168.1.2"}
	results, _ := checkH07(m, []int{0, 1}, ipMap)
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
	if results[1].Status != types.StatusFailed {
		t.Errorf("expected connectivity failed, got %v", results[1].Status)
	}
}

// ---- check910SeriesNode ----

func TestCheck910SeriesNode_GetPhyIDsFail(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"ls /dev/davinci*": {err: errors.New("no such file")},
		},
	}
	nodeResult := types.NodeResult{Name: "node1", Status: types.StatusPassed}
	ctx := &NodeCheckContext{Node: types.NodeInfo{Name: "node1"}}
	result, retCtx := check910SeriesNode(m, "node1", 2, nodeResult, ctx)
	if result.Status != types.StatusFailed {
		t.Errorf("expected failed, got %v", result.Status)
	}
	if retCtx != nil {
		t.Error("expected nil ctx on failure")
	}
}

func TestCheck910SeriesNode_PhyIDCountMismatch(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			// Only 1 device but npuTotal=2
			"ls /dev/davinci*": {stdout: "/dev/davinci0"},
		},
	}
	nodeResult := types.NodeResult{Name: "node1", Status: types.StatusPassed}
	ctx := &NodeCheckContext{Node: types.NodeInfo{Name: "node1"}}
	result, retCtx := check910SeriesNode(m, "node1", 2, nodeResult, ctx)
	if result.Status != types.StatusFailed {
		t.Errorf("expected failed for count mismatch, got %v", result.Status)
	}
	if retCtx != nil {
		t.Error("expected nil ctx on mismatch")
	}
}

func TestCheck910SeriesNode_H06Fail(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"ls /dev/davinci*":   {stdout: "/dev/davinci0 /dev/davinci1"},
			"cat /etc/hccn.conf": {err: errors.New("read error")},
		},
	}
	nodeResult := types.NodeResult{Name: "node1", Status: types.StatusPassed}
	ctx := &NodeCheckContext{Node: types.NodeInfo{Name: "node1"}}
	result, retCtx := check910SeriesNode(m, "node1", 2, nodeResult, ctx)
	if result.Status != types.StatusFailed {
		t.Errorf("expected failed on H06 fail, got %v", result.Status)
	}
	if retCtx != nil {
		t.Error("expected nil ctx when H06 fails")
	}
}

func TestCheck910SeriesNode_H07Fail(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"ls /dev/davinci*":   {stdout: "/dev/davinci0 /dev/davinci1"},
			"cat /etc/hccn.conf": {stdout: "address_0=192.168.1.1\nnetmask_0=255.255.255.0\naddress_1=192.168.1.2\nnetmask_1=255.255.255.0\n"},
			// TLS inconsistent -> H07 fails
			"hccn_tool -i 0 -tls -g": {stdout: "tls switch[1]"},
			"hccn_tool -i 1 -tls -g": {stdout: "tls switch[0]"},
		},
	}
	nodeResult := types.NodeResult{Name: "node1", Status: types.StatusPassed}
	ctx := &NodeCheckContext{Node: types.NodeInfo{Name: "node1"}}
	result, retCtx := check910SeriesNode(m, "node1", 2, nodeResult, ctx)
	if result.Status != types.StatusFailed {
		t.Errorf("expected failed on H07 fail, got %v", result.Status)
	}
	if retCtx != nil {
		t.Error("expected nil ctx when H07 fails")
	}
}

func TestCheck910SeriesNode_Pass(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"ls /dev/davinci*":       {stdout: "/dev/davinci0 /dev/davinci1"},
			"cat /etc/hccn.conf":     {stdout: "address_0=192.168.1.1\nnetmask_0=255.255.255.0\naddress_1=192.168.1.2\nnetmask_1=255.255.255.0\n"},
			"hccn_tool -i 0 -tls -g": {stdout: "tls switch[1]"},
			"hccn_tool -i 1 -tls -g": {stdout: "tls switch[1]"},
			"hccn_tool":              {stdout: "0.00% packet loss"},
		},
	}
	nodeResult := types.NodeResult{Name: "node1", Status: types.StatusPassed}
	ctx := &NodeCheckContext{Node: types.NodeInfo{Name: "node1"}}
	result, retCtx := check910SeriesNode(m, "node1", 2, nodeResult, ctx)
	if result.Status == types.StatusFailed {
		t.Errorf("expected not failed, got %v: checking checks", result.Status)
		for _, c := range result.Checks {
			t.Logf("  check %s: %v - %s", c.ID, c.Status, c.Message)
		}
	}
	if retCtx == nil {
		t.Error("expected non-nil ctx on success")
	}
}

func TestCheck910SeriesNode_NpuTotalZeroNoMismatch(t *testing.T) {
	// npuTotal=0 means skip count check
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"ls /dev/davinci*":       {stdout: "/dev/davinci0"},
			"cat /etc/hccn.conf":     {stdout: "address_0=192.168.1.1\nnetmask_0=255.255.255.0\n"},
			"hccn_tool -i 0 -tls -g": {stdout: "tls switch[1]"},
			"hccn_tool":              {stdout: "0.00% packet loss"},
		},
	}
	nodeResult := types.NodeResult{Name: "node1", Status: types.StatusPassed}
	ctx := &NodeCheckContext{Node: types.NodeInfo{Name: "node1"}}
	// npuTotal=0 -> no mismatch check
	_, retCtx := check910SeriesNode(m, "node1", 0, nodeResult, ctx)
	if retCtx == nil {
		t.Error("expected non-nil ctx when npuTotal=0 (no mismatch)")
	}
}

// ---- pingToDestination and pingOnePath ----

func TestPingOnePath_Pass(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"hccn_tool": {stdout: "0.00% packet loss"},
		},
	}
	path := pingPath{srcName: "node1", srcPhyID: 0, dstName: "node2", dstNic: "0", dstIP: "192.168.1.2"}
	result := pingOnePath(m, path)
	if result != "" {
		t.Errorf("expected empty string on pass, got %q", result)
	}
}

func TestPingOnePath_PacketLoss(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"hccn_tool": {stdout: "100.00% packet loss"},
		},
	}
	path := pingPath{srcName: "node1", srcPhyID: 0, dstName: "node2", dstNic: "0", dstIP: "192.168.1.2"}
	result := pingOnePath(m, path)
	if result == "" {
		t.Error("expected failure string, got empty")
	}
}

func TestPingOnePath_CommandFailed(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"hccn_tool": {stdout: "Command execute failed"},
		},
	}
	path := pingPath{srcName: "node1", srcPhyID: 0, dstName: "node2", dstNic: "0", dstIP: "192.168.1.2"}
	result := pingOnePath(m, path)
	if result == "" {
		t.Error("expected failure string, got empty")
	}
}

func TestPingOnePath_Error(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"hccn_tool": {err: errors.New("connection failed")},
		},
	}
	path := pingPath{srcName: "node1", srcPhyID: 0, dstName: "node2", dstNic: "0", dstIP: "192.168.1.2"}
	result := pingOnePath(m, path)
	if result == "" {
		t.Error("expected failure string on error, got empty")
	}
}

func TestPingToDestination_Pass(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"hccn_tool": {stdout: "0.00% packet loss"},
		},
	}
	src := &NodeCheckContext{
		Node:   types.NodeInfo{Name: "node1"},
		PhyIDs: []int{0},
	}
	dst := &NodeCheckContext{
		Node:    types.NodeInfo{Name: "node2"},
		HccnIPs: map[string]string{"0": "192.168.1.2"},
	}
	failed := pingToDestination(m, src, dst)
	if len(failed) != 0 {
		t.Errorf("expected no failures, got %v", failed)
	}
}

func TestPingToDestination_Fail(t *testing.T) {
	m := &mockRunner{
		nodeName: "node1",
		responses: map[string]mockResponse{
			"hccn_tool": {stdout: "100.00% packet loss"},
		},
	}
	src := &NodeCheckContext{
		Node:   types.NodeInfo{Name: "node1"},
		PhyIDs: []int{0},
	}
	dst := &NodeCheckContext{
		Node:    types.NodeInfo{Name: "node2"},
		HccnIPs: map[string]string{"0": "192.168.1.2"},
	}
	failed := pingToDestination(m, src, dst)
	if len(failed) == 0 {
		t.Error("expected failures, got none")
	}
}

// ---- crossNodePingOne via CheckCrossNodes integration ----

func TestCheckCrossNodes_TwoNodes_Pass(t *testing.T) {
	origFactory := newSSHRunner
	defer func() { newSSHRunner = origFactory }()

	newSSHRunner = func(node types.NodeInfo) (executor.CommandRunner, func(), error) {
		m := &mockRunner{
			nodeName: node.Name,
			responses: map[string]mockResponse{
				"hccn_tool": {stdout: "0.00% packet loss"},
			},
		}
		return m, func() {}, nil
	}

	ctxs := []*NodeCheckContext{
		{
			Node:      types.NodeInfo{Name: "node1"},
			PhyIDs:    []int{0},
			HccnIPs:   map[string]string{"0": "192.168.1.1"},
			TLSStates: map[string]string{"0": "enable"},
		},
		{
			Node:      types.NodeInfo{Name: "node2"},
			PhyIDs:    []int{0},
			HccnIPs:   map[string]string{"0": "192.168.1.2"},
			TLSStates: map[string]string{"0": "enable"},
		},
	}
	results := checkCrossNodes(ctxs)
	if len(results) != 3 {
		t.Errorf("expected 3 results, got %d", len(results))
	}
	for _, r := range results {
		if r.Status == types.StatusFailed {
			t.Errorf("unexpected failure: %s - %s", r.ID, r.Message)
		}
	}
}

func TestCheckCrossNodes_SSHFail(t *testing.T) {
	origFactory := newSSHRunner
	defer func() { newSSHRunner = origFactory }()

	newSSHRunner = func(node types.NodeInfo) (executor.CommandRunner, func(), error) {
		return nil, func() {}, errors.New("ssh refused")
	}

	ctxs := []*NodeCheckContext{
		{
			Node:      types.NodeInfo{Name: "node1"},
			PhyIDs:    []int{0},
			HccnIPs:   map[string]string{"0": "192.168.1.1"},
			TLSStates: map[string]string{"0": "enable"},
		},
		{
			Node:      types.NodeInfo{Name: "node2"},
			PhyIDs:    []int{0},
			HccnIPs:   map[string]string{"0": "192.168.1.2"},
			TLSStates: map[string]string{"0": "enable"},
		},
	}
	results := checkCrossNodes(ctxs)
	// step 3 connectivity should pass vacuously (no failed pings collected since SSH failed)
	// but steps 1 and 2 should still run
	if len(results) != 3 {
		t.Errorf("expected 3 results, got %d", len(results))
	}
}

// ---- CheckHardware full pass integration ----

func TestCheckHardware_FullPass(t *testing.T) {
	origFactory := newSSHRunner
	defer func() { newSSHRunner = origFactory }()

	newSSHRunner = func(node types.NodeInfo) (executor.CommandRunner, func(), error) {
		m := &mockRunner{
			nodeName: node.Name,
			responses: map[string]mockResponse{
				"lsmod | grep drv_pcie_host": {stdout: "drv_pcie_host 12345 0"},
				"npu-smi info":               {stdout: npuSmiOutput},
				"kubectl get pods -A -o wide | grep ascend-device-plugin": {
					stdout: fmt.Sprintf("kube-system   ascend-device-plugin-%s   1/1   Running   0   %s", node.Name, node.Name),
				},
				fmt.Sprintf("kubectl get node %s -o jsonpath='{.status.capacity}'", node.Name): {
					stdout: `{"huawei.com/Ascend910B4":"2"}`,
				},
				"ls /usr/local/dcmi":                       {stdout: "ok"},
				"ls /usr/local/Ascend/driver/lib64":        {stdout: "ok"},
				"ls /usr/local/bin/npu-smi":                {stdout: "ok"},
				"ls /usr/local/Ascend/driver/version.info": {stdout: "ok"},
				"ls /etc/ascend_install.info":              {stdout: "ok"},
				"ls /etc/hccn.conf":                        {stdout: "ok"},
				"ls /dev/davinci*":                         {stdout: "/dev/davinci0 /dev/davinci1"},
				"cat /etc/hccn.conf":                       {stdout: "address_0 = 192.168.1.1\nnetmask_0 = 255.255.255.0\naddress_1 = 192.168.1.2\nnetmask_1 = 255.255.255.0\n"},
				"hccn_tool -i 0 -tls -g":                   {stdout: "tls switch[1]"},
				"hccn_tool -i 1 -tls -g":                   {stdout: "tls switch[1]"},
				"hccn_tool":                                {stdout: "0.00% packet loss"},
			},
		}
		return m, func() {}, nil
	}

	nodes := []types.NodeInfo{{Name: "node1"}}
	result := CheckHardware(nodes)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(result.Nodes))
	}
}
