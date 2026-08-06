/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 * You can use this software according to the terms and conditions of the Mulan PSL v2.
 * You may obtain a copy of Mulan PSL v2 at:
 *          http://license.coscl.org.cn/MulanPSL2
 * THIS SOFTWARE IS PROVIDED ON AN "AS IS" BASIS, WITHOUT WARRANTIES OF ANY KIND,
 * EITHER EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO NON-INFRINGEMENT,
 * MERCHANTABILITY OR FIT FOR A PARTICULAR PURPOSE.
 * See the Mulan PSL v2 for more details.
 */

package experiment

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infernexv1alpha1 "gitcode.com/openFuyao/InferNex/api/v1alpha1"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/changesafety"
	"gitcode.com/openFuyao/InferNex/component/InferNex-Agent/internal/diagnostics"
)

const (
	managedByLabel          = "app.kubernetes.io/managed-by"
	managedByAgent          = "infernex-agent"
	experimentIDLabel       = "agent.infernex.io/experiment-id"
	changeIDAnnotation      = "agent.infernex.io/change-id"
	baselineAnnotation      = "agent.infernex.io/experiment-baseline"
	featureAnnotation       = "agent.infernex.io/experiment-feature"
	stageAnnotation         = "agent.infernex.io/experiment-stage"
	maxFeatureProfiles      = 16
	maxPlansReturned        = 100
	defaultReadinessTimeout = 20 * time.Minute
	defaultSoakDuration     = 5 * time.Minute
	defaultPollInterval     = 5 * time.Second
	defaultDiagnosticPeriod = 30 * time.Second
)

type Controller struct {
	client    client.Client
	changes   changesafety.Store
	plans     Store
	diagnoser diagnostics.Diagnoser
	config    Config
	now       func() time.Time

	createMu   sync.Mutex
	mu         sync.Mutex
	runContext context.Context
	running    map[string]struct{}
}

func New(
	kubeClient client.Client,
	changeStore changesafety.Store,
	planStore Store,
	diagnoser diagnostics.Diagnoser,
	config Config,
) (*Controller, error) {
	if kubeClient == nil {
		return nil, fmt.Errorf("Kubernetes client is required")
	}
	if changeStore == nil {
		changeStore = changesafety.NewMemoryStore()
	}
	if planStore == nil {
		planStore = NewMemoryStore()
	}
	if diagnoser == nil {
		return nil, fmt.Errorf("diagnostics collector is required for experiments")
	}
	config.TemplateNamespace = strings.TrimSpace(config.TemplateNamespace)
	if problems := validation.IsDNS1123Label(config.TemplateNamespace); len(problems) > 0 {
		return nil, fmt.Errorf(
			"invalid experiment template namespace %q: %s",
			config.TemplateNamespace,
			strings.Join(problems, "; "),
		)
	}
	if config.ReadinessTimeout <= 0 {
		config.ReadinessTimeout = defaultReadinessTimeout
	}
	if config.SoakDuration <= 0 {
		config.SoakDuration = defaultSoakDuration
	}
	if config.PollInterval <= 0 {
		config.PollInterval = defaultPollInterval
	}
	if config.DiagnosticInterval <= 0 {
		config.DiagnosticInterval = defaultDiagnosticPeriod
	}
	if config.DiagnosticsMinutes <= 0 {
		config.DiagnosticsMinutes = 15
	}
	return &Controller{
		client:    kubeClient,
		changes:   changeStore,
		plans:     planStore,
		diagnoser: diagnoser,
		config:    config,
		now:       time.Now,
		running:   make(map[string]struct{}),
	}, nil
}

func (c *Controller) Start(ctx context.Context) error {
	c.mu.Lock()
	c.runContext = ctx
	c.mu.Unlock()
	pending, err := c.plans.Pending()
	if err != nil {
		return fmt.Errorf("load pending experiment plans: %w", err)
	}
	if len(pending) > 1 {
		return fmt.Errorf("found %d pending experiment plans; refusing unsafe parallel resume", len(pending))
	}
	for _, plan := range pending {
		c.startPlan(plan.ID)
	}
	return nil
}

