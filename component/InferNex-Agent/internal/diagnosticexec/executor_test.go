package diagnosticexec

import (
	"context"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func TestCommandFailureRemainsStructuredEvidence(t *testing.T) {
	runner := &Runner{timeout: time.Second}
	result, err := runner.runCommand(context.Background(), "local", "management-node", "test-missing", commandSpec{name: "definitely-not-an-infernex-command"}, "definitely-not-an-infernex-command")
	if err == nil {
		t.Fatal("missing command unexpectedly succeeded")
	}
	if result.Status != "failed" || result.ExitCode != -1 || result.Error == "" || result.Command == "" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

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

func TestRootHelperChannelIsOptIn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket path validation is Linux-specific")
	}
	runner, err := New(fake.NewSimpleClientset(), &rest.Config{Host: "https://example.invalid"}, "", nil, WithRootHelper("/run/infernex-agent/collector.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(runner.Channels(), "host-root") {
		t.Fatalf("channels=%v", runner.Channels())
	}
	if _, err := New(fake.NewSimpleClientset(), &rest.Config{Host: "https://example.invalid"}, "", nil, WithRootHelper("/tmp/arbitrary.sock")); err == nil {
		t.Fatal("unsafe root helper socket was accepted")
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

func TestSSHDiscoveryOnlyAdvertisesConfiguredChannel(t *testing.T) {
	for _, test := range []struct {
		config  string
		targets []string
		want    bool
	}{
		{"", nil, false}, {"/etc/ssh/config", nil, false}, {"/etc/ssh/config", []string{"peer"}, true},
	} {
		runner, err := New(fake.NewSimpleClientset(), &rest.Config{Host: "https://example.invalid"}, test.config, test.targets)
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(runner.Channels(), "ssh") != test.want {
			t.Fatalf("channels=%v", runner.Channels())
		}
	}
}

func TestFailedExecutableFallbackPreservesBothAttempts(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	runner := &Runner{timeout: time.Second}
	result, err := runner.Run(t.Context(), Request{Channel: "local", Probe: "hccn-pfc-stats", DeviceID: 1})
	if err == nil || len(result.Attempts) != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if !strings.Contains(result.Attempts[0].Command, "hccn_tool -i 1 -stat -g") || result.Attempts[0].Error == "" || result.Attempts[1].Error == "" {
		t.Fatalf("attempts=%+v", result.Attempts)
	}
}

func TestNetworkCounterProbesDoNotMutateKernelState(t *testing.T) {
	for name, expected := range map[string]string{"network-addresses": "ip -brief address", "network-routes": "ip route show table all", "network-sockets": "ss -s", "network-tcp-counters": "nstat -az", "rdma-links": "rdma link show", "rdma-counters": "rdma statistic show"} {
		commands, err := probeCommands(name, 0)
		if err != nil || len(commands) != 1 || displayCommand(commands[0]) != expected {
			t.Fatalf("probe=%s commands=%v err=%v", name, commands, err)
		}
		if !rootHelperProbes[name] {
			t.Fatalf("missing root helper profile %s", name)
		}
	}
}
