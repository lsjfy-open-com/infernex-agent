// Package kubernetesdiscovery projects a registered Kubernetes environment
// through the existing read-only kubeops boundary into domain snapshots.
package kubernetesdiscovery

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domain"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/kubeops"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

const (
	collectorVersion = "kubeops-projection-v1"
	rulesetVersion   = "kubernetes-domain-v1"
)

var (
	ErrCursorExpired      = errors.New("cursor_expired")
	ErrCursorUnauthorized = errors.New("cursor_unauthorized")
	ErrCursorCapacity     = errors.New("cursor_capacity_reached")
)

type ReadOnlyReader interface {
	ReadResources(context.Context, kubeops.ResourceReadRequest) (kubeops.ResourceReadResult, error)
}

type DiscoverRequest struct {
	PrincipalScope string   `json:"-"`
	Namespaces     []string `json:"namespaces,omitempty"`
	ResourceKinds  []string `json:"resourceKinds,omitempty"`
	IncludeNodes   bool     `json:"includeNodes,omitempty"`
	CursorHandle   string   `json:"cursorHandle,omitempty"`
}

type Result struct {
	Snapshot     *domain.InventorySnapshot `json:"snapshot"`
	CursorHandle string                    `json:"cursorHandle,omitempty"`
}

type resourceTask struct{ groupVersion, resource, namespace, token string }
type cursorState struct {
	scope       string
	environment domain.RecordRef
	tasks       []resourceTask
	index       int
	binding     string
	previous    domain.RecordRef
	expires     time.Time
}

type Discoverer struct {
	reader                 ReadOnlyReader
	clusterID, fingerprint string
	now                    func() time.Time
	newID                  func() (string, error)
	random                 func([]byte) error
	mu                     sync.Mutex
	cursors                map[string]cursorState
	perScope               map[string]int
}

func NewDiscoverer(reader ReadOnlyReader, clusterID, observedFingerprint string) (*Discoverer, error) {
	if reader == nil || strings.TrimSpace(clusterID) == "" || !validDigest(observedFingerprint) {
		return nil, fmt.Errorf("kubernetes discovery configuration is invalid")
	}
	return &Discoverer{reader: reader, clusterID: clusterID, fingerprint: observedFingerprint, now: time.Now, newID: newUUID, random: func(b []byte) error { _, err := rand.Read(b); return err }, cursors: map[string]cursorState{}, perScope: map[string]int{}}, nil
}

