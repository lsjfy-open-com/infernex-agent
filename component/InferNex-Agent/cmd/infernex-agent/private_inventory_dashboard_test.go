package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/rest"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domain"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domainstore"
)

func TestPrivateInventoryDashboardOptions(t *testing.T) {
	absolute := t.TempDir()
	base := []string{"--state-dir", filepath.Join(absolute, "state"), "--token-file", filepath.Join(absolute, "token")}
	tests := []struct {
		name    string
		extra   []string
		wantErr bool
	}{
		{name: "default loopback HTTP"},
		{name: "IPv6 loopback HTTP", extra: []string{"--listen-address", "[::1]:8081"}},
		{name: "remote TLS", extra: []string{"--listen-address", "192.0.2.10:8443", "--tls-cert", "/etc/infernex/tls.crt", "--tls-key", "/etc/infernex/tls.key"}},
		{name: "wildcard HTTP", extra: []string{"--listen-address", "0.0.0.0:8081"}, wantErr: true},
		{name: "IPv6 wildcard HTTP", extra: []string{"--listen-address", "[::]:8081"}, wantErr: true},
		{name: "remote HTTP", extra: []string{"--listen-address", "192.0.2.10:8081"}, wantErr: true},
		{name: "ambiguous hostname", extra: []string{"--listen-address", "localhost:8081"}, wantErr: true},
		{name: "empty host", extra: []string{"--listen-address", ":8081"}, wantErr: true},
		{name: "missing port", extra: []string{"--listen-address", "127.0.0.1"}, wantErr: true},
		{name: "zero port", extra: []string{"--listen-address", "127.0.0.1:0"}, wantErr: true},
		{name: "noncanonical port", extra: []string{"--listen-address", "127.0.0.1:08081"}, wantErr: true},
		{name: "partial TLS", extra: []string{"--tls-cert", "/etc/infernex/tls.crt"}, wantErr: true},
		{name: "relative TLS", extra: []string{"--tls-cert", "tls.crt", "--tls-key", "tls.key"}, wantErr: true},
		{name: "trailing argument", extra: []string{"unexpected"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			arguments := append(append([]string(nil), base...), test.extra...)
			options, err := parsePrivateInventoryDashboardOptions(arguments)
			if (err != nil) != test.wantErr {
				t.Fatalf("parse error = %v, want error %v", err, test.wantErr)
			}
			if err == nil && test.name == "default loopback HTTP" && options.listenAddress != defaultPrivateDashboardListen {
				t.Fatalf("default listen address = %q", options.listenAddress)
			}
		})
	}

	if _, err := parsePrivateInventoryDashboardOptions([]string{"--state-dir", "relative", "--token-file", filepath.Join(absolute, "token")}); err == nil {
		t.Fatal("relative state directory accepted")
	}
	if _, err := parsePrivateInventoryDashboardOptions([]string{"--state-dir", filepath.Join(absolute, "state"), "--token-file", "relative"}); err == nil {
		t.Fatal("relative token file accepted")
	}
}