func (c *Controller) Create(ctx context.Context, request Request) (Plan, error) {
	c.createMu.Lock()
	defer c.createMu.Unlock()
	request, err := validateRequest(request)
	if err != nil {
		return Plan{}, err
	}
	pending, err := c.plans.Pending()
	if err != nil {
		return Plan{}, fmt.Errorf("check active experiment plans: %w", err)
	}
	if len(pending) > 0 {
		return Plan{}, fmt.Errorf(
			"experiment %s is still %s; this Agent runs one progressive plan at a time",
			pending[0].ID,
			pending[0].Status,
		)
	}
	baseline, err := c.getReadyBaseline(ctx, request.Namespace, request.BaselineName)
	if err != nil {
		return Plan{}, err
	}
	if err := validateBaselineSpec(baseline); err != nil {
		return Plan{}, err
	}
	for _, profile := range request.FeatureProfiles {
		if _, err := c.getApprovedFeature(ctx, profile); err != nil {
			return Plan{}, err
		}
	}

	id, err := changesafety.NewID()
	if err != nil {
		return Plan{}, err
	}
	createdAt := c.now().UTC()
	plan := Plan{
		APIVersion:      "agent.infernex.io/v1alpha1",
		Kind:            "InferNexExperiment",
		ID:              id,
		Namespace:       request.Namespace,
		BaselineName:    request.BaselineName,
		CandidatePrefix: request.CandidatePrefix,
		FeatureProfiles: append([]string(nil), request.FeatureProfiles...),
		Status:          PlanStatusPlanned,
		CurrentStage:    0,
		StableService:   request.BaselineName,
		CreatedAt:       createdAt,
		UpdatedAt:       createdAt,
		Message:         "experiment plan validated; no resource has been created yet",
		Stages:          make([]Stage, len(request.FeatureProfiles)),
	}
	stableName := request.BaselineName
	for index, profile := range request.FeatureProfiles {
		candidateName := candidateName(request.CandidatePrefix, index)
		if problems := validation.IsDNS1123Subdomain(candidateName); len(problems) > 0 {
			return Plan{}, fmt.Errorf("generated candidate name %q is invalid: %s", candidateName, strings.Join(problems, "; "))
		}
		if err := c.requireAbsent(ctx, request.Namespace, candidateName); err != nil {
			return Plan{}, err
		}
		plan.Stages[index] = Stage{
			Index:          index,
			FeatureProfile: profile,
			BaselineName:   stableName,
			CandidateName:  candidateName,
			Status:         StageStatusPending,
		}
		stableName = candidateName
	}
	if err := c.plans.Append(plan); err != nil {
		return Plan{}, fmt.Errorf("persist validated experiment plan: %w", err)
	}
	plan.Status = PlanStatusRunning
	plan.UpdatedAt = c.now().UTC()
	plan.Message = "experiment is running; one approved feature will be evaluated at a time"
	if err := c.plans.Append(plan); err != nil {
		return Plan{}, fmt.Errorf("start experiment plan: %w", err)
	}
	c.startPlan(plan.ID)
	return plan, nil
}

func (c *Controller) Get(_ context.Context, id string) (Plan, error) {
	return c.plans.Latest(strings.TrimSpace(id))
}

func (c *Controller) List(_ context.Context) ([]Plan, error) {
	plans, err := c.plans.List()
	if err != nil {
		return nil, err
	}
	if len(plans) > maxPlansReturned {
		plans = plans[len(plans)-maxPlansReturned:]
	}
	return plans, nil
}

