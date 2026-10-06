package main

import (
	"errors"
	"path/filepath"
	"testing"

	"k8s.io/client-go/rest"
)

func TestPrivateInventoryServerOptionValidation(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "private")
	tests := []struct {
		name string
		args []string
		ok   bool
	}{
		{name: "private only", args: []string{"--transport", "stdio", "--private-state-directory", abs, "--private-inventory-only"}, ok: true},
		{name: "mixed stdio", args: []string{"--transport", "stdio", "--private-state-directory", abs}, ok: true},
		{name: "http does not register but still parses", args: []string{"--transport", "streamable-http", "--private-state-directory", abs}, ok: true},
		{name: "relative state", args: []string{"--transport", "stdio", "--private-state-directory", "relative"}},
		{name: "only missing state", args: []string{"--transport", "stdio", "--private-inventory-only"}},
		{name: "only http", args: []string{"--transport", "streamable-http", "--private-state-directory", abs, "--private-inventory-only"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseServerOptions(test.args)
			if (err == nil) != test.ok {
				t.Fatalf("parse error = %v, want success %v", err, test.ok)
			}
		})
	}
}

func TestDockerOnlyPrivateStartupBypassesKubeConfig(t *testing.T) {
	want := errors.New("private runner reached")
	kubeCalls := 0
	originalKube := serverKubeConfig
	originalRunner := privateInventoryOnlyRunner
	t.Cleanup(func() {
		serverKubeConfig = originalKube
		privateInventoryOnlyRunner = originalRunner
	})
	serverKubeConfig = func(string) (*rest.Config, error) {
		kubeCalls++
		return nil, errors.New("kubeconfig must not be read")
	}
	privateInventoryOnlyRunner = func(options) error { return want }

	err := serveAgent(options{transport: "stdio", privateStateDirectory: "/private", privateInventoryOnly: true})
	if !errors.Is(err, want) || kubeCalls != 0 {
		t.Fatalf("serve error=%v kube calls=%d", err, kubeCalls)
	}
}
