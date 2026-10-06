package dockerdiscovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testDaemonID = "daemon-synthetic"
	testIDOne    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testIDTwo    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestClientUsesOnlyFixedReadOnlyPaths(t *testing.T) {
	t.Setenv("DOCKER_API_VERSION", "9.99")
	t.Setenv("HTTP_PROXY", "http://proxy.invalid:1")
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:1")

	var mu sync.Mutex
	var requests []string
	socket := newUnixHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		switch r.URL.RequestURI() {
		case "/version":
			writeJSON(t, w, map[string]any{"ApiVersion": "1.45", "MinAPIVersion": "1.24"})
		case "/v1.44/info":
			writeJSON(t, w, map[string]any{"ID": testDaemonID})
		case "/v1.44/containers/json?all=1":
			writeJSON(t, w, []map[string]any{
				{"Id": testIDOne, "Labels": map[string]string{"secret": "discard-me"}},
				{"Id": testIDTwo},
			})
		case "/v1.44/containers/" + testIDOne + "/json":
			writeJSON(t, w, inspectFixture(testIDOne, ""))
		default:
			http.NotFound(w, r)
		}
	}))

	client, err := NewClient(socket, testDaemonID)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	run, err := client.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer run.Close()
	if got := run.APIVersion(); got != engineAPIVersion {
		t.Fatalf("API version = %q, want %q", got, engineAPIVersion)
	}
	if got := run.DaemonID(); got != testDaemonID {
		t.Fatalf("daemon ID = %q, want %q", got, testDaemonID)
	}

	listed, err := run.ListContainers()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].id != testIDOne || listed[1].id != testIDTwo {
		t.Fatalf("unexpected list projection: %#v", listed)
	}
	inspect, err := run.InspectContainer(testIDOne)
	if err != nil {
		t.Fatal(err)
	}
	if inspect.id != testIDOne || inspect.name != "/synthetic" || inspect.imageReference != "example.invalid/image:v1" {
		t.Fatalf("unexpected inspect projection: %#v", inspect)
	}
	if got := strings.Join(inspect.ports, ","); got != "443/tcp,80/tcp" {
		t.Fatalf("ports = %q, want sorted projection", got)
	}

	want := []string{
		"GET /version",
		"GET /v1.44/info",
		"GET /v1.44/containers/json?all=1",
		"GET /v1.44/containers/" + testIDOne + "/json",
	}
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(requests) != fmt.Sprint(want) {
		t.Fatalf("requests = %v, want %v", requests, want)
	}
}

func TestClientRejectsNonUnixAndUncleanEndpoints(t *testing.T) {
	for _, endpoint := range []string{
		"",
		"relative/docker.sock",
		"http://127.0.0.1/docker.sock",
		"https://example.invalid",
		"ssh://example.invalid",
		"/tmp/../tmp/docker.sock",
		"/tmp/docker.sock/",
		"/tmp/docker\x00.sock",
	} {
		t.Run(strings.ReplaceAll(endpoint, "/", "_"), func(t *testing.T) {
			_, err := NewClient(endpoint, testDaemonID)
			assertCode(t, err, CodeInvalidArgument)
		})
	}
	for _, daemonID := range []string{"", " ", " daemon", "daemon "} {
		_, err := NewClient("/tmp/docker.sock", daemonID)
		assertCode(t, err, CodeInvalidArgument)
	}
}

func TestClientVersionNegotiationFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		min  any
		max  any
	}{
		{name: "minimum too new", min: "1.45", max: "1.50"},
		{name: "maximum too old", min: "1.20", max: "1.43"},
		{name: "missing minimum", min: nil, max: "1.44"},
		{name: "missing maximum", min: "1.24", max: nil},
		{name: "extra component", min: "1.24", max: "1.44.0"},
		{name: "leading zero", min: "1.24", max: "1.044"},
		{name: "space", min: "1.24", max: " 1.44"},
		{name: "non decimal", min: "1.24", max: "latest"},
		{name: "wrong json type", min: "1.24", max: 1.44},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var infoCalls atomic.Int32
			socket := newUnixHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/version" {
					response := map[string]any{}
					if test.min != nil {
						response["MinAPIVersion"] = test.min
					}
					if test.max != nil {
						response["ApiVersion"] = test.max
					}
					writeJSON(t, w, response)
					return
				}
				infoCalls.Add(1)
				http.Error(w, "must not be called", http.StatusInternalServerError)
			}))
			client, err := NewClient(socket, testDaemonID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Begin(context.Background())
			assertCode(t, err, CodeUnsupported)
			if calls := infoCalls.Load(); calls != 0 {
				t.Fatalf("info calls = %d, want 0", calls)
			}
		})
	}
}

