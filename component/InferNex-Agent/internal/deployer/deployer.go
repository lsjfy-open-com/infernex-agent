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

package deployer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
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
)

const (
	managedByLabel     = "app.kubernetes.io/managed-by"
	catalogLabel       = "infernex.io/catalog-id"
	changeIDAnnotation = "agent.infernex.io/change-id"
	sourceIDAnnotation = "agent.infernex.io/deployment-source"
	managedByAgent     = "infernex-agent"
)

type KubernetesDeployer struct {
	client            client.Client
	store             changesafety.Store
	readinessTimeout  time.Duration
	pollInterval      time.Duration
	targetNamespace   string
	templateNamespace string
	sourceNamespaces  []string

	mu         sync.Mutex
	runContext context.Context
	monitoring map[string]struct{}
}

func New(kubeClient client.Client, options ...Option) *KubernetesDeployer {
	deployer := &KubernetesDeployer{
		client:            kubeClient,
		store:             changesafety.NewMemoryStore(),
		monitoring:        make(map[string]struct{}),
		pollInterval:      2 * time.Second,
		targetNamespace:   "",
		templateNamespace: "infernex-bridge-system",
	}
	for _, option := range options {
		option(deployer)
	}
	return deployer
}

// Start resumes readiness monitoring for deployments that were applied before
// an Agent restart. The append-only store is the source of truth.
func (d *KubernetesDeployer) Start(ctx context.Context) error {
	d.mu.Lock()
	d.runContext = ctx
	d.mu.Unlock()

	pending, err := d.store.Pending()
	if err != nil {
		return fmt.Errorf("load pending deployment changes: %w", err)
	}
	for _, record := range pending {
		if record.Action != "deploy" {
			continue
		}
		if record.Status == changesafety.StatusPlanned {
			if err := d.recoverPlanned(ctx, record); err != nil {
				return err
			}
		} else {
			d.startMonitor(record)
		}
	}
	return nil
}

func (d *KubernetesDeployer) recoverPlanned(
	ctx context.Context,
	record changesafety.ChangeRecord,
) error {
	key := types.NamespacedName{
		Namespace: record.Target.Namespace,
		Name:      record.Target.Name,
	}
	service := &infernexv1alpha1.InferNexService{}
	if err := d.client.Get(ctx, key, service); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("recover planned deployment %s: %w", record.ID, err)
		}
		record.Status = changesafety.StatusApplyFailed
		record.OccurredAt = time.Now().UTC()
		record.Message = "Agent restarted before the planned resource was created"
		if err := d.store.Append(record); err != nil {
			return fmt.Errorf("close unapplied deployment %s: %w", record.ID, err)
		}
		return nil
	}
	if service.Labels[managedByLabel] != managedByAgent ||
		service.Annotations[changeIDAnnotation] != record.ID {
		record.Status = changesafety.StatusRollbackFailed
		record.OccurredAt = time.Now().UTC()
		record.Message = "resource exists but change ownership cannot be proven"
		if err := d.store.Append(record); err != nil {
			return fmt.Errorf("record unsafe planned deployment %s: %w", record.ID, err)
		}
		return fmt.Errorf(
			"planned deployment %s target %s exists without matching ownership",
			record.ID,
			key,
		)
	}
	record.Status = changesafety.StatusApplied
	record.OccurredAt = time.Now().UTC()
	record.Message = "Agent resumed after creation and before the applied event was persisted"
	if err := d.store.Append(record); err != nil {
		return fmt.Errorf("resume planned deployment %s: %w", record.ID, err)
	}
	d.startMonitor(record)
	return nil
}

