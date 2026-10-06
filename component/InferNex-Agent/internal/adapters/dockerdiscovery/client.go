// Package dockerdiscovery provides a deliberately narrow, read-only client for
// the Docker Engine API exposed by a registered local Unix socket.
package dockerdiscovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	engineAPIVersion   = "1.44"
	maxRawResponseSize = int64(2 << 20)
	maxRunDuration     = 30 * time.Second
)

// ErrorCode is a bounded classification. ClientError deliberately does not
// retain transport errors or daemon response bodies because either may contain
// a registered socket path or daemon-provided secret.
type ErrorCode string

const (
	CodeInvalidArgument  ErrorCode = "invalid_argument"
	CodeInvalidResponse  ErrorCode = "invalid_response"
	CodeUnsupported      ErrorCode = "unsupported"
	CodeIdentityChanged  ErrorCode = "identity_changed"
	CodeConflict         ErrorCode = "conflict"
	CodeResponseTooLarge ErrorCode = "response_too_large"
	CodeUnavailable      ErrorCode = "unavailable"
	CodeForbidden        ErrorCode = "forbidden"
	CodeNotFound         ErrorCode = "not_found"
	CodeCanceled         ErrorCode = "canceled"
	CodeTimeout          ErrorCode = "timeout"
)

// ClientError reports only a fixed operation and a bounded code. It never
// includes the socket path, request or response body, or an underlying error.
type ClientError struct {
	Operation string
	Code      ErrorCode
	cause     error
}

func (e *ClientError) Error() string {
	return fmt.Sprintf("docker engine %s: %s", e.Operation, e.Code)
}

// Unwrap preserves only the standard cancellation sentinels. Transport and
// protocol causes are intentionally not retained because their text can carry
// the registered socket path or daemon-controlled content.
func (e *ClientError) Unwrap() error {
	return e.cause
}

// ErrorCodeOf returns a stable code without exposing transport details.
func ErrorCodeOf(err error) ErrorCode {
	var clientErr *ClientError
	if errors.As(err, &clientErr) {
		return clientErr.Code
	}
	return ""
}

// Client can create independent read-only discovery runs. Authorization to
// inspect a container is held by Run, never by Client.
type Client struct {
	httpClient       *http.Client
	timeout          time.Duration
	expectedDaemonID string
}

// NewClient constructs a client for an explicitly registered absolute Unix
// socket and daemon identity. It cannot represent TCP, HTTP(S), or SSH targets.
func NewClient(socketPath, expectedDaemonID string) (*Client, error) {
	return newClient(socketPath, expectedDaemonID, maxRunDuration)
}

func newClient(socketPath, expectedDaemonID string, timeout time.Duration) (*Client, error) {
	if !validSocketPath(socketPath) || strings.TrimSpace(expectedDaemonID) == "" ||
		expectedDaemonID != strings.TrimSpace(expectedDaemonID) || timeout <= 0 || timeout > maxRunDuration {
		return nil, clientError("configure", CodeInvalidArgument)
	}

	dialer := &net.Dialer{}
	transport := &http.Transport{
		Proxy:              nil,
		DisableCompression: true,
		DisableKeepAlives:  true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	httpClient := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errRedirectBlocked
		},
	}

	return &Client{
		httpClient:       httpClient,
		timeout:          timeout,
		expectedDaemonID: expectedDaemonID,
	}, nil
}

func validSocketPath(path string) bool {
	return path != "" && !strings.ContainsRune(path, '\x00') && filepath.IsAbs(path) && filepath.Clean(path) == path
}

// CloseIdleConnections releases pooled Unix socket connections. It does not
// cancel runs; each Run owns its own context and Close method.
func (c *Client) CloseIdleConnections() {
	c.httpClient.CloseIdleConnections()
}

// Run is a single bounded discovery attempt. Its list authorization is local
// to the run, immutable after a successful list, and safe for concurrent
// inspection.
type Run struct {
	client *Client
	ctx    context.Context
	cancel context.CancelFunc

	apiVersion string
	daemonID   string

	mu          sync.RWMutex
	listStarted bool
	listedIDs   map[string]struct{}
}

// Begin negotiates exactly API v1.44 using the unversioned /version endpoint,
// then verifies /v1.44/info against the registered daemon identity. All later
// requests inherit one context with a maximum lifetime of 30 seconds.
func (c *Client) Begin(parent context.Context) (*Run, error) {
	if parent == nil {
		return nil, clientError("begin", CodeInvalidArgument)
	}
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	run := &Run{
		client: c,
		ctx:    ctx,
		cancel: cancel,
	}

	var version versionWire
	if err := c.getJSON(ctx, "/version", "version", &version); err != nil {
		cancel()
		return nil, err
	}
	minimum, minimumOK := versionString(version.MinAPIVersion)
	maximum, maximumOK := versionString(version.APIVersion)
	if !minimumOK || !maximumOK || !supportsFixedVersion(minimum, maximum) {
		cancel()
		return nil, clientError("version", CodeUnsupported)
	}
	run.apiVersion = engineAPIVersion

	var info infoWire
	if err := c.getJSON(ctx, "/v"+engineAPIVersion+"/info", "info", &info); err != nil {
		cancel()
		return nil, err
	}
	if strings.TrimSpace(info.ID) == "" || info.ID != c.expectedDaemonID {
		cancel()
		return nil, clientError("info", CodeIdentityChanged)
	}
	run.daemonID = info.ID
	return run, nil
}

