package dockerdiscovery

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/domain"
)

const (
	ResourceContainers = "containers"
	collectorVersion   = "docker-engine-api-v1.44"
	rulesetVersion     = "docker-projection-v1"
	timestampLayout    = "2006-01-02T15:04:05.000000000Z"
)

// DiscoverRequest contains only authorization and resource selection. The
// socket and expected daemon identity are supplied through the trusted Client
// constructor and can never be overridden by a discovery request.
type DiscoverRequest struct {
	PrincipalScope string
	ResourceKinds  []string
}

// Discoverer creates finalized, in-memory snapshot previews. It has no
// persistence or Docker mutation capability.
type Discoverer struct {
	client *Client
	now    func() time.Time
	newID  func() (string, error)
}

func NewDiscoverer(client *Client) (*Discoverer, error) {
	return newDiscoverer(client, time.Now, newUUID)
}

func newDiscoverer(client *Client, now func() time.Time, newID func() (string, error)) (*Discoverer, error) {
	if client == nil || now == nil || newID == nil {
		return nil, clientError("configure_discovery", CodeInvalidArgument)
	}
	return &Discoverer{client: client, now: now, newID: newID}, nil
}

// Discover reads the registered Docker daemon and returns a finalized preview.
// Authorization and environment validation always occur before network I/O.
// Caller cancellation returns no preview; bounded runtime failures are encoded
// as failed or partial coverage without retaining daemon error text.
func (d *Discoverer) Discover(ctx context.Context, environment domain.Environment, request DiscoverRequest) (*domain.InventorySnapshot, error) {
	if err := preflight(ctx, environment, request); err != nil {
		return nil, err
	}

	started := d.now().UTC()
	snapshotID, err := d.newID()
	if err != nil {
		return nil, clientError("create_snapshot", CodeUnavailable)
	}
	snapshot := newSnapshot(environment, snapshotID, started)
	issues := issueCollector{snapshot: snapshot, seen: make(map[string]struct{})}

	run, err := d.client.Begin(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		code, state, reason := discoveryOutcome(err)
		issues.add(code, domain.StageConnect, retryableIssue(code), issueDescription(code, domain.StageConnect))
		return d.finish(snapshot, state, reason)
	}
	defer run.Close()

	fingerprint, err := DockerIdentityFingerprint(environment.Spec.HostID, run.DaemonID())
	if err != nil || subtle.ConstantTimeCompare([]byte(fingerprint), []byte(environment.Spec.IdentityFingerprint)) != 1 {
		issues.add(domain.IssueIdentityChanged, domain.StageConnect, false, issueDescription(domain.IssueIdentityChanged, domain.StageConnect))
		return d.finish(snapshot, domain.CoverageFailed, domain.IssueIdentityChanged)
	}

	listed, err := run.ListContainers()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		code, state, reason := discoveryOutcome(err)
		issues.add(code, domain.StageList, retryableIssue(code), issueDescription(code, domain.StageList))
		return d.finish(snapshot, state, reason)
	}

	coverageState := domain.CoverageComplete
	var coverageReason domain.IssueCode
	limit := len(listed)
	if limit > domain.MaxEntities {
		limit = domain.MaxEntities
		coverageState = domain.CoveragePartial
		coverageReason = domain.IssueLimitExceeded
		issues.add(domain.IssueLimitExceeded, domain.StageList, false, issueDescription(domain.IssueLimitExceeded, domain.StageList))
	}

	for _, summary := range listed[:limit] {
		if contextErr := run.ctx.Err(); contextErr != nil {
			if errors.Is(contextErr, context.Canceled) {
				return nil, contextClientError("discover", contextErr)
			}
			coverageState = domain.CoverageTimeout
			coverageReason = domain.IssueTimeout
			issues.add(domain.IssueTimeout, domain.StageInspect, true, issueDescription(domain.IssueTimeout, domain.StageInspect))
			break
		}

		inspect, inspectErr := run.InspectContainer(summary.id)
		if inspectErr != nil {
			if errors.Is(inspectErr, context.Canceled) {
				return nil, inspectErr
			}
			issueCode, state, reason := discoveryOutcome(inspectErr)
			issues.add(issueCode, domain.StageInspect, retryableIssue(issueCode), issueDescription(issueCode, domain.StageInspect))
			coverageState = state
			coverageReason = reason
			if errors.Is(inspectErr, context.DeadlineExceeded) {
				break
			}
			continue
		}

		entity, normalizationIssues, ok := normalizeContainer(snapshot, environment, run.DaemonID(), inspect, formatTimestamp(d.now()))
		for _, issue := range normalizationIssues {
			issues.add(issue.Code, issue.Stage, issue.Retryable, issue.Description)
		}
		if len(normalizationIssues) > 0 && coverageState == domain.CoverageComplete {
			coverageState = domain.CoveragePartial
			coverageReason = normalizationIssues[0].Code
		}
		if !ok {
			coverageState = domain.CoveragePartial
			coverageReason = domain.IssueLimitExceeded
			continue
		}
		snapshot.Spec.Entities = append(snapshot.Spec.Entities, entity)
	}
	if contextErr := run.ctx.Err(); contextErr != nil {
		if errors.Is(contextErr, context.Canceled) {
			return nil, contextClientError("discover", contextErr)
		}
		if coverageState == domain.CoverageComplete {
			coverageState = domain.CoverageTimeout
			coverageReason = domain.IssueTimeout
			issues.add(domain.IssueTimeout, domain.StageNormalize, true, issueDescription(domain.IssueTimeout, domain.StageNormalize))
		}
	}

	if len(snapshot.Spec.Entities) == 0 && coverageState != domain.CoverageComplete {
		coverageState = failedCoverageState(coverageState)
	}
	return d.finish(snapshot, coverageState, coverageReason)
}

