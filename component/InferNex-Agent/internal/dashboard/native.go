package dashboard

import (
	"context"
	"net/http"
	"sync"
	"time"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/kubeops"
)

type KubernetesReader interface {
	ClusterOverview(context.Context) (kubeops.ClusterOverview, error)
	ListWorkloads(context.Context, kubeops.WorkloadRequest) (kubeops.WorkloadInventory, error)
	ListHelmReleases(context.Context, kubeops.HelmReleaseRequest) (kubeops.HelmReleaseList, error)
}
type nativeWorkload struct {
	Kind         string   `json:"kind"`
	Namespace    string   `json:"namespace"`
	Name         string   `json:"name"`
	Desired      int32    `json:"desired"`
	Ready        int32    `json:"ready"`
	Images       []string `json:"images,omitempty"`
	HelmRelease  string   `json:"helmRelease,omitempty"`
	HelmRevision int      `json:"helmRevision,omitempty"`
	LiveYAML     string   `json:"liveYaml,omitempty"`
}
type nativePod struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Phase     string `json:"phase"`
	Ready     bool   `json:"ready"`
	Restarts  int32  `json:"restarts"`
}
type nativeHelmRelease struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Revision  int    `json:"revision"`
	Status    string `json:"status,omitempty"`
	Storage   string `json:"storage"`
}
type nativeSummary struct {
	Scope             string              `json:"scope"`
	Namespaces        []string            `json:"namespaces"`
	KubernetesVersion string              `json:"kubernetesVersion,omitempty"`
	NodeCount         int                 `json:"nodeCount"`
	NamespaceCount    int                 `json:"namespaceCount"`
	PodCount          int                 `json:"podCount"`
	ReadyPodCount     int                 `json:"readyPodCount"`
	Workloads         []nativeWorkload    `json:"workloads"`
	Pods              []nativePod         `json:"pods"`
	HelmReleases      []nativeHelmRelease `json:"helmReleases"`
	HelmReleaseCount  int                 `json:"helmReleaseCount"`
	Total             int                 `json:"total"`
	WorkloadCount     int                 `json:"workloadCount"`
	PodCountInScope   int                 `json:"podCountInScope"`
	OverviewPartial   bool                `json:"overviewPartial"`
	ScopeTruncated    bool                `json:"scopeTruncated"`
	Stale             bool                `json:"stale"`
	Error             string              `json:"error,omitempty"`
	Truncated         bool                `json:"truncated"`
	Warnings          []string            `json:"warnings"`
	UpdatedAt         time.Time           `json:"updatedAt"`
}
type nativeCache struct {
	reader         KubernetesReader
	namespaces     []string
	scopeTruncated bool
	mu             sync.Mutex
	value          nativeSummary
	err            error
	expires        time.Time
	inflight       chan struct{}
}