// Close cancels outstanding work for this run. It is safe to call repeatedly.
func (r *Run) Close() {
	r.cancel()
}

// APIVersion is always the fixed negotiated version, never an environment
// override or the server's highest version.
func (r *Run) APIVersion() string {
	return r.apiVersion
}

// DaemonID returns the identity already matched to the registered daemon.
func (r *Run) DaemonID() string {
	return r.daemonID
}

// ListContainers performs the run's single container list. A failed or second
// list never grants inspect permission.
func (r *Run) ListContainers() ([]containerSummary, error) {
	r.mu.Lock()
	if r.listStarted {
		r.mu.Unlock()
		return nil, clientError("list", CodeConflict)
	}
	r.listStarted = true
	r.mu.Unlock()

	var wire []containerSummaryWire
	if err := r.client.getJSON(r.ctx, "/v"+engineAPIVersion+"/containers/json?all=1", "list", &wire); err != nil {
		return nil, err
	}

	listed := make(map[string]struct{}, len(wire))
	result := make([]containerSummary, 0, len(wire))
	for _, item := range wire {
		if !validContainerID(item.ID) {
			return nil, clientError("list", CodeInvalidResponse)
		}
		if _, duplicate := listed[item.ID]; duplicate {
			return nil, clientError("list", CodeInvalidResponse)
		}
		listed[item.ID] = struct{}{}
		result = append(result, containerSummary{id: item.ID})
	}

	r.mu.Lock()
	r.listedIDs = listed
	r.mu.Unlock()
	return result, nil
}

// InspectContainer reads only a complete, canonical ID returned by this Run's
// successful list. Invalid and stale IDs are rejected before network I/O.
func (r *Run) InspectContainer(id string) (containerInspect, error) {
	if !validContainerID(id) {
		return containerInspect{}, clientError("inspect", CodeInvalidArgument)
	}
	r.mu.RLock()
	_, listed := r.listedIDs[id]
	r.mu.RUnlock()
	if !listed {
		return containerInspect{}, clientError("inspect", CodeInvalidArgument)
	}

	var wire containerInspectWire
	path := "/v" + engineAPIVersion + "/containers/" + id + "/json"
	if err := r.client.getJSON(r.ctx, path, "inspect", &wire); err != nil {
		return containerInspect{}, err
	}
	if !validContainerID(wire.ID) {
		return containerInspect{}, clientError("inspect", CodeInvalidResponse)
	}
	if wire.ID != id {
		return containerInspect{}, clientError("inspect", CodeConflict)
	}

	ports := make([]string, 0, len(wire.NetworkSettings.Ports))
	for port := range wire.NetworkSettings.Ports {
		ports = append(ports, port)
	}
	sort.Strings(ports)

	mounts := make([]mountProjection, 0, len(wire.Mounts))
	for _, mount := range wire.Mounts {
		mounts = append(mounts, mountProjection{
			typeName:    mount.Type,
			name:        mount.Name,
			destination: mount.Destination,
			driver:      mount.Driver,
			mode:        mount.Mode,
			readWrite:   mount.RW,
			propagation: mount.Propagation,
		})
	}

	requests := make([]deviceRequestProjection, 0, len(wire.HostConfig.DeviceRequests))
	for _, request := range wire.HostConfig.DeviceRequests {
		requests = append(requests, deviceRequestProjection{
			driver:       request.Driver,
			count:        request.Count,
			deviceIDs:    append([]string(nil), request.DeviceIDs...),
			capabilities: cloneStringMatrix(request.Capabilities),
		})
	}

	return containerInspect{
		id:             wire.ID,
		name:           wire.Name,
		imageReference: wire.Config.Image,
		imageID:        wire.Image,
		state:          wire.State.Status,
		running:        wire.State.Running,
		ports:          ports,
		mounts:         mounts,
		deviceRequests: requests,
	}, nil
}

func cloneStringMatrix(values [][]string) [][]string {
	cloned := make([][]string, len(values))
	for i := range values {
		cloned[i] = append([]string(nil), values[i]...)
	}
	return cloned
}