func preflight(ctx context.Context, environment domain.Environment, request DiscoverRequest) error {
	if ctx == nil {
		return clientError("authorize", CodeInvalidArgument)
	}
	if err := ctx.Err(); err != nil {
		return contextClientError("authorize", err)
	}
	if err := domain.VerifyRecord(&environment); err != nil {
		return clientError("authorize", CodeInvalidArgument)
	}
	if err := domain.AuthorizeScope(request.PrincipalScope, environment.TenantScope); err != nil {
		return clientError("authorize", CodeForbidden)
	}
	if !environment.Spec.Enabled {
		return clientError("authorize", CodeForbidden)
	}
	if environment.Spec.Runtime != domain.RuntimeDocker {
		return clientError("authorize", CodeInvalidArgument)
	}
	seen := map[string]bool{}
	resources := request.ResourceKinds
	if len(resources) == 0 {
		resources = []string{ResourceContainers}
	}
	for _, resource := range resources {
		if resource != ResourceContainers || seen[resource] {
			return clientError("authorize", CodeInvalidArgument)
		}
		seen[resource] = true
	}
	return nil
}

func newSnapshot(environment domain.Environment, id string, started time.Time) *domain.InventorySnapshot {
	timestamp := formatTimestamp(started)
	return &domain.InventorySnapshot{
		SchemaVersion: domain.SchemaVersion,
		Kind:          domain.KindInventorySnapshot,
		TenantScope:   environment.TenantScope,
		ID:            id,
		Revision:      1,
		CreatedAt:     timestamp,
		Spec: domain.SnapshotSpec{
			EnvironmentRef:   environment.Reference(),
			CollectorVersion: collectorVersion,
			RulesetVersion:   rulesetVersion,
			StartedAt:        timestamp,
			Coverage:         make([]domain.Coverage, 0, 1),
			Entities:         make([]domain.Entity, 0),
			Relations:        make([]domain.Relation, 0),
			Issues:           make([]domain.Issue, 0),
		},
	}
}

func (d *Discoverer) finish(snapshot *domain.InventorySnapshot, state domain.CoverageState, reason domain.IssueCode) (*domain.InventorySnapshot, error) {
	count := int64(len(snapshot.Spec.Entities))
	completeness := domain.CompletenessComplete
	if state != domain.CoverageComplete {
		if count == 0 {
			completeness = domain.CompletenessFailed
			state = failedCoverageState(state)
		} else {
			completeness = domain.CompletenessPartial
			if state == domain.CoverageFailed || state == domain.CoverageForbidden || state == domain.CoverageUnsupported {
				state = domain.CoveragePartial
			}
		}
	}
	snapshot.Spec.Coverage = append(snapshot.Spec.Coverage, domain.Coverage{
		ResourceKind: ResourceContainers,
		Namespace:    "",
		Count:        count,
		State:        state,
		Reason:       reason,
	})
	snapshot.Spec.Completeness = completeness
	finished := d.now().UTC()
	started, _ := time.Parse(timestampLayout, snapshot.Spec.StartedAt)
	if finished.Before(started) {
		finished = started
	}
	snapshot.Spec.FinishedAt = formatTimestamp(finished)
	if err := domain.FinalizeRecord(snapshot); err != nil {
		return nil, clientError("finalize", CodeInvalidResponse)
	}
	if err := domain.VerifyRecord(snapshot); err != nil {
		return nil, clientError("finalize", CodeInvalidResponse)
	}
	return snapshot, nil
}