func (d *KubernetesDeployer) ListSources(ctx context.Context) (SourceList, error) {
	result := SourceList{
		TargetNamespace: d.targetNamespace,
		Sources:         make([]Source, 0),
		Message:         "Only existing Ready services and administrator-created InferNexServiceConfig profiles can be reused; the Agent does not generate runtime YAML or image settings.",
	}
	for _, namespace := range normalizedNamespaces(d.sourceNamespaces) {
		services := &infernexv1alpha1.InferNexServiceList{}
		if err := d.client.List(ctx, services, client.InNamespace(namespace)); err != nil {
			return SourceList{}, fmt.Errorf("list deployment sources in %s: %w", namespace, err)
		}
		for index := range services.Items {
			service := &services.Items[index]
			if !stableSource(service) {
				continue
			}
			source := Source{
				SourceID:        serviceSourceID(service.Namespace, service.Name),
				Kind:            "stable-service",
				Namespace:       service.Namespace,
				Name:            service.Name,
				Mode:            service.Status.Mode,
				TargetNamespace: d.targetNamespace,
				BaseRefs:        namedRefNames(service.Spec.BaseRefs),
			}
			if service.Spec.Model != nil {
				source.ModelName = service.Spec.Model.Name
				source.ModelURI = sanitizedModelURI(service.Spec.Model.URI)
			}
			result.Sources = append(result.Sources, source)
		}
	}

	if strings.TrimSpace(d.templateNamespace) != "" {
		profiles := &infernexv1alpha1.InferNexServiceConfigList{}
		if err := d.client.List(ctx, profiles, client.InNamespace(d.templateNamespace)); err != nil {
			return SourceList{}, fmt.Errorf("list deployment profiles in %s: %w", d.templateNamespace, err)
		}
		for index := range profiles.Items {
			profile := &profiles.Items[index]
			if profile.Spec.Engine == nil ||
				(profile.Spec.Engine.Template == nil &&
					(profile.Spec.Engine.Prefill == nil || profile.Spec.Engine.Prefill.Template == nil)) {
				continue
			}
			source := Source{
				SourceID:        profileSourceID(profile.Name),
				Kind:            "profile",
				Namespace:       profile.Namespace,
				Name:            profile.Name,
				TargetNamespace: d.targetNamespace,
				BaseRefs:        []string{profile.Name},
			}
			if profile.Spec.Model != nil {
				source.ModelName = profile.Spec.Model.Name
				source.ModelURI = sanitizedModelURI(profile.Spec.Model.URI)
			}
			if profile.Spec.Engine.Prefill != nil {
				source.Mode = "pd"
			} else {
				source.Mode = "aggregate"
			}
			result.Sources = append(result.Sources, source)
		}
	}
	sort.Slice(result.Sources, func(i, j int) bool {
		return result.Sources[i].SourceID < result.Sources[j].SourceID
	})
	return result, nil
}