func validContainerID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, char := range id {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func (c *Client) getJSON(ctx context.Context, path, operation string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return clientError(operation, CodeInvalidArgument)
	}
	request.Header.Set("Accept", "application/json")
	request.Close = true

	response, err := c.httpClient.Do(request)
	if err != nil {
		return classifyTransportError(ctx, operation, err)
	}
	defer response.Body.Close()

	if response.ContentLength > maxRawResponseSize {
		return clientError(operation, CodeResponseTooLarge)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxRawResponseSize+1))
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextClientError(operation, contextErr)
		}
		return clientError(operation, CodeInvalidResponse)
	}
	if int64(len(body)) > maxRawResponseSize {
		return clientError(operation, CodeResponseTooLarge)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return statusError(operation, response.StatusCode)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(target); err != nil {
		return clientError(operation, CodeInvalidResponse)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return clientError(operation, CodeInvalidResponse)
	}
	return nil
}

func classifyTransportError(ctx context.Context, operation string, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextClientError(operation, contextErr)
	}
	if errors.Is(err, os.ErrPermission) {
		return clientError(operation, CodeForbidden)
	}
	if errors.Is(err, errRedirectBlocked) {
		return clientError(operation, CodeInvalidResponse)
	}
	return clientError(operation, CodeUnavailable)
}

func contextClientError(operation string, err error) error {
	if errors.Is(err, context.Canceled) {
		return &ClientError{Operation: operation, Code: CodeCanceled, cause: context.Canceled}
	}
	return &ClientError{Operation: operation, Code: CodeTimeout, cause: context.DeadlineExceeded}
}

func statusError(operation string, status int) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return clientError(operation, CodeForbidden)
	case http.StatusNotFound:
		return clientError(operation, CodeNotFound)
	default:
		if status >= http.StatusInternalServerError {
			return clientError(operation, CodeUnavailable)
		}
		return clientError(operation, CodeInvalidResponse)
	}
}

func clientError(operation string, code ErrorCode) *ClientError {
	return &ClientError{Operation: operation, Code: code}
}

type apiVersion struct {
	major uint64
	minor uint64
}

func supportsFixedVersion(minimum, maximum string) bool {
	min, ok := parseAPIVersion(minimum)
	if !ok {
		return false
	}
	max, ok := parseAPIVersion(maximum)
	if !ok {
		return false
	}
	target, _ := parseAPIVersion(engineAPIVersion)
	return compareAPIVersion(min, target) <= 0 && compareAPIVersion(target, max) <= 0
}

func parseAPIVersion(value string) (apiVersion, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return apiVersion{}, false
	}
	if (len(parts[0]) > 1 && parts[0][0] == '0') || (len(parts[1]) > 1 && parts[1][0] == '0') {
		return apiVersion{}, false
	}
	major, err := strconv.ParseUint(parts[0], 10, 32)
	if err != nil {
		return apiVersion{}, false
	}
	minor, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		return apiVersion{}, false
	}
	return apiVersion{major: major, minor: minor}, true
}

func compareAPIVersion(left, right apiVersion) int {
	if left.major < right.major || (left.major == right.major && left.minor < right.minor) {
		return -1
	}
	if left == right {
		return 0
	}
	return 1
}

var errRedirectBlocked = errors.New("docker engine redirect blocked")

type versionWire struct {
	APIVersion    json.RawMessage `json:"ApiVersion"`
	MinAPIVersion json.RawMessage `json:"MinAPIVersion"`
}

func versionString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

type infoWire struct {
	ID string `json:"ID"`
}

type containerSummaryWire struct {
	ID string `json:"Id"`
}

type containerInspectWire struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Image  string `json:"Image"`
	Config struct {
		Image string `json:"Image"`
	} `json:"Config"`
	State struct {
		Status  string `json:"Status"`
		Running bool   `json:"Running"`
	} `json:"State"`
	HostConfig struct {
		DeviceRequests []struct {
			Driver       string     `json:"Driver"`
			Count        int64      `json:"Count"`
			DeviceIDs    []string   `json:"DeviceIDs"`
			Capabilities [][]string `json:"Capabilities"`
		} `json:"DeviceRequests"`
	} `json:"HostConfig"`
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Destination string `json:"Destination"`
		Driver      string `json:"Driver"`
		Mode        string `json:"Mode"`
		RW          bool   `json:"RW"`
		Propagation string `json:"Propagation"`
	} `json:"Mounts"`
	NetworkSettings struct {
		Ports map[string]json.RawMessage `json:"Ports"`
	} `json:"NetworkSettings"`
}

// The projections intentionally have no Env, Labels, AuthConfig, command,
// entrypoint, mount source, device options, network binding, or raw JSON field.
type containerSummary struct {
	id string
}

type containerInspect struct {
	id             string
	name           string
	imageReference string
	imageID        string
	state          string
	running        bool
	ports          []string
	mounts         []mountProjection
	deviceRequests []deviceRequestProjection
}

type mountProjection struct {
	typeName    string
	name        string
	destination string
	driver      string
	mode        string
	readWrite   bool
	propagation string
}

type deviceRequestProjection struct {
	driver       string
	count        int64
	deviceIDs    []string
	capabilities [][]string
}