func (d *Discoverer) Discover(parent context.Context, env domain.Environment, req DiscoverRequest) (Result, error) {
	if parent == nil {
		return Result{}, fmt.Errorf("invalid discovery context")
	}
	if err := domain.VerifyRecord(&env); err != nil {
		return Result{}, fmt.Errorf("invalid registered environment")
	}
	if err := domain.AuthorizeScope(req.PrincipalScope, env.TenantScope); err != nil {
		return Result{}, fmt.Errorf("unauthorized discovery scope")
	}
	if env.Spec.Runtime != domain.RuntimeKubernetes || !env.Spec.Enabled || env.Spec.ClusterID != d.clusterID || subtle.ConstantTimeCompare([]byte(env.Spec.IdentityFingerprint), []byte(d.fingerprint)) != 1 {
		return Result{}, fmt.Errorf("registered Kubernetes identity changed")
	}
	ctx, cancel := context.WithTimeout(parent, domain.MaxScanDuration)
	defer cancel()
	var tasks []resourceTask
	var binding string
	var previous *domain.RecordRef
	if req.CursorHandle != "" {
		expectedBinding := ""
		if requestHasFilters(req) {
			requestedTasks, err := authorizedTasks(env, req)
			if err != nil {
				return Result{}, err
			}
			expectedBinding = taskBinding(requestedTasks)
		}
		state, err := d.takeCursor(req.CursorHandle, req.PrincipalScope, env.Reference(), expectedBinding)
		if err != nil {
			return Result{}, err
		}
		tasks = state.tasks[state.index:]
		tasks[0].token = state.tasks[state.index].token
		binding = state.binding
		p := state.previous
		previous = &p
	} else {
		var err error
		tasks, err = authorizedTasks(env, req)
		if err != nil {
			return Result{}, err
		}
		binding = taskBinding(tasks)
	}
	id, err := d.newID()
	if err != nil {
		return Result{}, fmt.Errorf("create snapshot identifier")
	}
	started := d.now().UTC()
	snapshot := newSnapshot(env, id, started, previous)
	entityCount := 0
	seenObjects := map[string]string{}
	hadFailure := false
	nextIndex := -1
	stop := false

	// A domain entity is at most 64 KiB and one Kubernetes object may also
	// produce one ownership entity. Fifteen objects therefore keep a projected
	// page below the 2 MiB contract including JSON framing.
	const maxObjectsPerProjectedPage = 15

discoveryLoop:
	for i := range tasks {
		token := tasks[i].token
		readCount := int64(0)
		taskState := domain.CoverageComplete
		var taskReason domain.IssueCode
		for {
			if ctx.Err() != nil {
				if errors.Is(parent.Err(), context.Canceled) {
					return Result{}, context.Canceled
				}
				addIssue(snapshot, domain.IssueTimeout, domain.StageList, true, "Kubernetes read timed out")
				taskState, taskReason, hadFailure = domain.CoverageTimeout, domain.IssueTimeout, true
				tasks[i].token, nextIndex, stop = token, i, true
				break
			}
			remaining := domain.MaxEntities - entityCount
			if remaining <= 0 {
				addIssue(snapshot, domain.IssueLimitExceeded, domain.StageNormalize, false, "entity limit reached")
				taskState, taskReason, hadFailure, stop = domain.CoveragePartial, domain.IssueLimitExceeded, true, true
				tasks[i].token, nextIndex = token, i
				break
			}
			limit := remaining / 2
			if limit < 1 {
				limit = 1
			}
			if limit > maxObjectsPerProjectedPage {
				limit = maxObjectsPerProjectedPage
			}
			result, readErr := d.reader.ReadResources(ctx, kubeops.ResourceReadRequest{GroupVersion: tasks[i].groupVersion, Resource: tasks[i].resource, Namespace: tasks[i].namespace, Limit: limit, Continue: token})
			if readErr != nil {
				if errors.Is(readErr, context.Canceled) && parent.Err() != nil {
					return Result{}, context.Canceled
				}
				code, state := classify(readErr)
				addIssue(snapshot, code, domain.StageList, code == domain.IssueTimeout || code == domain.IssueUnavailable, "Kubernetes resource read failed")
				taskState, taskReason, hadFailure = state, code, true
				if errors.Is(readErr, context.DeadlineExceeded) {
					tasks[i].token, nextIndex, stop = token, i, true
				}
				break
			}
			if len(result.Objects) > limit {
				addIssue(snapshot, domain.IssueLimitExceeded, domain.StageList, false, "Kubernetes reader exceeded the requested page limit")
				taskState, taskReason, hadFailure, stop = domain.CoveragePartial, domain.IssueLimitExceeded, true, true
				break
			}

			pageBytes := 2 // []
			pageInvalid := false
			for _, object := range result.Objects {
				logicalKey, uid, identified := objectIdentityKey(object)
				if identified {
					if priorUID, exists := seenObjects[logicalKey]; exists {
						appendBoundedIssue(snapshot, domain.Issue{Code: domain.IssueConflict, Stage: domain.StageNormalize, Description: "Kubernetes object identity changed or repeated during the scan"})
						taskState, taskReason, hadFailure = domain.CoveragePartial, domain.IssueConflict, true
						if priorUID == uid {
							readCount++
							continue
						}
					}
					seenObjects[logicalKey] = uid
				}
				entities, issues := normalizeObject(snapshot, env, tasks[i], object, domain.FormatTimestamp(d.now()))
				for _, issue := range issues {
					appendBoundedIssue(snapshot, issue)
					taskState, taskReason, hadFailure = domain.CoveragePartial, issue.Code, true
				}
				encoded, marshalErr := json.Marshal(entities)
				if marshalErr != nil || pageBytes+len(encoded) > domain.MaxRawResponseBytes {
					addIssue(snapshot, domain.IssueLimitExceeded, domain.StageNormalize, false, "projected page exceeded byte limit")
					taskState, taskReason, hadFailure, pageInvalid = domain.CoveragePartial, domain.IssueLimitExceeded, true, true
					break
				}
				pageBytes += len(encoded)
				if entityCount+len(entities) > domain.MaxEntities {
					addIssue(snapshot, domain.IssueLimitExceeded, domain.StageNormalize, false, "entity limit reached")
					taskState, taskReason, hadFailure, pageInvalid = domain.CoveragePartial, domain.IssueLimitExceeded, true, true
					break
				}
				snapshot.Spec.Entities = append(snapshot.Spec.Entities, entities...)
				entityCount += len(entities)
				readCount++
			}
			if pageInvalid {
				stop = true
				break
			}
			if result.Continue == "" {
				break
			}
			token = result.Continue
			if entityCount >= domain.MaxEntities {
				addIssue(snapshot, domain.IssueLimitExceeded, domain.StageList, false, "entity limit reached before pagination completed")
				taskState, taskReason, hadFailure, stop = domain.CoveragePartial, domain.IssueLimitExceeded, true, true
				tasks[i].token, nextIndex = token, i
				break
			}
		}
		snapshot.Spec.Coverage = append(snapshot.Spec.Coverage, domain.Coverage{ResourceKind: tasks[i].resource, Namespace: tasks[i].namespace, Count: readCount, State: taskState, Reason: taskReason})
		if stop {
			break discoveryLoop
		}
	}
	if len(snapshot.Spec.Coverage) == 0 {
		snapshot.Spec.Coverage = append(snapshot.Spec.Coverage, domain.Coverage{ResourceKind: "requested", Namespace: "", State: domain.CoverageFailed, Reason: domain.IssueUnavailable})
		hadFailure = true
	}
	if hadFailure {
		usableRead := len(snapshot.Spec.Entities) > 0
		for _, coverage := range snapshot.Spec.Coverage {
			usableRead = usableRead || coverage.State == domain.CoverageComplete || coverage.State == domain.CoveragePartial
		}
		if usableRead {
			snapshot.Spec.Completeness = domain.CompletenessPartial
		} else {
			snapshot.Spec.Completeness = domain.CompletenessFailed
		}
	} else {
		snapshot.Spec.Completeness = domain.CompletenessComplete
	}
	snapshot.Spec.FinishedAt = domain.FormatTimestamp(d.now())
	if err := domain.FinalizeRecord(snapshot); err != nil {
		return Result{}, fmt.Errorf("finalize Kubernetes snapshot: %w", err)
	}
	out := Result{Snapshot: snapshot}
	if nextIndex >= 0 {
		state := cursorState{scope: req.PrincipalScope, environment: env.Reference(), tasks: tasks, index: nextIndex, binding: binding, previous: snapshot.Reference(), expires: d.now().Add(domain.PreviewTTL)}
		handle, err := d.putCursor(state)
		if err != nil {
			return Result{}, err
		}
		out.CursorHandle = handle
	}
	return out, nil
}

