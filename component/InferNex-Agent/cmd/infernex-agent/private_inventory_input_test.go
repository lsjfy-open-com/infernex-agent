package main

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/client-go/rest"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domain"
)

func TestPrivateConnectionInputIsExactAndProtected(t *testing.T) {
	directory := t.TempDir()
	valid := `{"runtime":"docker","endpoint":"/tmp/synthetic-docker.sock","hostID":"host-a","expectedDaemonID":"daemon-a","networkPolicy":"offline"}`
	tests := []struct {
		name string
		raw  string
		ok   bool
	}{
		{name: "valid", raw: valid, ok: true},
		{name: "case alias", raw: `{"Runtime":"docker","endpoint":"/tmp/synthetic-docker.sock","hostID":"host-a","expectedDaemonID":"daemon-a","networkPolicy":"offline"}`},
		{name: "duplicate", raw: `{"runtime":"docker","runtime":"kubernetes","endpoint":"/tmp/synthetic-docker.sock","hostID":"host-a","expectedDaemonID":"daemon-a","networkPolicy":"offline"}`},
		{name: "unknown", raw: `{"runtime":"docker","endpoint":"/tmp/synthetic-docker.sock","hostID":"host-a","expectedDaemonID":"daemon-a","networkPolicy":"offline","token":"secret"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(directory, test.name+".json")
			if err := os.WriteFile(path, []byte(test.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := readPrivateConnection(path)
			if (err == nil) != test.ok {
				t.Fatalf("read error=%v, want success=%v", err, test.ok)
			}
		})
	}

	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(target, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateConnection(link); err == nil {
		t.Fatal("symlink registration input accepted")
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateConnection(target); err == nil {
		t.Fatal("group/world-readable registration input accepted")
	}
}

func TestPrivateListFlagsDoNotWidenOnEmptyItems(t *testing.T) {
	if values, err := splitPrivateList("", false); err != nil || values != nil {
		t.Fatalf("unsupplied list = %v, %v", values, err)
	}
	for _, value := range []string{"", ",", "pods,,services", "pods,pods"} {
		if _, err := splitPrivateList(value, true); err == nil {
			t.Fatalf("explicit list %q was accepted", value)
		}
	}
	values, err := splitPrivateList("pods, services", true)
	if err != nil || len(values) != 2 || values[0] != "pods" || values[1] != "services" {
		t.Fatalf("valid list = %v, %v", values, err)
	}
}

func TestPinnedKubeConfigUsesVerifiedCABytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ca.pem")
	original := []byte("synthetic-ca-before")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	config := &rest.Config{Host: "https://api.invalid.example", TLSClientConfig: rest.TLSClientConfig{CAFile: path}}
	pinned, fingerprint, err := pinPrivateKubeConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("synthetic-ca-after"), 0o600); err != nil {
		t.Fatal(err)
	}
	if pinned.TLSClientConfig.CAFile != "" || string(pinned.TLSClientConfig.CAData) != string(original) || fingerprint == "" {
		t.Fatalf("unpinned Kubernetes config: file=%q ca=%q fingerprint=%q", pinned.TLSClientConfig.CAFile, pinned.TLSClientConfig.CAData, fingerprint)
	}
}

func TestPrivateAfterParserIsStrictAndScopeBound(t *testing.T) {
	const value = "InventorySnapshot/22222222-2222-4222-8222-222222222222/3"
	ref, err := parsePrivateAfter("scope-a", value)
	if err != nil {
		t.Fatal(err)
	}
	if ref.TenantScope != "scope-a" || ref.Kind != domain.KindInventorySnapshot || ref.ID != "22222222-2222-4222-8222-222222222222" || ref.Revision != 3 {
		t.Fatalf("after reference = %+v", ref)
	}
	for _, invalid := range []string{
		"", "InventorySnapshot", "InventorySnapshot/relative/path/1",
		"Unknown/22222222-2222-4222-8222-222222222222/1",
		"InventorySnapshot/22222222-2222-4222-8222-222222222222/0",
		"InventorySnapshot/22222222-2222-4222-8222-222222222222/01",
		"InventorySnapshot/22222222-2222-4222-8222-222222222222/+1",
		"InventorySnapshot/22222222-2222-4222-8222-222222222222/9007199254740992",
		"InventorySnapshot/22222222-2222-4222-8222-222222222222/1/extra",
	} {
		if _, err := parsePrivateAfter("scope-a", invalid); err == nil {
			t.Fatalf("invalid --after %q accepted", invalid)
		}
	}
}
