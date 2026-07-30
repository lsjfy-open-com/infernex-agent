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

package remediator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infernexv1alpha1 "gitcode.com/openFuyao/InferNex/api/v1alpha1"
)

const (
	ApprovedProfileLabel      = "agent.infernex.io/approved-recovery-profile"
	managedLabel              = "agent.infernex.io/managed"
	recoveryProfileAnnotation = "agent.infernex.io/recovery-profile"
	recoverySourceAnnotation  = "agent.infernex.io/recovery-source"
	maxNameLength             = 253
)

type Request struct {
	Namespace  string
	SourceName string
	Profile    string
	Name       string
}

type Result struct {
	Namespace string
	Name      string
	Profile   string
	Action    string
}

type ProfileRemediator struct {
	client            client.Client
	templateNamespace string
}

func New(kubeClient client.Client, templateNamespace string) (*ProfileRemediator, error) {
	templateNamespace, err := validNamespace(templateNamespace)
	if err != nil {
		return nil, fmt.Errorf("invalid recovery template namespace: %w", err)
	}
	return &ProfileRemediator{
		client:            kubeClient,
		templateNamespace: templateNamespace,
	}, nil
}

func (r *ProfileRemediator) EnsureRecovery(ctx context.Context, request Request) (Result, error) {
	namespace, err := validNamespace(request.Namespace)
	if err != nil {
		return Result{}, err
	}
	sourceName, err := validName("source InferNexService", request.SourceName)
	if err != nil {
		return Result{}, err
	}
	profile, err := validName("recovery profile", request.Profile)
	if err != nil {
		return Result{}, err
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		name = defaultRecoveryName(sourceName)
	}
	name, err = validName("recovery InferNexService", name)
	if err != nil {
		return Result{}, err
	}

	config := &infernexv1alpha1.InferNexServiceConfig{}
	configKey := types.NamespacedName{Namespace: r.templateNamespace, Name: profile}
	if err := r.client.Get(ctx, configKey, config); err != nil {
		return Result{}, fmt.Errorf("get recovery profile %s: %w", configKey, err)
	}
	if !strings.EqualFold(
		strings.TrimSpace(config.Labels[ApprovedProfileLabel]),
		"true",
	) {
		return Result{}, fmt.Errorf(
			"InferNexServiceConfig %s is not approved by label %s=true",
			configKey,
			ApprovedProfileLabel,
		)
	}

	desired := recoveryService(namespace, name, sourceName, profile)
	key := types.NamespacedName{Namespace: namespace, Name: name}
	current := &infernexv1alpha1.InferNexService{}
	if err := r.client.Get(ctx, key, current); err == nil {
		if err := verifyRecovery(current, sourceName, profile, desired.Spec); err != nil {
			return Result{}, err
		}
		return resultFor(desired, profile, "unchanged"), nil
	} else if !apierrors.IsNotFound(err) {
		return Result{}, fmt.Errorf("check recovery InferNexService %s: %w", key, err)
	}
	if err := r.client.Create(ctx, desired); err != nil {
		return Result{}, fmt.Errorf("create recovery InferNexService %s: %w", key, err)
	}
	return resultFor(desired, profile, "created"), nil
}

func recoveryService(
	namespace string,
	name string,
	sourceName string,
	profile string,
) *infernexv1alpha1.InferNexService {
	return &infernexv1alpha1.InferNexService{
		TypeMeta: metav1.TypeMeta{
			APIVersion: infernexv1alpha1.GroupVersion.String(),
			Kind:       "InferNexService",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels: map[string]string{
				managedLabel: "true",
			},
			Annotations: map[string]string{
				recoveryProfileAnnotation: profile,
				recoverySourceAnnotation:  sourceName,
			},
		},
		Spec: infernexv1alpha1.InferNexServiceSpec{
			BaseRefs: []infernexv1alpha1.NamedRef{{Name: profile}},
		},
	}
}

func verifyRecovery(
	current *infernexv1alpha1.InferNexService,
	sourceName string,
	profile string,
	expected infernexv1alpha1.InferNexServiceSpec,
) error {
	if current.Labels[managedLabel] != "true" ||
		current.Annotations[recoverySourceAnnotation] != sourceName ||
		current.Annotations[recoveryProfileAnnotation] != profile {
		return fmt.Errorf(
			"InferNexService %s/%s is not the expected Agent-managed recovery service; refusing to mutate it",
			current.Namespace,
			current.Name,
		)
	}
	if !reflect.DeepEqual(current.Spec, expected) {
		return fmt.Errorf(
			"Agent-managed recovery InferNexService %s/%s has spec drift; refusing to overwrite it",
			current.Namespace,
			current.Name,
		)
	}
	return nil
}

func resultFor(
	service *infernexv1alpha1.InferNexService,
	profile string,
	action string,
) Result {
	return Result{
		Namespace: service.Namespace,
		Name:      service.Name,
		Profile:   profile,
		Action:    action,
	}
}

func defaultRecoveryName(sourceName string) string {
	suffix := "-recovery"
	if len(sourceName)+len(suffix) <= maxNameLength {
		return sourceName + suffix
	}
	sum := sha256.Sum256([]byte(sourceName))
	hash := hex.EncodeToString(sum[:4])
	prefixLimit := maxNameLength - len(suffix) - len(hash) - 1
	prefix := strings.TrimRight(sourceName[:prefixLimit], "-.")
	return prefix + "-" + hash + suffix
}

func validNamespace(value string) (string, error) {
	namespace := strings.TrimSpace(value)
	if problems := validation.IsDNS1123Label(namespace); len(problems) > 0 {
		return "", fmt.Errorf("invalid namespace %q: %s", namespace, strings.Join(problems, "; "))
	}
	return namespace, nil
}

func validName(label string, value string) (string, error) {
	name := strings.TrimSpace(value)
	if problems := validation.IsDNS1123Subdomain(name); len(problems) > 0 {
		return "", fmt.Errorf("invalid %s name %q: %s", label, name, strings.Join(problems, "; "))
	}
	return name, nil
}