func failedCoverageState(state domain.CoverageState) domain.CoverageState {
	if state == domain.CoverageForbidden || state == domain.CoverageUnsupported || state == domain.CoverageTimeout {
		return state
	}
	return domain.CoverageFailed
}

func discoveryOutcome(err error) (domain.IssueCode, domain.CoverageState, domain.IssueCode) {
	switch ErrorCodeOf(err) {
	case CodeForbidden:
		return domain.IssueForbidden, domain.CoverageForbidden, domain.IssueForbidden
	case CodeUnsupported:
		return domain.IssueUnsupported, domain.CoverageUnsupported, domain.IssueUnsupported
	case CodeIdentityChanged:
		return domain.IssueIdentityChanged, domain.CoverageFailed, domain.IssueIdentityChanged
	case CodeConflict:
		return domain.IssueConflict, domain.CoveragePartial, domain.IssueConflict
	case CodeResponseTooLarge:
		return domain.IssueLimitExceeded, domain.CoveragePartial, domain.IssueLimitExceeded
	case CodeNotFound:
		return domain.IssueNotFound, domain.CoveragePartial, domain.IssueNotFound
	case CodeTimeout:
		return domain.IssueTimeout, domain.CoverageTimeout, domain.IssueTimeout
	case CodeInvalidResponse:
		return domain.IssueUnparsed, domain.CoverageFailed, domain.IssueUnparsed
	default:
		return domain.IssueUnavailable, domain.CoverageFailed, domain.IssueUnavailable
	}
}

func retryableIssue(code domain.IssueCode) bool {
	return code == domain.IssueUnavailable || code == domain.IssueTimeout || code == domain.IssueNotFound
}

func issueDescription(code domain.IssueCode, stage domain.IssueStage) string {
	return "docker discovery " + string(stage) + " ended with " + string(code)
}

type issueCollector struct {
	snapshot *domain.InventorySnapshot
	seen     map[string]struct{}
}

func (c *issueCollector) add(code domain.IssueCode, stage domain.IssueStage, retryable bool, description string) {
	key := string(code) + "\x00" + string(stage) + "\x00" + description
	if _, duplicate := c.seen[key]; duplicate {
		return
	}
	c.seen[key] = struct{}{}
	if len(c.snapshot.Spec.Issues) >= domain.MaxIssues {
		c.snapshot.Spec.OmittedIssueCount++
		return
	}
	c.snapshot.Spec.Issues = append(c.snapshot.Spec.Issues, domain.Issue{
		Code:        code,
		Stage:       stage,
		Retryable:   retryable,
		Description: description,
	})
}

// DockerIdentityFingerprint computes the registered pd-json-v1 identity over
// exactly runtime, hostID, and daemonID. The Unix socket path is not identity.
func DockerIdentityFingerprint(hostID, daemonID string) (string, error) {
	if hostID == "" || daemonID == "" {
		return "", clientError("identity", CodeInvalidArgument)
	}
	identity := struct {
		Runtime  domain.Runtime `json:"runtime"`
		HostID   string         `json:"hostID"`
		DaemonID string         `json:"daemonID"`
	}{domain.RuntimeDocker, hostID, daemonID}
	raw, err := json.Marshal(identity)
	if err != nil {
		return "", clientError("identity", CodeInvalidArgument)
	}
	canonical, err := domain.CanonicalizeJSON(raw, domain.CanonicalOptions{})
	if err != nil {
		return "", clientError("identity", CodeInvalidArgument)
	}
	digest := sha256.Sum256(canonical)
	return domain.DigestPrefix + hex.EncodeToString(digest[:]), nil
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func formatTimestamp(value time.Time) string {
	return value.UTC().Format(timestampLayout)
}