func (c *Controller) startPlan(id string) {
	c.mu.Lock()
	if _, found := c.running[id]; found {
		c.mu.Unlock()
		return
	}
	c.running[id] = struct{}{}
	ctx := c.runContext
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Unlock()

	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.running, id)
			c.mu.Unlock()
		}()
		if err := c.runPlan(ctx, id); err != nil && ctx.Err() == nil {
			slog.Error("experiment plan stopped", "experimentId", id, "error", err)
		}
	}()
}

func (c *Controller) runPlan(ctx context.Context, id string) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		plan, err := c.plans.Latest(id)
		if err != nil {
			return err
		}
		if plan.Status == PlanStatusCompleted || plan.Status == PlanStatusFailed {
			return nil
		}
		if plan.Status == PlanStatusPlanned {
			plan.Status = PlanStatusRunning
			plan.Message = "resumed experiment after Agent restart"
			if err := c.savePlan(&plan); err != nil {
				return err
			}
		}
		if plan.CurrentStage >= len(plan.Stages) {
			plan.Status = PlanStatusCompleted
			plan.Message = "all approved feature stages passed readiness, diagnostics comparison, and soak gates"
			return c.savePlan(&plan)
		}
		if err := c.runStage(ctx, &plan, plan.CurrentStage); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
	}
}

func (c *Controller) runStage(ctx context.Context, plan *Plan, stageIndex int) error {
	stage := &plan.Stages[stageIndex]
	if stage.Status == StageStatusPassed {
		plan.StableService = stage.CandidateName
		plan.CurrentStage = stageIndex + 1
		return c.savePlan(plan)
	}
	if stage.Status == StageStatusRolledBack {
		plan.Status = PlanStatusFailed
		plan.Message = "experiment stopped after a failed stage; the previous stable service was retained"
		return c.savePlan(plan)
	}

	if err := c.ensureCandidate(ctx, plan, stage); err != nil {
		return c.failStage(context.Background(), plan, stage, err.Error())
	}
	return c.monitorStage(ctx, plan, stage)
}

func (c *Controller) ensureCandidate(ctx context.Context, plan *Plan, stage *Stage) error {
	baseline, err := c.getReadyBaseline(ctx, plan.Namespace, stage.BaselineName)
	if err != nil {
		return err
	}
	if err := validateBaselineSpec(baseline); err != nil {
		return err
	}
	if _, err := c.getApprovedFeature(ctx, stage.FeatureProfile); err != nil {
		return err
	}
	if stage.ChangeID == "" {
		stage.ChangeID, err = changesafety.NewID()
		if err != nil {
			return err
		}
		stage.StartedAt = c.now().UTC()
		stage.Status = StageStatusCreating
		stage.Message = "persisting the candidate before creation"
		if err := c.savePlan(plan); err != nil {
			return err
		}
	}

	desired := candidateService(plan, *stage, baseline)
	desiredRaw, err := json.Marshal(desired)
	if err != nil {
		return fmt.Errorf("encode candidate InferNexService: %w", err)
	}
	change := changesafety.ChangeRecord{
		APIVersion: "agent.infernex.io/v1alpha1",
		Kind:       "InferNexChange",
		ID:         stage.ChangeID,
		Action:     "experiment-stage",
		Status:     changesafety.StatusPlanned,
		Target: changesafety.Target{
			APIVersion: infernexv1alpha1.GroupVersion.String(),
			Kind:       "InferNexService",
			Namespace:  plan.Namespace,
			Name:       stage.CandidateName,
		},
		Desired:    desiredRaw,
		OccurredAt: stage.StartedAt,
		Message:    fmt.Sprintf("baseline %s retained; adding only approved feature profile %s", stage.BaselineName, stage.FeatureProfile),
	}

	key := types.NamespacedName{Namespace: plan.Namespace, Name: stage.CandidateName}
	current := &infernexv1alpha1.InferNexService{}
	if getErr := c.client.Get(ctx, key, current); getErr == nil {
		if err := verifyOwnedCandidate(current, plan.ID, stage.ChangeID); err != nil {
			return err
		}
		if !equality.Semantic.DeepEqual(current.Spec, desired.Spec) {
			return fmt.Errorf(
				"experiment candidate %s has spec drift; refusing to monitor or overwrite it",
				key,
			)
		}
		stage.Status = StageStatusWaiting
		stage.Message = "resumed monitoring of the existing owned candidate"
		return c.savePlan(plan)
	} else if !apierrors.IsNotFound(getErr) {
		return fmt.Errorf("check candidate InferNexService %s: %w", key, getErr)
	}

	if _, latestErr := c.changes.Latest(stage.ChangeID); latestErr != nil {
		if err := c.changes.Append(change); err != nil {
			return fmt.Errorf("persist pre-experiment change record: %w", err)
		}
	}
	if err := c.client.Create(ctx, desired); err != nil {
		change.Status = changesafety.StatusApplyFailed
		change.OccurredAt = c.now().UTC()
		change.Message = err.Error()
		_ = c.changes.Append(change)
		return fmt.Errorf("create experiment candidate %s: %w", key, err)
	}
	change.Status = changesafety.StatusApplied
	change.OccurredAt = c.now().UTC()
	change.Message = "candidate created; readiness and diagnostic gates started"
	if err := c.changes.Append(change); err != nil {
		deleteErr := c.deleteOwnedCandidate(context.Background(), key, plan.ID, stage.ChangeID)
		if deleteErr != nil {
			return fmt.Errorf("persist candidate creation: %v; emergency rollback: %w", err, deleteErr)
		}
		return fmt.Errorf("persist candidate creation: %w; candidate was rolled back", err)
	}
	stage.Status = StageStatusWaiting
	stage.Message = "waiting for the candidate to become Ready"
	return c.savePlan(plan)
}

