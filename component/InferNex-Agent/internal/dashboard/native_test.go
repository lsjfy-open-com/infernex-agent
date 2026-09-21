package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/kubeops"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/supervisor"
)

type nativeReaderStub struct {
	calls   atomic.Int32
	blocked chan struct{}
	request string
}

func (s *nativeReaderStub) ClusterOverview(context.Context) (kubeops.ClusterOverview, error) {
	s.calls.Add(1)
	if s.blocked != nil {
		<-s.blocked
	}
	return kubeops.ClusterOverview{KubernetesVersion: "v1.33", PodCount: 3, Warnings: []string{"list nodes: forbidden"}}, nil
}
func (s *nativeReaderStub) ListWorkloads(_ context.Context, r kubeops.WorkloadRequest) (kubeops.WorkloadInventory, error) {
	s.request = r.Namespace
	return kubeops.WorkloadInventory{Total: 1, Workloads: []kubeops.WorkloadSummary{{Kind: "Deployment", Namespace: "models", Name: "qwen", Desired: 2, Ready: 1}}}, nil
}
func TestNativeKubernetesAPIUsesScopeAndCache(t *testing.T) {
	store := supervisor.NewSnapshotStore("test", time.Minute, false)
	reader := &nativeReaderStub{}
	handler := New(store, WithKubernetes(reader, []string{"models"}))
	for range 2 {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/kubernetes", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("status %d", response.Code)
		}
		var data nativeSummary
		if err := json.Unmarshal(response.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		if data.Scope != "配置的巡检命名空间" || len(data.Workloads) != 1 || data.Workloads[0].Name != "qwen" || len(data.Warnings) != 1 {
			t.Fatalf("bad summary: %+v", data)
		}
	}
	if reader.calls.Load() != 1 || reader.request != "models" {
		t.Fatalf("cache/scope failed: calls=%d request=%q", reader.calls.Load(), reader.request)
	}
}
func TestNativeBlockingReaderDoesNotStackScans(t *testing.T) {
	store := supervisor.NewSnapshotStore("test", time.Minute, false)
	reader := &nativeReaderStub{blocked: make(chan struct{})}
	handler := New(store, WithKubernetes(reader, nil))
	defer close(reader.blocked)
	start := time.Now()
	for range 5 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/kubernetes", nil).WithContext(ctx))
		cancel()
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status %d", response.Code)
		}
	}
	if time.Since(start) > time.Second || reader.calls.Load() != 1 {
		t.Fatalf("blocking reader stacked scans: %d", reader.calls.Load())
	}
}
func TestNativeTimeoutKeepsPriorResult(t *testing.T) {
	reader := &nativeReaderStub{blocked: make(chan struct{})}
	cache := newNativeCache(reader, nil)
	cache.value = nativeSummary{Scope: "previous", UpdatedAt: time.Now().Add(-time.Minute), Workloads: []nativeWorkload{{Namespace: "models", Name: "prior"}}}
	defer close(reader.blocked)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	value, err := cache.get(ctx)
	if err != nil || !value.Stale || len(value.Workloads) != 1 || value.Workloads[0].Name != "prior" {
		t.Fatalf("stale data lost: %+v %v", value, err)
	}
}