func (d *KubernetesDeployer) Deploy(ctx context.Context, request Request) (Result, error) {
	if d.targetNamespace != "" {
		request.Namespace = d.targetNamespace
	}
	if request.SourceID == "" && request.CatalogID == TinyModelCatalogID {
		request.SourceID = "builtin:" + TinyModelCatalogID
	}
	request, err := validateRequest(request, "deploy", true)
	if err != nil {
		return Result{}, err
	}

	desired, err := d.desiredFromSource(ctx, request)
	if err != nil {
		return Result{}, err
	}
	current := &infernexv1alpha1.InferNexService{}
	key := types.NamespacedName{Namespace: request.Namespace, Name: request.Name}
	if err := d.client.Get(ctx, key, current); err != nil {
		if !apierrors.IsNotFound(err) {
			return Result{}, fmt.Errorf("check InferNexService %s: %w", key, err)
		}
		changeID, err := changesafety.NewID()
		if err != nil {
			return Result{}, err
		}
		if desired.Annotations == nil {
			desired.Annotations = make(map[string]string)
		}
		desired.Annotations[changeIDAnnotation] = changeID
		desiredRaw, err := json.Marshal(desired)
		if err != nil {
			return Result{}, fmt.Errorf("encode desired InferNexService %s: %w", key, err)
		}
		record := newChangeRecord(
			changeID,
			"deploy",
			changesafety.StatusPlanned,
			request,
			nil,
			desiredRaw,
			"target did not exist before this deployment",
		)
		if err := d.store.Append(record); err != nil {
			return Result{}, fmt.Errorf("persist pre-deployment change record: %w", err)
		}
		if err := d.client.Create(ctx, desired); err != nil {
			record.Status = changesafety.StatusApplyFailed
			record.OccurredAt = time.Now().UTC()
			record.Message = err.Error()
			if storeErr := d.store.Append(record); storeErr != nil {
				return Result{}, fmt.Errorf(
					"create catalog InferNexService %s: %v; persist failure: %w",
					key,
					err,
					storeErr,
				)
			}
			return Result{}, fmt.Errorf("create catalog InferNexService %s: %w", key, err)
		}
		record.Status = changesafety.StatusApplied
		record.OccurredAt = time.Now().UTC()
		record.Message = "resource created; readiness monitoring started"
		if err := d.store.Append(record); err != nil {
			rollbackErr := d.deleteOwnedChange(ctx, key, changeID)
			if rollbackErr != nil {
				return Result{}, fmt.Errorf(
					"persist applied deployment: %v; emergency rollback: %w",
					err,
					rollbackErr,
				)
			}
			return Result{}, fmt.Errorf(
				"persist applied deployment: %w; created resource was rolled back",
				err,
			)
		}
		if d.readinessTimeout > 0 {
			d.startMonitor(record)
		} else {
			record.Status = changesafety.StatusCommitted
			record.OccurredAt = time.Now().UTC()
			record.Message = "readiness monitoring is disabled"
			if err := d.store.Append(record); err != nil {
				return Result{}, fmt.Errorf("persist deployment commit: %w", err)
			}
		}
		result := resultFor(request, "created")
		result.ChangeID = changeID
		if d.readinessTimeout > 0 {
			result.ChangeStatus = changesafety.StatusApplied
		} else {
			result.ChangeStatus = changesafety.StatusCommitted
		}
		return result, nil
	}

	if err := verifyOwned(current); err != nil {
		return Result{}, err
	}
	if current.Annotations[sourceIDAnnotation] != request.SourceID {
		return Result{}, fmt.Errorf(
			"InferNexService %s is Agent-owned from source %q, not %q; refusing to overwrite it",
			key,
			current.Annotations[sourceIDAnnotation],
			request.SourceID,
		)
	}
	if !equality.Semantic.DeepEqual(current.Spec, desired.Spec) {
		return Result{}, fmt.Errorf(
			"InferNexService %s is Agent-owned but its spec drifted from source %q; refusing to overwrite it",
			key,
			request.SourceID,
		)
	}
	result := resultFor(request, "already-exists")
	result.ChangeID = current.Annotations[changeIDAnnotation]
	if result.ChangeID == "" {
		result.ChangeStatus = changesafety.StatusCommitted
	} else if record, err := d.store.Latest(result.ChangeID); err == nil {
		result.ChangeStatus = record.Status
	} else {
		result.ChangeStatus = "unknown"
	}
	return result, nil
}

func (d *KubernetesDeployer) Delete(ctx context.Context, request Request) (Result, error) {
	if d.targetNamespace != "" {
		request.Namespace = d.targetNamespace
	}
	if request.SourceID == "" && request.CatalogID == TinyModelCatalogID {
		request.SourceID = "builtin:" + TinyModelCatalogID
	}
	request, err := validateRequest(request, "delete", false)
	if err != nil {
		return Result{}, err
	}

	current := &infernexv1alpha1.InferNexService{}
	key := types.NamespacedName{Namespace: request.Namespace, Name: request.Name}
	if err := d.client.Get(ctx, key, current); err != nil {
		if apierrors.IsNotFound(err) {
			return resultFor(request, "already-absent"), nil
		}
		return Result{}, fmt.Errorf("check InferNexService %s: %w", key, err)
	}
	if err := verifyOwned(current); err != nil {
		return Result{}, err
	}
	changeID, err := changesafety.NewID()
	if err != nil {
		return Result{}, err
	}
	before, err := json.Marshal(current)
	if err != nil {
		return Result{}, fmt.Errorf("encode pre-delete InferNexService %s: %w", key, err)
	}
	record := newChangeRecord(
		changeID,
		"delete",
		changesafety.StatusPlanned,
		request,
		before,
		nil,
		"exact pre-delete object recorded",
	)
	if err := d.store.Append(record); err != nil {
		return Result{}, fmt.Errorf("persist pre-delete change record: %w", err)
	}
	if err := d.client.Delete(ctx, current); err != nil && !apierrors.IsNotFound(err) {
		record.Status = changesafety.StatusApplyFailed
		record.OccurredAt = time.Now().UTC()
		record.Message = err.Error()
		_ = d.store.Append(record)
		return Result{}, fmt.Errorf("delete catalog InferNexService %s: %w", key, err)
	}
	record.Status = changesafety.StatusCommitted
	record.OccurredAt = time.Now().UTC()
	record.Message = "explicitly confirmed deletion completed"
	if err := d.store.Append(record); err != nil {
		return Result{}, fmt.Errorf("persist deletion commit: %w", err)
	}
	result := resultFor(request, "deleted")
	result.ChangeID = changeID
	result.ChangeStatus = changesafety.StatusCommitted
	return result, nil
}