func requestHasFilters(req DiscoverRequest) bool {
	return len(req.Namespaces) > 0 || len(req.ResourceKinds) > 0 || req.IncludeNodes
}

func taskBinding(tasks []resourceTask) string {
	parts := make([]string, 0, len(tasks))
	for _, task := range tasks {
		parts = append(parts, task.groupVersion+"\x00"+task.resource+"\x00"+task.namespace)
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x01")
}

func authorizedTasks(env domain.Environment, req DiscoverRequest) ([]resourceTask, error) {
	namespaces := req.Namespaces
	if len(namespaces) == 0 {
		namespaces = append([]string(nil), env.Spec.AllowedNamespaces...)
	}
	allowed := map[string]bool{}
	for _, ns := range env.Spec.AllowedNamespaces {
		allowed[ns] = true
	}
	seen := map[string]bool{}
	for _, ns := range namespaces {
		if !allowed[ns] || seen[ns] {
			return nil, fmt.Errorf("requested namespace is outside registered scope")
		}
		seen[ns] = true
	}
	defs := []resourceTask{{"apps/v1", "deployments", "", ""}, {"apps/v1", "statefulsets", "", ""}, {"v1", "pods", "", ""}, {"v1", "services", "", ""}, {"discovery.k8s.io/v1", "endpointslices", "", ""}, {"v1", "persistentvolumeclaims", "", ""}}
	wanted := map[string]bool{}
	if len(req.ResourceKinds) > 0 {
		for _, k := range req.ResourceKinds {
			normalized := strings.ToLower(strings.TrimSpace(k))
			if normalized == "" || wanted[normalized] {
				return nil, fmt.Errorf("resource kind is empty or duplicated")
			}
			wanted[normalized] = true
		}
	}
	var tasks []resourceTask
	for _, ns := range namespaces {
		for _, def := range defs {
			if len(wanted) > 0 && !wanted[def.resource] {
				continue
			}
			def.namespace = ns
			tasks = append(tasks, def)
		}
	}
	if req.IncludeNodes {
		tasks = append(tasks, resourceTask{"v1", "nodes", "", ""})
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("no authorized Kubernetes resources requested")
	}
	if len(wanted) > 0 {
		for k := range wanted {
			found := k == "nodes" && req.IncludeNodes
			for _, d := range defs {
				found = found || k == d.resource
			}
			if !found {
				return nil, fmt.Errorf("unsupported Kubernetes resource kind")
			}
		}
	}
	return tasks, nil
}

func newSnapshot(env domain.Environment, id string, started time.Time, previous *domain.RecordRef) *domain.InventorySnapshot {
	ts := domain.FormatTimestamp(started)
	return &domain.InventorySnapshot{SchemaVersion: domain.SchemaVersion, Kind: domain.KindInventorySnapshot, TenantScope: env.TenantScope, ID: id, Revision: 1, CreatedAt: ts, Spec: domain.SnapshotSpec{EnvironmentRef: env.Reference(), PreviousSnapshotRef: previous, CollectorVersion: collectorVersion, RulesetVersion: rulesetVersion, StartedAt: ts, Coverage: []domain.Coverage{}, Entities: []domain.Entity{}, Relations: []domain.Relation{}, Issues: []domain.Issue{}}}
}
func classify(err error) (domain.IssueCode, domain.CoverageState) {
	switch {
	case apierrors.IsForbidden(err):
		return domain.IssueForbidden, domain.CoverageForbidden
	case apierrors.IsNotFound(err):
		return domain.IssueUnsupported, domain.CoverageUnsupported
	case apierrors.IsResourceExpired(err):
		return domain.IssueCursorExpired, domain.CoverageFailed
	case errors.Is(err, context.DeadlineExceeded):
		return domain.IssueTimeout, domain.CoverageTimeout
	default:
		return domain.IssueUnavailable, domain.CoverageFailed
	}
}
func addIssue(s *domain.InventorySnapshot, code domain.IssueCode, stage domain.IssueStage, retry bool, description string) {
	appendBoundedIssue(s, domain.Issue{Code: code, Stage: stage, Retryable: retry, Description: description})
}
func appendBoundedIssue(s *domain.InventorySnapshot, i domain.Issue) {
	if len(s.Spec.Issues) < domain.MaxIssues {
		s.Spec.Issues = append(s.Spec.Issues, i)
	} else {
		s.Spec.OmittedIssueCount++
	}
}
func validDigest(s string) bool {
	if !strings.HasPrefix(s, domain.DigestPrefix) || len(s) != len(domain.DigestPrefix)+64 || s != strings.ToLower(s) {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(s, domain.DigestPrefix))
	return err == nil
}
func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}
func (d *Discoverer) putCursor(s cursorState) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.expireLocked()
	if d.perScope[s.scope] >= domain.MaxPreviewsPerSubject || len(d.cursors) >= domain.MaxPreviewsPerService {
		return "", ErrCursorCapacity
	}
	for attempt := 0; attempt < 8; attempt++ {
		raw := make([]byte, 32)
		if err := d.random(raw); err != nil {
			return "", fmt.Errorf("create cursor")
		}
		h := base64.RawURLEncoding.EncodeToString(raw)
		if _, exists := d.cursors[h]; exists {
			continue
		}
		d.cursors[h] = s
		d.perScope[s.scope]++
		return h, nil
	}
	return "", fmt.Errorf("create unique cursor")
}
func (d *Discoverer) takeCursor(h, scope string, env domain.RecordRef, expectedBinding string) (cursorState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.expireLocked()
	s, ok := d.cursors[h]
	if !ok {
		return cursorState{}, ErrCursorExpired
	}
	if s.scope != scope || !sameRef(s.environment, env) || (expectedBinding != "" && s.binding != expectedBinding) {
		return cursorState{}, ErrCursorUnauthorized
	}
	delete(d.cursors, h)
	d.perScope[s.scope]--
	return s, nil
}
func (d *Discoverer) expireLocked() {
	now := d.now()
	for h, s := range d.cursors {
		if !now.Before(s.expires) {
			delete(d.cursors, h)
			d.perScope[s.scope]--
		}
	}
}
func sameRef(a, b domain.RecordRef) bool {
	return a.TenantScope == b.TenantScope && a.Kind == b.Kind && a.ID == b.ID && a.Revision == b.Revision
}