func TestClientValidatesDaemonIdentityBeforeListing(t *testing.T) {
	for _, daemonID := range []string{"", "different-daemon"} {
		t.Run(daemonID, func(t *testing.T) {
			var listCalls atomic.Int32
			socket := newUnixHTTPServer(t, standardHandler(t, daemonID, nil, &listCalls))
			client, err := NewClient(socket, testDaemonID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Begin(context.Background())
			assertCode(t, err, CodeIdentityChanged)
			if calls := listCalls.Load(); calls != 0 {
				t.Fatalf("list calls = %d, want 0", calls)
			}
		})
	}
}

func TestInspectRequiresThisRunsCompleteListedID(t *testing.T) {
	var inspectCalls atomic.Int32
	socket := newUnixHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.RequestURI() {
		case "/version":
			writeJSON(t, w, map[string]string{"ApiVersion": "1.44", "MinAPIVersion": "1.44"})
		case "/v1.44/info":
			writeJSON(t, w, map[string]string{"ID": testDaemonID})
		case "/v1.44/containers/json?all=1":
			writeJSON(t, w, []map[string]string{{"Id": testIDOne}})
		case "/v1.44/containers/" + testIDOne + "/json":
			inspectCalls.Add(1)
			writeJSON(t, w, inspectFixture(testIDOne, ""))
		default:
			inspectCalls.Add(1)
			http.NotFound(w, r)
		}
	}))
	client, _ := NewClient(socket, testDaemonID)
	run, err := client.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer run.Close()

	for _, id := range []string{testIDOne, testIDTwo, "a", strings.Repeat("A", 64), testIDOne + "/logs"} {
		_, err := run.InspectContainer(id)
		assertCode(t, err, CodeInvalidArgument)
	}
	if calls := inspectCalls.Load(); calls != 0 {
		t.Fatalf("inspect calls before list = %d, want 0", calls)
	}
	if _, err := run.ListContainers(); err != nil {
		t.Fatal(err)
	}
	if _, err := run.ListContainers(); ErrorCodeOf(err) != CodeConflict {
		t.Fatalf("second list error = %v, want conflict", err)
	}
	if _, err := run.InspectContainer(testIDTwo); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("unlisted inspect error = %v", err)
	}
	if _, err := run.InspectContainer(testIDOne); err != nil {
		t.Fatal(err)
	}
	if calls := inspectCalls.Load(); calls != 1 {
		t.Fatalf("inspect calls = %d, want 1", calls)
	}
}

func TestRunListedIDsAreIsolatedAndRaceSafe(t *testing.T) {
	var listCalls atomic.Int32
	var inspectCalls atomic.Int32
	socket := newUnixHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.RequestURI() {
		case "/version":
			writeJSON(t, w, map[string]string{"ApiVersion": "1.44", "MinAPIVersion": "1.24"})
		case "/v1.44/info":
			writeJSON(t, w, map[string]string{"ID": testDaemonID})
		case "/v1.44/containers/json?all=1":
			if listCalls.Add(1) == 1 {
				writeJSON(t, w, []map[string]string{{"Id": testIDOne}})
			} else {
				writeJSON(t, w, []map[string]string{{"Id": testIDTwo}})
			}
		case "/v1.44/containers/" + testIDOne + "/json":
			inspectCalls.Add(1)
			writeJSON(t, w, inspectFixture(testIDOne, ""))
		case "/v1.44/containers/" + testIDTwo + "/json":
			inspectCalls.Add(1)
			writeJSON(t, w, inspectFixture(testIDTwo, ""))
		default:
			http.NotFound(w, r)
		}
	}))
	client, _ := NewClient(socket, testDaemonID)
	runOne, err := client.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer runOne.Close()
	runTwo, err := client.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer runTwo.Close()
	if _, err := runOne.ListContainers(); err != nil {
		t.Fatal(err)
	}
	if _, err := runTwo.ListContainers(); err != nil {
		t.Fatal(err)
	}
	if _, err := runTwo.InspectContainer(testIDOne); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("second run accepted first run ID: %v", err)
	}

	errorsFound := make(chan error, 32)
	var wait sync.WaitGroup
	for i := 0; i < 32; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, inspectErr := runOne.InspectContainer(testIDOne)
			errorsFound <- inspectErr
		}()
	}
	wait.Wait()
	close(errorsFound)
	for inspectErr := range errorsFound {
		if inspectErr != nil {
			t.Errorf("concurrent inspect: %v", inspectErr)
		}
	}
	if calls := inspectCalls.Load(); calls != 32 {
		t.Fatalf("inspect calls = %d, want 32", calls)
	}
}