func (d *KubernetesDeployer) GetChange(
	_ context.Context,
	changeID string,
) (changesafety.ChangeStatus, error) {
	record, err := d.store.Latest(strings.TrimSpace(changeID))
	if err != nil {
		return changesafety.ChangeStatus{}, fmt.Errorf("get deployment change: %w", err)
	}
	return changesafety.PublicStatus(record), nil
}

func (d *KubernetesDeployer) startMonitor(record changesafety.ChangeRecord) {
	d.mu.Lock()
	if _, running := d.monitoring[record.ID]; running {
		d.mu.Unlock()
		return
	}
	d.monitoring[record.ID] = struct{}{}
	ctx := d.runContext
	if ctx == nil {
		ctx = context.Background()
	}
	d.mu.Unlock()

	go func() {
		defer func() {
			d.mu.Lock()
			delete(d.monitoring, record.ID)
			d.mu.Unlock()
		}()
		d.monitorDeployment(ctx, record)
	}()
}

func (d *KubernetesDeployer) monitorDeployment(
	ctx context.Context,
	record changesafety.ChangeRecord,
) {
	key := types.NamespacedName{
		Namespace: record.Target.Namespace,
		Name:      record.Target.Name,
	}
	deadline := record.OccurredAt.Add(d.readinessTimeout)
	pollInterval := d.pollInterval
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		d.rollbackDeployment(context.Background(), record, "readiness deadline expired")
		return
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		service := &infernexv1alpha1.InferNexService{}
		err := d.client.Get(ctx, key, service)
		if err == nil {
			if service.Status.Ready &&
				service.Status.ObservedGeneration >= service.Generation {
				d.finishRecord(record, changesafety.StatusCommitted, "deployment reached Ready")
				return
			}
			if terminalDegraded(service) {
				d.rollbackDeployment(
					context.Background(),
					record,
					"deployment reported a terminal Degraded condition",
				)
				return
			}
		} else if apierrors.IsNotFound(err) {
			d.finishRecord(
				record,
				changesafety.StatusRolledBack,
				"deployment resource is absent; pre-deployment state is restored",
			)
			return
		} else if ctx.Err() == nil {
			slog.Warn(
				"readiness check failed; monitoring will retry",
				"changeId", record.ID,
				"resource", key,
				"error", err,
			)
		}

		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			d.rollbackDeployment(
				context.Background(),
				record,
				fmt.Sprintf("deployment did not become Ready within %s", d.readinessTimeout),
			)
			return
		case <-ticker.C:
		}
	}
}

func (d *KubernetesDeployer) rollbackDeployment(
	ctx context.Context,
	record changesafety.ChangeRecord,
	reason string,
) {
	key := types.NamespacedName{
		Namespace: record.Target.Namespace,
		Name:      record.Target.Name,
	}
	rollbackCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := d.deleteOwnedChange(rollbackCtx, key, record.ID); err != nil {
		d.finishRecord(
			record,
			changesafety.StatusRollbackFailed,
			fmt.Sprintf("%s; rollback failed: %v", reason, err),
		)
		slog.Error(
			"deployment rollback failed",
			"changeId", record.ID,
			"resource", key,
			"error", err,
		)
		return
	}
	d.finishRecord(record, changesafety.StatusRolledBack, reason)
	slog.Warn(
		"deployment rolled back to its pre-change state",
		"changeId", record.ID,
		"resource", key,
		"reason", reason,
	)
}