func newNativeCache(reader KubernetesReader, namespaces []string) *nativeCache {
	copyNames := append([]string(nil), namespaces...)
	truncated := len(copyNames) > 16
	if len(copyNames) > 16 {
		copyNames = copyNames[:16]
	}
	return &nativeCache{reader: reader, namespaces: copyNames, scopeTruncated: truncated}
}
func (c *nativeCache) get(ctx context.Context) (nativeSummary, error) {
	c.mu.Lock()
	if time.Now().Before(c.expires) {
		v, e := c.value, c.err
		c.mu.Unlock()
		if e != nil && !v.UpdatedAt.IsZero() {
			v.Stale = true
			v.Error = "最近一次 Kubernetes 更新失败"
			return v, nil
		}
		return v, e
	}
	if c.inflight == nil {
		done := make(chan struct{})
		c.inflight = done
		go func() {
			workCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			v, e := c.collect(workCtx)
			c.mu.Lock()
			if e == nil {
				c.value = v
			}
			c.err = e
			c.expires = time.Now().Add(30 * time.Second)
			close(done)
			c.inflight = nil
			c.mu.Unlock()
		}()
	}
	done := c.inflight
	c.mu.Unlock()
	select {
	case <-done:
		c.mu.Lock()
		v, e := c.value, c.err
		c.mu.Unlock()
		if e != nil && !v.UpdatedAt.IsZero() {
			v.Stale = true
			v.Error = "最近一次 Kubernetes 更新失败"
			return v, nil
		}
		return v, e
	case <-ctx.Done():
		return c.stale(ctx.Err())
	case <-time.After(3 * time.Second):
		return c.stale(context.DeadlineExceeded)
	}
}
func (c *nativeCache) stale(err error) (nativeSummary, error) {
	c.mu.Lock()
	v := c.value
	c.mu.Unlock()
	if !v.UpdatedAt.IsZero() {
		v.Stale = true
		v.Error = "Kubernetes 更新超时，显示上次结果"
		return v, nil
	}
	return nativeSummary{}, err
}
func (c *nativeCache) collect(ctx context.Context) (nativeSummary, error) {
	v := nativeSummary{Namespaces: append([]string{}, c.namespaces...), Workloads: []nativeWorkload{}, Pods: []nativePod{}, HelmReleases: []nativeHelmRelease{}, Warnings: []string{}, UpdatedAt: time.Now().UTC(), ScopeTruncated: c.scopeTruncated}
	if len(c.namespaces) == 0 {
		v.Scope = "kubeconfig 可见范围（全部命名空间）"
	} else {
		v.Scope = "配置的巡检命名空间"
	}
	overview, err := c.reader.ClusterOverview(ctx)
	if err != nil {
		return v, err
	}
	v.KubernetesVersion = overview.KubernetesVersion
	v.NodeCount = len(overview.Nodes)
	v.NamespaceCount = overview.NamespaceCount
	v.PodCount = overview.PodCount
	v.ReadyPodCount = overview.ReadyPodCount
	if len(overview.Warnings) > 0 {
		v.OverviewPartial = true
		v.Warnings = append(v.Warnings, "集群概览部分资源读取受限")
	}
	if c.scopeTruncated {
		v.Warnings = append(v.Warnings, "配置的命名空间超过16个，仅展示前16个")
	}
	names := c.namespaces
	if len(names) == 0 {
		names = []string{""}
	}
	for _, name := range names {
		if ctx.Err() != nil {
			return v, ctx.Err()
		}
		inventory, e := c.reader.ListWorkloads(ctx, kubeops.WorkloadRequest{Namespace: name, Limit: 100})
		if e != nil {
			v.Warnings = append(v.Warnings, "读取工作负载失败: "+name)
			continue
		}
		if len(inventory.Warnings) > 0 {
			v.Warnings = append(v.Warnings, "命名空间资源读取受限")
		}
		v.WorkloadCount += len(inventory.Workloads)
		v.PodCountInScope += len(inventory.Pods)
		v.Total += inventory.Total
		v.Truncated = v.Truncated || inventory.Truncated
		for _, item := range inventory.Workloads {
			if len(v.Workloads) >= 100 {
				v.Truncated = true
				continue
			}
			images := kubeops.PublicWorkloadImages(item.Images)
			if len(images) > 20 {
				images = images[:20]
				v.Truncated = true
			}
			v.Workloads = append(v.Workloads, nativeWorkload{
				Kind: item.Kind, Namespace: item.Namespace, Name: item.Name, Desired: item.Desired,
				Ready: item.Ready, Images: images, HelmRelease: item.HelmRelease, LiveYAML: item.LiveYAML,
			})
		}
		for _, pod := range inventory.Pods {
			if len(v.Pods) >= 100 {
				v.Truncated = true
				continue
			}
			v.Pods = append(v.Pods, nativePod{Namespace: pod.Namespace, Name: pod.Name, Phase: pod.Phase, Ready: pod.Ready, Restarts: pod.Restarts})
		}
	}
	for _, name := range names {
		if ctx.Err() != nil {
			v.Warnings = append(v.Warnings, "Helm release 读取超时，工作负载结果可能仍可用")
			break
		}
		helm, e := c.reader.ListHelmReleases(ctx, kubeops.HelmReleaseRequest{Namespace: name, Limit: 100})
		if e != nil {
			v.Warnings = append(v.Warnings, "读取 Helm release 失败")
			continue
		}
		if len(helm.Warnings) > 0 {
			v.Warnings = append(v.Warnings, "Helm release 元数据读取受限")
		}
		v.HelmReleaseCount += helm.Total
		v.Truncated = v.Truncated || helm.Truncated
		for _, release := range helm.Releases {
			if len(v.HelmReleases) >= 100 {
				v.Truncated = true
				continue
			}
			v.HelmReleases = append(v.HelmReleases, nativeHelmRelease{
				Namespace: release.Namespace, Name: release.Name, Revision: release.Revision,
				Status: release.Status, Storage: release.Storage,
			})
		}
	}
	for index := range v.Workloads {
		workload := &v.Workloads[index]
		for _, release := range v.HelmReleases {
			if workload.Namespace == release.Namespace && workload.HelmRelease == release.Name {
				workload.HelmRevision = release.Revision
				break
			}
		}
	}
	v.UpdatedAt = time.Now().UTC()
	return v, nil
}
func (c *nativeCache) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if c == nil {
		http.Error(w, "native Kubernetes view is unavailable", http.StatusServiceUnavailable)
		return
	}
	v, e := c.get(r.Context())
	if e != nil {
		http.Error(w, "native Kubernetes view unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, v)
}