func (c *Controller) monitorStage(ctx context.Context, plan *Plan, stage *Stage) error {
	deadline := stage.StartedAt.Add(c.config.ReadinessTimeout)
	nextDiagnostic := time.Time{}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		now := c.now().UTC()
		if !now.Before(deadline) {
			return c.failStage(context.Background(), plan, stage, fmt.Sprintf(
				"candidate did not pass all gates within %s", c.config.ReadinessTimeout,
			))
		}
		baseline, err := c.getReadyBaseline(ctx, plan.Namespace, stage.BaselineName)
		if err != nil {
			return c.failStage(context.Background(), plan, stage, "baseline became unhealthy; experiment result is inconclusive: "+err.Error())
		}
		_ = baseline
		candidate := &infernexv1alpha1.InferNexService{}
		key := types.NamespacedName{Namespace: plan.Namespace, Name: stage.CandidateName}
		if err := c.client.Get(ctx, key, candidate); err != nil {
			if apierrors.IsNotFound(err) {
				return c.failStage(context.Background(), plan, stage, "candidate disappeared before the experiment completed")
			}
			if err := waitContext(ctx, c.config.PollInterval); err != nil {
				return err
			}
			continue
		}
		if err := verifyOwnedCandidate(candidate, plan.ID, stage.ChangeID); err != nil {
			return c.failStage(context.Background(), plan, stage, err.Error())
		}
		if terminalDegraded(candidate) {
			return c.failStage(context.Background(), plan, stage, "candidate reported a terminal Degraded condition")
		}
		candidateReady := candidate.Status.Ready && candidate.Status.ObservedGeneration >= candidate.Generation
		if nextDiagnostic.IsZero() || !now.Before(nextDiagnostic) {
			baselineReport, baselineErr := c.diagnoser.Diagnose(ctx, diagnostics.Request{
				Namespace: plan.Namespace, Name: stage.BaselineName,
				SinceMinutes: c.config.DiagnosticsMinutes,
			})
			candidateReport, candidateErr := c.diagnoser.Diagnose(ctx, diagnostics.Request{
				Namespace: plan.Namespace, Name: stage.CandidateName,
				SinceMinutes: c.config.DiagnosticsMinutes,
			})
			if baselineErr != nil || candidateErr != nil {
				stage.Comparison = nil
				stage.Message = fmt.Sprintf("diagnostic comparison will retry: baseline=%v candidate=%v", baselineErr, candidateErr)
				if err := c.savePlan(plan); err != nil {
					return err
				}
			} else {
				comparison := diagnostics.Compare(baselineReport, candidateReport)
				if !comparison.Healthy {
					stage.Comparison = &comparison
					return c.failStage(context.Background(), plan, stage, "candidate introduced critical diagnostic categories: "+strings.Join(comparison.RegressionCategories, ", "))
				}
				if candidateReady {
					stage.Comparison = &comparison
					stage.Message = "candidate remains Ready with no critical diagnostic regression against the stable baseline"
				} else {
					stage.Message = "candidate is not Ready yet; pre-readiness diagnostics show no critical regression"
				}
				if err := c.savePlan(plan); err != nil {
					return err
				}
			}
			nextDiagnostic = now.Add(c.config.DiagnosticInterval)
		}
		if !candidateReady {
			if !stage.ReadyAt.IsZero() {
				return c.failStage(context.Background(), plan, stage, "candidate lost readiness during the soak window")
			}
			if err := waitContext(ctx, c.config.PollInterval); err != nil {
				return err
			}
			continue
		}
		if stage.ReadyAt.IsZero() {
			stage.ReadyAt = now
			stage.Comparison = nil
			stage.Status = StageStatusSoaking
			stage.Message = fmt.Sprintf("candidate is Ready; comparing it with %s for %s", stage.BaselineName, c.config.SoakDuration)
			if err := c.savePlan(plan); err != nil {
				return err
			}
		}
		if stage.Comparison != nil && !now.Before(stage.ReadyAt.Add(c.config.SoakDuration)) {
			return c.passStage(plan, stage)
		}
		if err := waitContext(ctx, c.config.PollInterval); err != nil {
			return err
		}
	}
}