func (d *KubernetesDeployer) deleteOwnedChange(
	ctx context.Context,
	key types.NamespacedName,
	changeID string,
) error {
	service := &infernexv1alpha1.InferNexService{}
	if err := d.client.Get(ctx, key, service); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("get rollback target: %w", err)
	}
	if service.Labels[managedByLabel] != managedByAgent ||
		service.Annotations[changeIDAnnotation] != changeID {
		return fmt.Errorf(
			"rollback ownership changed; refusing to delete InferNexService %s",
			key,
		)
	}
	if err := d.client.Delete(ctx, service); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete rollback target: %w", err)
	}
	for {
		err := d.client.Get(ctx, key, &infernexv1alpha1.InferNexService{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("confirm rollback target deletion: %w", err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for rollback target deletion: %w", ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (d *KubernetesDeployer) finishRecord(
	record changesafety.ChangeRecord,
	status string,
	message string,
) {
	record.Status = status
	record.OccurredAt = time.Now().UTC()
	record.Message = message
	if err := d.store.Append(record); err != nil {
		slog.Error(
			"persist deployment change state",
			"changeId", record.ID,
			"status", status,
			"error", err,
		)
	}
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

func newChangeRecord(
	id string,
	action string,
	status string,
	request Request,
	before json.RawMessage,
	desired json.RawMessage,
	message string,
) changesafety.ChangeRecord {
	return changesafety.ChangeRecord{
		APIVersion: "agent.infernex.io/v1alpha1",
		Kind:       "InferNexChange",
		ID:         id,
		Action:     action,
		Status:     status,
		Target: changesafety.Target{
			APIVersion: infernexv1alpha1.GroupVersion.String(),
			Kind:       "InferNexService",
			Namespace:  request.Namespace,
			Name:       request.Name,
		},
		Before:     before,
		Desired:    desired,
		OccurredAt: time.Now().UTC(),
		Message:    message,
	}
}

func validateRequest(request Request, action string, requireSource ...bool) (Request, error) {
	request.Namespace = strings.TrimSpace(request.Namespace)
	request.Name = strings.TrimSpace(request.Name)
	request.SourceID = strings.TrimSpace(request.SourceID)
	request.CatalogID = strings.TrimSpace(request.CatalogID)
	sourceRequired := len(requireSource) > 0 && requireSource[0]

	if problems := validation.IsDNS1123Label(request.Namespace); len(problems) > 0 {
		return Request{}, fmt.Errorf(
			"invalid namespace %q: %s",
			request.Namespace,
			strings.Join(problems, "; "),
		)
	}
	if problems := validation.IsDNS1123Subdomain(request.Name); len(problems) > 0 {
		return Request{}, fmt.Errorf(
			"invalid InferNexService name %q: %s",
			request.Name,
			strings.Join(problems, "; "),
		)
	}
	if request.CatalogID != "" && request.CatalogID != TinyModelCatalogID {
		return Request{}, fmt.Errorf("unsupported legacy catalogId %q", request.CatalogID)
	}
	if sourceRequired && request.SourceID == "" && request.CatalogID == "" {
		return Request{}, fmt.Errorf("deploy requires a sourceId returned by infernex_list_deployment_sources")
	}
	if !request.Confirm {
		return Request{}, fmt.Errorf(
			"%s requires confirm=true after reviewing the discovered source and target name",
			action,
		)
	}
	return request, nil
}

func verifyOwned(service *infernexv1alpha1.InferNexService) error {
	key := types.NamespacedName{Namespace: service.Namespace, Name: service.Name}
	legacyCatalogOwned := service.Labels[catalogLabel] == TinyModelCatalogID
	if service.Labels[managedByLabel] != managedByAgent ||
		(strings.TrimSpace(service.Annotations[sourceIDAnnotation]) == "" && !legacyCatalogOwned) {
		return fmt.Errorf(
			"InferNexService %s is not owned by an InferNex Agent deployment change; refusing to mutate it",
			key,
		)
	}
	return nil
}

func resultFor(request Request, operation string) Result {
	result := Result{
		Namespace:    request.Namespace,
		Name:         request.Name,
		SourceID:     request.SourceID,
		CatalogID:    request.CatalogID,
		Operation:    operation,
		ResourceKind: "InferNexService",
		Message:      "InferNex Bridge owns workload reconciliation; inspect readiness and topology before using the service endpoint.",
	}
	if request.CatalogID == TinyModelCatalogID {
		result.Endpoint = fmt.Sprintf(
			"http://%s-engine-aggregate.%s.svc:8080",
			request.Name,
			request.Namespace,
		)
		result.InferenceAPI = "/v1/chat/completions"
	}
	return result
}

func objectMeta(namespace, name, sourceID string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Namespace: namespace,
		Name:      name,
		Labels: map[string]string{
			managedByLabel: managedByAgent,
		},
		Annotations: map[string]string{
			sourceIDAnnotation: sourceID,
		},
	}
}

func (d *KubernetesDeployer) desiredFromSource(
	ctx context.Context,
	request Request,
) (*infernexv1alpha1.InferNexService, error) {
	desired := &infernexv1alpha1.InferNexService{
		TypeMeta: metav1.TypeMeta{
			APIVersion: infernexv1alpha1.GroupVersion.String(),
			Kind:       "InferNexService",
		},
		ObjectMeta: objectMeta(request.Namespace, request.Name, request.SourceID),
	}
	if request.SourceID == "builtin:"+TinyModelCatalogID {
		legacy := tinyModelService(request.Namespace, request.Name)
		legacy.Annotations[sourceIDAnnotation] = request.SourceID
		return legacy, nil
	}

	if strings.HasPrefix(request.SourceID, "service:") {
		parts := strings.Split(request.SourceID, ":")
		if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
			return nil, fmt.Errorf("invalid stable-service sourceId %q", request.SourceID)
		}
		if !containsNamespace(d.sourceNamespaces, parts[1]) {
			return nil, fmt.Errorf("source namespace %q is outside the discovered Agent scope", parts[1])
		}
		source := &infernexv1alpha1.InferNexService{}
		key := types.NamespacedName{Namespace: parts[1], Name: parts[2]}
		if err := d.client.Get(ctx, key, source); err != nil {
			return nil, fmt.Errorf("get stable deployment source %s: %w", key, err)
		}
		if !stableSource(source) {
			return nil, fmt.Errorf("deployment source %s is no longer Ready at its current generation", key)
		}
		desired.Spec = source.DeepCopy().Spec
		// An omitted SourceRef namespace means "the InferNexService namespace".
		// The Agent creates the new service in its isolated workspace, so make the
		// original namespace explicit to preserve the stable source's semantics.
		if desired.Spec.SourceRef != nil && strings.TrimSpace(desired.Spec.SourceRef.Namespace) == "" {
			desired.Spec.SourceRef.Namespace = source.Namespace
		}
		return desired, nil
	}

	if strings.HasPrefix(request.SourceID, "profile:") {
		name := strings.TrimPrefix(request.SourceID, "profile:")
		if problems := validation.IsDNS1123Subdomain(name); len(problems) > 0 {
			return nil, fmt.Errorf("invalid profile sourceId %q", request.SourceID)
		}
		profile := &infernexv1alpha1.InferNexServiceConfig{}
		key := types.NamespacedName{Namespace: d.templateNamespace, Name: name}
		if err := d.client.Get(ctx, key, profile); err != nil {
			return nil, fmt.Errorf("get deployment profile %s: %w", key, err)
		}
		if profile.Spec.Engine == nil ||
			(profile.Spec.Engine.Template == nil &&
				(profile.Spec.Engine.Prefill == nil || profile.Spec.Engine.Prefill.Template == nil)) {
			return nil, fmt.Errorf("profile %s does not contain a complete inference engine template", key)
		}
		desired.Spec.BaseRefs = []infernexv1alpha1.NamedRef{{Name: profile.Name}}
		return desired, nil
	}

	return nil, fmt.Errorf("unsupported sourceId %q; refresh infernex_list_deployment_sources", request.SourceID)
}

func normalizedNamespaces(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func containsNamespace(values []string, wanted string) bool {
	for _, value := range normalizedNamespaces(values) {
		if value == wanted {
			return true
		}
	}
	return false
}

func sanitizedModelURI(value string) string {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil {
		return value
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func stableSource(service *infernexv1alpha1.InferNexService) bool {
	if service == nil || !service.Status.Ready || service.Status.ObservedGeneration < service.Generation {
		return false
	}
	for _, condition := range service.Status.Conditions {
		if condition.Type == "Degraded" && condition.Status == metav1.ConditionTrue {
			return false
		}
	}
	return true
}

func serviceSourceID(namespace, name string) string {
	return "service:" + namespace + ":" + name
}

func profileSourceID(name string) string {
	return "profile:" + name
}

func namedRefNames(refs []infernexv1alpha1.NamedRef) []string {
	result := make([]string, 0, len(refs))
	for _, ref := range refs {
		if strings.TrimSpace(ref.Name) != "" {
			result = append(result, ref.Name)
		}
	}
	return result
}
