//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"k8s.io/client-go/rest"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domain"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domainstore"
)

const privateLifecycleContainerID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestPrivateInventoryCLILifecycleAndStableListTraversal(t *testing.T) {
	stateDirectory := filepath.Join(t.TempDir(), "private-state")
	socket, dockerRequests := newPrivateLifecycleDockerServer(t)

	originalKubeConfig := serverKubeConfig
	kubeCalls := 0
	serverKubeConfig = func(string) (*rest.Config, error) {
		kubeCalls++
		return nil, errors.New("Kubernetes configuration must not be loaded")
	}
	t.Cleanup(func() { serverKubeConfig = originalKubeConfig })

	var initialized struct {
		Scope string `json:"scope"`
	}
	runPrivateCLIJSON(t, &initialized, "init", "--state-dir", stateDirectory, "--scope", "tenant-a")
	if initialized.Scope != "tenant-a" {
		t.Fatalf("initialized scope = %q", initialized.Scope)
	}

	connectionPath := filepath.Join(t.TempDir(), "docker-connection.json")
	connection := fmt.Sprintf(`{"runtime":"docker","endpoint":%q,"hostID":"host-a","expectedDaemonID":"daemon-a","networkPolicy":"offline"}`, socket)
	if err := os.WriteFile(connectionPath, []byte(connection), 0o600); err != nil {
		t.Fatal(err)
	}

	var environment domain.Environment
	runPrivateCLIJSON(t, &environment, "environment", "register", "--state-dir", stateDirectory, "--input", connectionPath)
	if environment.Kind != domain.KindEnvironment || environment.Revision != 1 || environment.Spec.Runtime != domain.RuntimeDocker {
		t.Fatalf("registered Environment = %+v", environment)
	}
	if got := dockerRequests(); len(got) != 0 {
		t.Fatalf("offline registration contacted Docker: %v", got)
	}

	var shownEnvironment domain.Environment
	runPrivateCLIJSON(t, &shownEnvironment, "environment", "show", "--state-dir", stateDirectory, "--id", environment.ID)
	if shownEnvironment.Reference() != environment.Reference() || shownEnvironment.Digest != environment.Digest {
		t.Fatalf("shown Environment differs from registration: %+v", shownEnvironment)
	}

	var preview privateDiscoverOutput
	runPrivateCLIJSON(t, &preview, "discover", "--state-dir", stateDirectory, "--environment-id", environment.ID, "--revision", "1")
	if preview.SavedRef != nil || preview.Snapshot == nil || preview.Snapshot.Spec.EnvironmentRef != environment.Reference() {
		t.Fatalf("unexpected unsaved preview: %+v", preview)
	}
	if preview.Snapshot.Spec.Completeness != domain.CompletenessComplete || len(preview.Snapshot.Spec.Entities) != 1 {
		t.Fatalf("preview completeness=%q entities=%d", preview.Snapshot.Spec.Completeness, len(preview.Snapshot.Spec.Entities))
	}
	assertPrivateLifecycleListCount(t, stateDirectory, 1)

	var saved privateDiscoverOutput
	runPrivateCLIJSON(t, &saved, "discover", "--state-dir", stateDirectory, "--environment-id", environment.ID, "--revision", "1", "--save")
	if saved.SavedRef == nil || saved.Snapshot == nil || *saved.SavedRef != saved.Snapshot.Reference() {
		t.Fatalf("saved discovery output = %+v", saved)
	}

	var shownSnapshot domain.InventorySnapshot
	runPrivateCLIJSON(t, &shownSnapshot, "show", "--state-dir", stateDirectory,
		"--kind", string(domain.KindInventorySnapshot), "--id", saved.SavedRef.ID, "--revision", "1")
	if shownSnapshot.Reference() != *saved.SavedRef || shownSnapshot.Digest != saved.Snapshot.Digest {
		t.Fatalf("shown snapshot differs from saved preview: %+v", shownSnapshot)
	}

	var verified struct {
		Reference domain.RecordRef `json:"reference"`
		Valid     bool             `json:"valid"`
	}
	runPrivateCLIJSON(t, &verified, "verify", "--state-dir", stateDirectory,
		"--kind", string(domain.KindInventorySnapshot), "--id", saved.SavedRef.ID, "--revision", "1")
	if !verified.Valid || verified.Reference != *saved.SavedRef {
		t.Fatalf("verify output = %+v", verified)
	}

	reopened, err := domainstore.Open(stateDirectory, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	record, err := reopened.Get(context.Background(), reopened.Scope(), *saved.SavedRef)
	if err != nil {
		t.Fatal(err)
	}
	persisted, ok := record.(*domain.InventorySnapshot)
	if !ok || persisted.Digest != saved.Snapshot.Digest {
		t.Fatalf("reopened snapshot = %#v", record)
	}

	for range 4 {
		var extra domain.Environment
		runPrivateCLIJSON(t, &extra, "environment", "register", "--state-dir", stateDirectory, "--input", connectionPath)
	}
	expectedPage, err := reopened.ListPage(context.Background(), reopened.Scope(), domainstore.ListOptions{Limit: domain.MaxPageSize})
	if err != nil {
		t.Fatal(err)
	}
	if expectedPage.HasMore || len(expectedPage.Entries) != 6 {
		t.Fatalf("complete expected page hasMore=%v records=%d", expectedPage.HasMore, len(expectedPage.Entries))
	}

	var traversed []domain.RecordRef
	var after *domain.RecordRef
	for {
		args := []string{"list", "--state-dir", stateDirectory, "--limit", "2"}
		if after != nil {
			args = append(args, "--after", fmt.Sprintf("%s/%s/%d", after.Kind, after.ID, after.Revision))
		}
		var page struct {
			Records []domainstore.ListEntry `json:"records"`
			HasMore bool                    `json:"hasMore"`
			NextRef *domain.RecordRef       `json:"nextRef"`
		}
		runPrivateCLIJSON(t, &page, args...)
		for _, entry := range page.Records {
			traversed = append(traversed, entry.Reference)
		}
		if !page.HasMore {
			if page.NextRef != nil {
				t.Fatalf("terminal page exposed nextRef: %+v", page.NextRef)
			}
			break
		}
		if page.NextRef == nil || page.NextRef.TenantScope != reopened.Scope() {
			t.Fatalf("non-terminal page has invalid nextRef: %+v", page.NextRef)
		}
		after = page.NextRef
	}
	wantRefs := make([]domain.RecordRef, len(expectedPage.Entries))
	for index := range expectedPage.Entries {
		wantRefs[index] = expectedPage.Entries[index].Reference
	}
	if !reflect.DeepEqual(traversed, wantRefs) {
		t.Fatalf("traversed references = %v, want %v", traversed, wantRefs)
	}

	badAfter := fmt.Sprintf("%s/%s/%d", domain.KindEnvironment, environment.ID, environment.Revision)
	if _, err := capturePrivateCLIOutput(func() error {
		return runPrivateInventory([]string{"list", "--state-dir", stateDirectory, "--kind", string(domain.KindInventorySnapshot), "--after", badAfter})
	}); err == nil {
		t.Fatal("list accepted an --after kind that conflicts with --kind")
	}

	wantDockerRequests := []string{
		"GET /version", "GET /v1.44/info", "GET /v1.44/containers/json?all=1", "GET /v1.44/containers/" + privateLifecycleContainerID + "/json",
		"GET /version", "GET /v1.44/info", "GET /v1.44/containers/json?all=1", "GET /v1.44/containers/" + privateLifecycleContainerID + "/json",
	}
	if got := dockerRequests(); !reflect.DeepEqual(got, wantDockerRequests) {
		t.Fatalf("Docker requests = %v, want %v", got, wantDockerRequests)
	}
	if kubeCalls != 0 {
		t.Fatalf("Kubernetes configuration calls = %d, want 0", kubeCalls)
	}
}

func assertPrivateLifecycleListCount(t *testing.T, stateDirectory string, count int) {
	t.Helper()
	var page struct {
		Records []domainstore.ListEntry `json:"records"`
		HasMore bool                    `json:"hasMore"`
		NextRef *domain.RecordRef       `json:"nextRef"`
	}
	runPrivateCLIJSON(t, &page, "list", "--state-dir", stateDirectory, "--limit", "100")
	if len(page.Records) != count || page.HasMore || page.NextRef != nil {
		t.Fatalf("list records=%d hasMore=%v nextRef=%+v, want %d terminal records", len(page.Records), page.HasMore, page.NextRef, count)
	}
}

func runPrivateCLIJSON(t *testing.T, output any, args ...string) {
	t.Helper()
	raw, err := capturePrivateCLIOutput(func() error { return runPrivateInventory(args) })
	if err != nil {
		t.Fatalf("private-inventory %v: %v", args, err)
	}
	if err := json.Unmarshal(raw, output); err != nil {
		t.Fatalf("decode private-inventory %v output %q: %v", args, raw, err)
	}
}

func capturePrivateCLIOutput(run func() error) ([]byte, error) {
	previous := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	os.Stdout = writer
	readResult := make(chan struct {
		data []byte
		err  error
	}, 1)
	go func() {
		data, readErr := io.ReadAll(reader)
		readResult <- struct {
			data []byte
			err  error
		}{data, readErr}
	}()

	runErr := run()
	os.Stdout = previous
	closeErr := writer.Close()
	result := <-readResult
	readCloseErr := reader.Close()
	if result.err != nil {
		return nil, result.err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if readCloseErr != nil {
		return nil, readCloseErr
	}
	return result.data, runErr
}

func newPrivateLifecycleDockerServer(t *testing.T) (string, func() []string) {
	t.Helper()
	directory, err := os.MkdirTemp("", "infernex-private-docker-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	requests := make([]string, 0, 8)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		requests = append(requests, request.Method+" "+request.URL.RequestURI())
		mu.Unlock()
		if request.Method != http.MethodGet {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		var response any
		switch request.URL.RequestURI() {
		case "/version":
			response = map[string]string{"ApiVersion": "1.44", "MinAPIVersion": "1.24"}
		case "/v1.44/info":
			response = map[string]string{"ID": "daemon-a"}
		case "/v1.44/containers/json?all=1":
			response = []map[string]string{{"Id": privateLifecycleContainerID}}
		case "/v1.44/containers/" + privateLifecycleContainerID + "/json":
			response = map[string]any{
				"Id": privateLifecycleContainerID, "Name": "/synthetic", "Image": "sha256:synthetic",
				"Config":     map[string]any{"Image": "example.invalid/image:v1", "Env": []string{"TOKEN=discard"}},
				"State":      map[string]any{"Status": "running", "Running": true},
				"HostConfig": map[string]any{"DeviceRequests": []any{}},
				"Mounts":     []any{}, "NetworkSettings": map[string]any{"Ports": map[string]any{}},
			}
		default:
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewEncoder(writer).Encode(response); err != nil {
			t.Errorf("encode Docker fixture response: %v", err)
		}
	}))
	if err := server.Listener.Close(); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)

	return socket, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), requests...)
	}
}