func (c *Controller) passStage(plan *Plan, stage *Stage) error {
	record, err := c.changes.Latest(stage.ChangeID)
	if err != nil {
		return c.failStage(context.Background(), plan, stage, "cannot load stage change record: "+err.Error())
	}
	record.Status = changesafety.StatusCommitted
	record.OccurredAt = c.now().UTC()
	record.Message = "candidate passed readiness, baseline comparison, and soak gates"
	if err := c.changes.Append(record); err != nil {
		return c.failStage(context.Background(), plan, stage, "cannot persist stage commit: "+err.Error())
	}
	stage.Status = StageStatusPassed
	stage.CompletedAt = c.now().UTC()
	stage.Message = "feature stage passed; candidate is the next stable baseline"
	plan.StableService = stage.CandidateName
	plan.CurrentStage = stage.Index + 1
	return c.savePlan(plan)
}

func (c *Controller) failStage(ctx context.Context, plan *Plan, stage *Stage, reason string) error {
	key := types.NamespacedName{Namespace: plan.Namespace, Name: stage.CandidateName}
	rollbackErr := c.deleteOwnedCandidate(ctx, key, plan.ID, stage.ChangeID)
	record, recordErr := c.changes.Latest(stage.ChangeID)
	if recordErr == nil {
		record.OccurredAt = c.now().UTC()
		if rollbackErr == nil {
			record.Status = changesafety.StatusRolledBack
			record.Message = reason
		} else {
			record.Status = changesafety.StatusRollbackFailed
			record.Message = reason + "; rollback failed: " + rollbackErr.Error()
		}
		_ = c.changes.Append(record)
	}
	stage.Status = StageStatusRolledBack
	stage.CompletedAt = c.now().UTC()
	stage.Message = reason
	plan.Status = PlanStatusFailed
	plan.Message = "experiment stopped; previous stable service retained"
	if rollbackErr != nil {
		plan.Message = "experiment stopped and automatic candidate rollback failed: " + rollbackErr.Error()
	}
	if err := c.savePlan(plan); err != nil {
		return err
	}
	if rollbackErr != nil {
		return rollbackErr
	}
	return nil
}

