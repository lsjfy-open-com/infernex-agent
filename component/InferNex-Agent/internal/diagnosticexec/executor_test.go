package diagnosticexec

import (
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func TestProbeCommandsRejectsArbitraryProbeAndDevice(t *testing.T) {
	if _, err := probeCommands("rm-everything", 0); err == nil {
		t.Fatal("arbitrary probe was accepted")
	}
	if _, err := probeCommands("hccn-device", 64); err == nil {
		t.Fatal("out-of-range device was accepted")
	}
	if _, err := probeCommands("hccn-pfc-stats", -1); err == nil {
		t.Fatal("negative PFC device was accepted")
	}
}

func TestPFCAndHCCLProbesAreFixedReadOnlyCommands(t *testing.T) {
	pfc, err := probeCommands("hccn-pfc-stats", 3)
	if err != nil || len(pfc) != 2 || displayCommand(pfc[0]) != "hccn_tool -i 3 -stat -g" {
		t.Fatalf("PFC commands = %#v, %v", pfc, err)
	}
	rootInfo, err := probeCommands("hccl-root-info", 0)
	if err != nil || len(rootInfo) != 1 || displayCommand(rootInfo[0]) != "cat /etc/hccl_rootInfo.json" {
		t.Fatalf("HCCL root-info commands = %#v, %v", rootInfo, err)
	}
	layout, err := probeCommands("hccl-test-layout", 0)
	if err != nil || len(layout) != 2 {
		t.Fatalf("HCCL layout commands = %#v, %v", layout, err)
	}
}

func TestNewRejectsSSHAddressSyntax(t *testing.T) {
	_, err := New(fake.NewSimpleClientset(), &rest.Config{Host: "https://example.invalid"}, "/etc/ssh/config", []string{"root@10.0.0.1"})
	if err == nil {
		t.Fatal("model-addressable SSH destination was accepted")
	}
}

func TestLimitedBufferCapsWithoutShortWrite(t *testing.T) {
	buffer := &limitedBuffer{limit: 4}
	input := []byte("123456")
	n, err := buffer.Write(input)
	if err != nil || n != len(input) {
		t.Fatalf("write = %d, %v", n, err)
	}
	if got := buffer.String(); got != "1234" || !buffer.truncated {
		t.Fatalf("buffer = %q truncated=%v", got, buffer.truncated)
	}
}

func TestSSHRequiresConfiguredAlias(t *testing.T) {
	runner, err := New(fake.NewSimpleClientset(), &rest.Config{Host: "https://example.invalid"}, "/etc/infernex-agent/ssh.conf", []string{"npu-node-01"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), Request{Channel: "ssh", Probe: "system-summary", SSHTarget: "npu-node-02"})
	if err == nil || !strings.Contains(err.Error(), "allow-list") {
		t.Fatalf("unexpected error: %v", err)
	}
}