func TestPrivateInventoryDashboardTokenIsProtectedStrictHex(t *testing.T) {
	directory := t.TempDir()
	validToken := strings.Repeat("ab", 32)
	validPath := filepath.Join(directory, "valid-token")
	if err := os.WriteFile(validPath, []byte(validToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := readPrivateInventoryDashboardToken(validPath)
	if err != nil || token != validToken {
		t.Fatalf("valid token = %q, %v", token, err)
	}

	invalid := []struct {
		name string
		raw  string
		mode os.FileMode
	}{
		{name: "too short", raw: strings.Repeat("ab", 31), mode: 0o600},
		{name: "odd length", raw: strings.Repeat("ab", 32) + "a", mode: 0o600},
		{name: "not hex", raw: strings.Repeat("zz", 32), mode: 0o600},
		{name: "leading whitespace", raw: " " + validToken, mode: 0o600},
		{name: "extra newline", raw: validToken + "\n\n", mode: 0o600},
		{name: "read only", raw: validToken, mode: 0o400},
		{name: "group readable", raw: validToken, mode: 0o640},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(directory, strings.ReplaceAll(test.name, " ", "-"))
			if err := os.WriteFile(path, []byte(test.raw), test.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, test.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := readPrivateInventoryDashboardToken(path); err == nil {
				t.Fatal("invalid token file accepted")
			}
		})
	}

	symlink := filepath.Join(directory, "token-link")
	if err := os.Symlink(validPath, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateInventoryDashboardToken(symlink); err == nil {
		t.Fatal("symlink token file accepted")
	}
}

func TestPrivateInventoryDashboardStartupBypassesKubernetes(t *testing.T) {
	token := strings.Repeat("cd", 32)
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}

	originalOpen := openPrivateInventoryDashboardStore
	originalRunner := runPrivateInventoryDashboardServer
	originalKubeConfig := serverKubeConfig
	t.Cleanup(func() {
		openPrivateInventoryDashboardStore = originalOpen
		runPrivateInventoryDashboardServer = originalRunner
		serverKubeConfig = originalKubeConfig
	})

	store := &privateDashboardFakeStore{scope: "scope-a"}
	opened := false
	openPrivateInventoryDashboardStore = func(stateDirectory string, ownerUID int) (privateInventoryDashboardStore, error) {
		opened = true
		if stateDirectory != "/private/state" || ownerUID != os.Geteuid() {
			t.Fatalf("open state=%q owner=%d", stateDirectory, ownerUID)
		}
		return store, nil
	}
	kubeCalls := 0
	serverKubeConfig = func(string) (*rest.Config, error) {
		kubeCalls++
		return nil, errors.New("Kubernetes configuration must not be loaded")
	}
	served := false
	runPrivateInventoryDashboardServer = func(ctx context.Context, options privateInventoryDashboardOptions, handler http.Handler) error {
		served = true
		if ctx == nil || options.listenAddress != defaultPrivateDashboardListen {
			t.Fatalf("server options = %+v", options)
		}

		unauthorized := httptest.NewRecorder()
		handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/inventory/records", nil))
		if unauthorized.Code != http.StatusUnauthorized {
			t.Fatalf("unauthorized API status = %d", unauthorized.Code)
		}
		authorizedRequest := httptest.NewRequest(http.MethodGet, "/api/v1/inventory/records", nil)
		authorizedRequest.Header.Set("Authorization", "Bearer "+token)
		authorized := httptest.NewRecorder()
		handler.ServeHTTP(authorized, authorizedRequest)
		if authorized.Code != http.StatusOK {
			t.Fatalf("authorized API status = %d body=%s", authorized.Code, authorized.Body.String())
		}
		page := httptest.NewRecorder()
		handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
		if page.Code != http.StatusOK {
			t.Fatalf("dashboard page status = %d", page.Code)
		}
		return nil
	}

	err := runPrivateInventory([]string{"dashboard", "--state-dir", "/private/state", "--token-file", tokenPath})
	if err != nil {
		t.Fatal(err)
	}
	if !opened || !served || kubeCalls != 0 || store.listScopes != 1 {
		t.Fatalf("opened=%v served=%v kubeCalls=%d listCalls=%d", opened, served, kubeCalls, store.listScopes)
	}
}

func TestPrivateInventoryDashboardServerShutsDownWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := servePrivateInventoryDashboard(ctx, privateInventoryDashboardOptions{listenAddress: "127.0.0.1:0"}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("canceled dashboard server handled a request")
	}))
	if err != nil {
		t.Fatalf("graceful shutdown error = %v", err)
	}
}

type privateDashboardFakeStore struct {
	scope      string
	listScopes int
}

func (store *privateDashboardFakeStore) Scope() string { return store.scope }

func (store *privateDashboardFakeStore) ListPage(_ context.Context, scope string, _ domainstore.ListOptions) (domainstore.ListPage, error) {
	if scope != store.scope {
		return domainstore.ListPage{}, errors.New("unexpected scope")
	}
	store.listScopes++
	return domainstore.ListPage{Entries: []domainstore.ListEntry{}}, nil
}

func (*privateDashboardFakeStore) Get(context.Context, string, domain.RecordRef) (domain.Record, error) {
	return nil, errors.New("not found")
}