func (c *Controller) getReadyBaseline(
	ctx context.Context,
	namespace string,
	name string,
) (*infernexv1alpha1.InferNexService, error) {
	service := &infernexv1alpha1.InferNexService{}
	key := types.NamespacedName{Namespace: namespace, Name: name}
	if err := c.client.Get(ctx, key, service); err != nil {
		return nil, fmt.Errorf("get stable baseline %s: %w", key, err)
	}
	if !service.Status.Ready || service.Status.ObservedGeneration < service.Generation || terminalDegraded(service) {
		return nil, fmt.Errorf("baseline %s is not stably Ready at its current generation", key)
	}
	return service, nil
}

func (c *Controller) getApprovedFeature(
	ctx context.Context,
	name string,
) (*infernexv1alpha1.InferNexServiceConfig, error) {
	config := &infernexv1alpha1.InferNexServiceConfig{}
	key := types.NamespacedName{Namespace: c.config.TemplateNamespace, Name: name}
	if err := c.client.Get(ctx, key, config); err != nil {
		return nil, fmt.Errorf("get experiment feature profile %s: %w", key, err)
	}
	if !strings.EqualFold(strings.TrimSpace(config.Labels[ApprovedFeatureLabel]), "true") {
		return nil, fmt.Errorf("InferNexServiceConfig %s is not approved by label %s=true", key, ApprovedFeatureLabel)
	}
	if config.Spec.SourceRef != nil || config.Spec.Model != nil || len(config.Spec.BaseRefs) > 0 {
		return nil, fmt.Errorf("experiment feature profile %s must be a sparse feature layer without sourceRef, model, or baseRefs", key)
	}
	return config, nil
}

func (c *Controller) requireAbsent(ctx context.Context, namespace string, name string) error {
	key := types.NamespacedName{Namespace: namespace, Name: name}
	err := c.client.Get(ctx, key, &infernexv1alpha1.InferNexService{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check candidate %s: %w", key, err)
	}
	return fmt.Errorf("candidate InferNexService %s already exists", key)
}

func (c *Controller) deleteOwnedCandidate(
	ctx context.Context,
	key types.NamespacedName,
	experimentID string,
	changeID string,
) error {
	service := &infernexv1alpha1.InferNexService{}
	if err := c.client.Get(ctx, key, service); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get experiment rollback target %s: %w", key, err)
	}
	if err := verifyOwnedCandidate(service, experimentID, changeID); err != nil {
		return err
	}
	if err := c.client.Delete(ctx, service); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete experiment rollback target %s: %w", key, err)
	}
	return nil
}

func (c *Controller) savePlan(plan *Plan) error {
	plan.UpdatedAt = c.now().UTC()
	if err := c.plans.Append(*plan); err != nil {
		return fmt.Errorf("persist experiment plan %s: %w", plan.ID, err)
	}
	return nil
}

