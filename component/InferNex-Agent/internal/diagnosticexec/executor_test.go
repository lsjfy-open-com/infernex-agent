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