func TestInspectDetectsContainerIDReplacement(t *testing.T) {
	socket := newUnixHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.RequestURI() {
		case "/version":
			writeJSON(t, w, map[string]string{"ApiVersion": "1.44", "MinAPIVersion": "1.24"})
		case "/v1.44/info":
			writeJSON(t, w, map[string]string{"ID": testDaemonID})
		case "/v1.44/containers/json?all=1":
			writeJSON(t, w, []map[string]string{{"Id": testIDOne}})
		case "/v1.44/containers/" + testIDOne + "/json":
			writeJSON(t, w, inspectFixture(testIDTwo, ""))
		}
	}))
	client, _ := NewClient(socket, testDaemonID)
	run, err := client.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer run.Close()
	if _, err := run.ListContainers(); err != nil {
		t.Fatal(err)
	}
	_, err = run.InspectContainer(testIDOne)
	assertCode(t, err, CodeConflict)
}

func TestRawResponseLimitAppliesToSuccessAndError(t *testing.T) {
	tests := []struct {
		name      string
		size      int64
		status    int
		wantCode  ErrorCode
		wantBegin bool
	}{
		{name: "success exactly limit", size: maxRawResponseSize, status: http.StatusOK, wantBegin: true},
		{name: "success limit plus one", size: maxRawResponseSize + 1, status: http.StatusOK, wantCode: CodeResponseTooLarge},
		{name: "error exactly limit", size: maxRawResponseSize, status: http.StatusInternalServerError, wantCode: CodeUnavailable},
		{name: "error limit plus one", size: maxRawResponseSize + 1, status: http.StatusInternalServerError, wantCode: CodeResponseTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := paddedVersionBody(t, test.size)
			socket := newUnixHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/version":
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(test.status)
					_, _ = w.Write(body)
				case "/v1.44/info":
					writeJSON(t, w, map[string]string{"ID": testDaemonID})
				}
			}))
			client, _ := NewClient(socket, testDaemonID)
			run, err := client.Begin(context.Background())
			if test.wantBegin {
				if err != nil {
					t.Fatal(err)
				}
				run.Close()
				return
			}
			assertCode(t, err, test.wantCode)
		})
	}
}

func TestResponseBodiesAreClosedOnEveryOutcome(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		contentLength int64
		body          string
	}{
		{name: "success", status: http.StatusOK, contentLength: -1, body: `{"ok":true}`},
		{name: "daemon error", status: http.StatusInternalServerError, contentLength: -1, body: `daemon secret`},
		{name: "declared oversize", status: http.StatusOK, contentLength: maxRawResponseSize + 1, body: `{}`},
		{name: "invalid json", status: http.StatusOK, contentLength: -1, body: `{`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := &trackingBody{Reader: strings.NewReader(test.body)}
			client := &Client{httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode:    test.status,
					ContentLength: test.contentLength,
					Body:          body,
					Header:        make(http.Header),
				}, nil
			})}}
			var target map[string]bool
			_ = client.getJSON(context.Background(), "/version", "version", &target)
			if !body.closed.Load() {
				t.Fatal("response body was not closed")
			}
		})
	}
}

func TestListDoesNotSilentlyTruncate(t *testing.T) {
	containers := make([]map[string]string, 301)
	for i := range containers {
		containers[i] = map[string]string{"Id": fmt.Sprintf("%064x", i+1)}
	}
	socket := newUnixHTTPServer(t, standardHandler(t, testDaemonID, containers, nil))
	client, _ := NewClient(socket, testDaemonID)
	run, err := client.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer run.Close()
	listed, err := run.ListContainers()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 301 {
		t.Fatalf("listed = %d, want 301 for downstream explicit partial handling", len(listed))
	}
}