func validateRequest(request Request) (Request, error) {
	request.Namespace = strings.TrimSpace(request.Namespace)
	request.BaselineName = strings.TrimSpace(request.BaselineName)
	request.CandidatePrefix = strings.TrimSpace(request.CandidatePrefix)
	if problems := validation.IsDNS1123Label(request.Namespace); len(problems) > 0 {
		return Request{}, fmt.Errorf("invalid namespace %q: %s", request.Namespace, strings.Join(problems, "; "))
	}
	if problems := validation.IsDNS1123Subdomain(request.BaselineName); len(problems) > 0 {
		return Request{}, fmt.Errorf("invalid baseline name %q: %s", request.BaselineName, strings.Join(problems, "; "))
	}
	if problems := validation.IsDNS1123Subdomain(request.CandidatePrefix); len(problems) > 0 {
		return Request{}, fmt.Errorf("invalid candidatePrefix %q: %s", request.CandidatePrefix, strings.Join(problems, "; "))
	}
	if len(request.FeatureProfiles) == 0 || len(request.FeatureProfiles) > maxFeatureProfiles {
		return Request{}, fmt.Errorf("featureProfiles must contain between 1 and %d approved profiles", maxFeatureProfiles)
	}
	seen := make(map[string]struct{}, len(request.FeatureProfiles))
	for index, profile := range request.FeatureProfiles {
		profile = strings.TrimSpace(profile)
		if problems := validation.IsDNS1123Subdomain(profile); len(problems) > 0 {
			return Request{}, fmt.Errorf("invalid feature profile %q: %s", profile, strings.Join(problems, "; "))
		}
		if _, found := seen[profile]; found {
			return Request{}, fmt.Errorf("feature profile %q is duplicated", profile)
		}
		seen[profile] = struct{}{}
		request.FeatureProfiles[index] = profile
	}
	if !request.Confirm {
		return Request{}, fmt.Errorf("experiment creation requires confirm=true after reviewing the baseline, candidate prefix, and ordered feature profiles")
	}
	return request, nil
}

func validateBaselineSpec(service *infernexv1alpha1.InferNexService) error {
	if len(service.Spec.BaseRefs) == 0 {
		return fmt.Errorf("baseline %s/%s is not experiment-compatible: at least one baseRef is required", service.Namespace, service.Name)
	}
	if service.Spec.Engine != nil || service.Spec.Components != nil || service.Spec.IntelligentGatewayRouting != nil {
		return fmt.Errorf("baseline %s/%s is not experiment-compatible: engine, components, and routing must come from baseRefs instead of inline overrides", service.Namespace, service.Name)
	}
	return nil
}

func candidateName(prefix string, index int) string {
	return fmt.Sprintf("%s-s%02d", prefix, index+1)
}

func candidateService(
	plan *Plan,
	stage Stage,
	baseline *infernexv1alpha1.InferNexService,
) *infernexv1alpha1.InferNexService {
	spec := baseline.Spec.DeepCopy()
	spec.BaseRefs = append(
		[]infernexv1alpha1.NamedRef{{Name: stage.FeatureProfile}},
		spec.BaseRefs...,
	)
	return &infernexv1alpha1.InferNexService{
		TypeMeta: metav1.TypeMeta{
			APIVersion: infernexv1alpha1.GroupVersion.String(),
			Kind:       "InferNexService",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: plan.Namespace,
			Name:      stage.CandidateName,
			Labels: map[string]string{
				managedByLabel:    managedByAgent,
				experimentIDLabel: plan.ID,
			},
			Annotations: map[string]string{
				changeIDAnnotation: stage.ChangeID,
				baselineAnnotation: stage.BaselineName,
				featureAnnotation:  stage.FeatureProfile,
				stageAnnotation:    fmt.Sprintf("%d", stage.Index+1),
			},
		},
		Spec: *spec,
	}
}

func verifyOwnedCandidate(
	service *infernexv1alpha1.InferNexService,
	experimentID string,
	changeID string,
) error {
	if service.Labels[managedByLabel] != managedByAgent ||
		service.Labels[experimentIDLabel] != experimentID ||
		service.Annotations[changeIDAnnotation] != changeID {
		return fmt.Errorf("experiment ownership changed; refusing to mutate InferNexService %s/%s", service.Namespace, service.Name)
	}
	return nil
}

func terminalDegraded(service *infernexv1alpha1.InferNexService) bool {
	if service.Status.ObservedGeneration < service.Generation {
		return false
	}
	for _, condition := range service.Status.Conditions {
		if condition.Type == "Degraded" && condition.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