func TestSecretsAndRawErrorsAreDiscarded(t *testing.T) {
	const marker = "secret-marker-7b77"
	socket := newUnixHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.RequestURI() {
		case "/version":
			writeJSON(t, w, map[string]string{"ApiVersion": "1.44", "MinAPIVersion": "1.24"})
		case "/v1.44/info":
			writeJSON(t, w, map[string]string{"ID": testDaemonID})
		case "/v1.44/containers/json?all=1":
			writeJSON(t, w, []map[string]any{{"Id": testIDOne, "Labels": map[string]string{"token": marker}}})
		case "/v1.44/containers/" + testIDOne + "/json":
			writeJSON(t, w, inspectFixture(testIDOne, marker))
		case "/v1.44/containers/" + testIDTwo + "/json":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(marker))
		default:
			http.NotFound(w, r)
		}
	}))
	client, _ := NewClient(socket, testDaemonID)
	run, err := client.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer run.Close()
	listed, err := run.ListContainers()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%#v", listed), marker) {
		t.Fatal("list projection retained a label secret")
	}
	inspect, err := run.InspectContainer(testIDOne)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%#v", inspect), marker) {
		t.Fatal("inspect projection retained Env, label, auth, command, source, binding, or option secret")
	}

	// Exercise a daemon-controlled error body through the same bounded reader.
	run.mu.Lock()
	run.listedIDs[testIDTwo] = struct{}{}
	run.mu.Unlock()
	_, err = run.InspectContainer(testIDTwo)
	if err == nil || strings.Contains(err.Error(), marker) || strings.Contains(fmt.Sprintf("%+v", err), marker) {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	var redirectedCalls atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedCalls.Add(1)
		writeJSON(t, w, map[string]string{"ApiVersion": "1.44", "MinAPIVersion": "1.24"})
	}))
	defer redirectTarget.Close()
	socket := newUnixHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL+"/stolen", http.StatusTemporaryRedirect)
	}))
	client, _ := NewClient(socket, testDaemonID)
	_, err := client.Begin(context.Background())
	assertCode(t, err, CodeInvalidResponse)
	if calls := redirectedCalls.Load(); calls != 0 {
		t.Fatalf("redirect target calls = %d, want 0", calls)
	}
}

func TestCancellationTimeoutAndCloseAreDistinct(t *testing.T) {
	for _, test := range []struct {
		name     string
		timeout  time.Duration
		cancel   bool
		closeRun bool
		wantCode ErrorCode
		wantIs   error
	}{
		{name: "caller cancel", timeout: time.Second, cancel: true, wantCode: CodeCanceled, wantIs: context.Canceled},
		{name: "run close", timeout: time.Second, closeRun: true, wantCode: CodeCanceled, wantIs: context.Canceled},
		{name: "run timeout", timeout: 20 * time.Millisecond, wantCode: CodeTimeout, wantIs: context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			listStarted := make(chan struct{})
			var once sync.Once
			socket := newUnixHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.RequestURI() {
				case "/version":
					writeJSON(t, w, map[string]string{"ApiVersion": "1.44", "MinAPIVersion": "1.24"})
				case "/v1.44/info":
					writeJSON(t, w, map[string]string{"ID": testDaemonID})
				case "/v1.44/containers/json?all=1":
					once.Do(func() { close(listStarted) })
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					if flusher, ok := w.(http.Flusher); ok {
						flusher.Flush()
					}
					<-r.Context().Done()
				}
			}))
			client, err := newClient(socket, testDaemonID, test.timeout)
			if err != nil {
				t.Fatal(err)
			}
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			run, err := client.Begin(parent)
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				_, listErr := run.ListContainers()
				result <- listErr
			}()
			<-listStarted
			if test.cancel {
				cancel()
			}
			if test.closeRun {
				run.Close()
			}
			select {
			case err := <-result:
				assertCode(t, err, test.wantCode)
				if !errors.Is(err, test.wantIs) {
					t.Fatalf("errors.Is(%v, %v) = false", err, test.wantIs)
				}
				if strings.Contains(err.Error(), socket) {
					t.Fatalf("error leaked socket path: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("list did not stop after cancellation")
			}
			run.Close()
		})
	}
}

func TestStatusAndConnectionErrorsAreBounded(t *testing.T) {
	for _, test := range []struct {
		status int
		code   ErrorCode
	}{
		{status: http.StatusUnauthorized, code: CodeForbidden},
		{status: http.StatusForbidden, code: CodeForbidden},
		{status: http.StatusNotFound, code: CodeNotFound},
		{status: http.StatusBadRequest, code: CodeInvalidResponse},
		{status: http.StatusServiceUnavailable, code: CodeUnavailable},
	} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			socket := newUnixHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte("daemon-secret-error"))
			}))
			client, _ := NewClient(socket, testDaemonID)
			_, err := client.Begin(context.Background())
			assertCode(t, err, test.code)
			if strings.Contains(err.Error(), "daemon-secret-error") {
				t.Fatalf("error leaked response: %v", err)
			}
		})
	}

	missing := filepath.Join(t.TempDir(), "private-socket-name")
	client, _ := NewClient(missing, testDaemonID)
	_, err := client.Begin(context.Background())
	assertCode(t, err, CodeUnavailable)
	if strings.Contains(err.Error(), missing) || strings.Contains(fmt.Sprintf("%+v", err), missing) {
		t.Fatalf("connection error leaked socket path: %v", err)
	}
}

func TestListRejectsInvalidOrDuplicateIDsWithoutInspectGrant(t *testing.T) {
	for _, ids := range [][]string{
		{"short"},
		{strings.Repeat("A", 64)},
		{testIDOne, testIDOne},
	} {
		t.Run(fmt.Sprint(ids), func(t *testing.T) {
			containers := make([]map[string]string, len(ids))
			for i, id := range ids {
				containers[i] = map[string]string{"Id": id}
			}
			socket := newUnixHTTPServer(t, standardHandler(t, testDaemonID, containers, nil))
			client, _ := NewClient(socket, testDaemonID)
			run, err := client.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer run.Close()
			_, err = run.ListContainers()
			assertCode(t, err, CodeInvalidResponse)
			_, err = run.InspectContainer(testIDOne)
			assertCode(t, err, CodeInvalidArgument)
		})
	}
}

func newUnixHTTPServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	// Darwin's sockaddr_un path is short enough that testing.T.TempDir paths can
	// exceed it once the test name is included.
	directory, err := os.MkdirTemp("/tmp", "dockerdiscovery-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	if err := server.Listener.Close(); err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return socket
}

func standardHandler(t *testing.T, daemonID string, containers any, listCalls *atomic.Int32) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.RequestURI() {
		case "/version":
			writeJSON(t, w, map[string]string{"ApiVersion": "1.44", "MinAPIVersion": "1.24"})
		case "/v1.44/info":
			writeJSON(t, w, map[string]string{"ID": daemonID})
		case "/v1.44/containers/json?all=1":
			if listCalls != nil {
				listCalls.Add(1)
			}
			if containers == nil {
				containers = []map[string]string{}
			}
			writeJSON(t, w, containers)
		default:
			http.NotFound(w, r)
		}
	})
}

func inspectFixture(id, marker string) map[string]any {
	return map[string]any{
		"Id":    id,
		"Name":  "/synthetic",
		"Image": "sha256:synthetic",
		"Config": map[string]any{
			"Image":      "example.invalid/image:v1",
			"Env":        []string{"TOKEN=" + marker},
			"Labels":     map[string]string{"credential": marker},
			"Cmd":        []string{"--password", marker},
			"Entrypoint": []string{marker},
		},
		"State": map[string]any{"Status": "running", "Running": true, "Error": marker},
		"HostConfig": map[string]any{
			"AuthConfig": map[string]string{"Password": marker},
			"DeviceRequests": []map[string]any{{
				"Driver":       "synthetic",
				"Count":        1,
				"DeviceIDs":    []string{"device-0"},
				"Capabilities": [][]string{{"compute"}},
				"Options":      map[string]string{"token": marker},
			}},
		},
		"RegistryConfig": map[string]string{"Auth": marker},
		"Mounts": []map[string]any{{
			"Type": "volume", "Name": "models", "Source": marker,
			"Destination": "/models", "Driver": "local", "Mode": "ro", "RW": false,
		}},
		"NetworkSettings": map[string]any{
			"Ports": map[string]any{
				"80/tcp":  []map[string]string{{"HostIp": marker, "HostPort": marker}},
				"443/tcp": nil,
			},
		},
	}
}

func paddedVersionBody(t *testing.T, size int64) []byte {
	t.Helper()
	prefix := []byte(`{"ApiVersion":"1.44","MinAPIVersion":"1.24","Padding":"`)
	suffix := []byte(`"}`)
	padding := size - int64(len(prefix)) - int64(len(suffix))
	if padding < 0 {
		t.Fatalf("requested body size %d is too small", size)
	}
	body := make([]byte, 0, size)
	body = append(body, prefix...)
	body = append(body, strings.Repeat("x", int(padding))...)
	body = append(body, suffix...)
	if int64(len(body)) != size {
		t.Fatalf("body size = %d, want %d", len(body), size)
	}
	return body
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode fixture: %v", err)
	}
}

func assertCode(t *testing.T, err error, want ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %s", want)
	}
	if got := ErrorCodeOf(err); got != want {
		t.Fatalf("error code = %q (%v), want %q", got, err, want)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type trackingBody struct {
	*strings.Reader
	closed atomic.Bool
}

func (body *trackingBody) Close() error {
	body.closed.Store(true)
	return nil
}
