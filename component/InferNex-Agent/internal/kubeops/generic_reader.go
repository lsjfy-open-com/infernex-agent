/*
 * Copyright (c) 2026 Huawei Technologies Co., Ltd.
 * openFuyao is licensed under Mulan PSL v2.
 */

package kubeops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
)

const (
	defaultResourceLimit = 100
	maxResourceLimit     = 300
	maxGenericString     = 16 * 1024
)

var (
	resourceNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)
	sensitiveFieldKey   = regexp.MustCompile(`(?i)(authorization|api.?key|access.?key|secret|token|password|credential|private.?key)`)
)

func (r *KubernetesReader) DiscoverResources(
	_ context.Context, request ResourceDiscoveryRequest,
) (ResourceDiscovery, error) {
	requested := strings.TrimSpace(request.GroupVersion)
	var lists []*metav1.APIResourceList
	result := ResourceDiscovery{GroupVersions: []APIGroupResources{}, Warnings: []string{}}
	if requested != "" {
		if _, err := schema.ParseGroupVersion(requested); err != nil {
			return result, fmt.Errorf("invalid groupVersion %q: %w", requested, err)
		}
		resources, err := r.discovery.ServerResourcesForGroupVersion(requested)
		if err != nil {
			return result, fmt.Errorf("discover %s: %w", requested, err)
		}
		lists = []*metav1.APIResourceList{resources}
	} else {
		resources, err := r.discovery.ServerPreferredResources()
		if err != nil {
			result.Warnings = append(result.Warnings, sanitize(err.Error(), 1024))
		}
		lists = resources
	}
	for _, list := range lists {
		if list == nil {
			continue
		}
		group := APIGroupResources{GroupVersion: list.GroupVersion, Resources: []APIResourceSummary{}}
		for _, resource := range list.APIResources {
			if strings.Contains(resource.Name, "/") || !supportsRead(resource.Verbs) {
				continue
			}
			verbs := append([]string(nil), resource.Verbs...)
			sort.Strings(verbs)
			group.Resources = append(group.Resources, APIResourceSummary{
				Resource: resource.Name, Kind: resource.Kind,
				Namespaced: resource.Namespaced, Verbs: verbs,
			})
		}
		sort.Slice(group.Resources, func(i, j int) bool {
			return group.Resources[i].Resource < group.Resources[j].Resource
		})
		if len(group.Resources) > 0 {
			result.GroupVersions = append(result.GroupVersions, group)
		}
	}
	sort.Slice(result.GroupVersions, func(i, j int) bool {
		return result.GroupVersions[i].GroupVersion < result.GroupVersions[j].GroupVersion
	})
	return result, nil
}

func supportsRead(verbs metav1.Verbs) bool {
	for _, verb := range verbs {
		if verb == "get" || verb == "list" {
			return true
		}
	}
	return false
}

func (r *KubernetesReader) ReadResources(
	ctx context.Context, request ResourceReadRequest,
) (ResourceReadResult, error) {
	request.GroupVersion = strings.TrimSpace(request.GroupVersion)
	request.Resource = strings.TrimSpace(request.Resource)
	request.Namespace = strings.TrimSpace(request.Namespace)
	request.Name = strings.TrimSpace(request.Name)
	request.LabelSelector = strings.TrimSpace(request.LabelSelector)
	request.FieldSelector = strings.TrimSpace(request.FieldSelector)
	request.Continue = strings.TrimSpace(request.Continue)
	result := ResourceReadResult{
		GroupVersion: request.GroupVersion, Resource: request.Resource,
		Namespace: request.Namespace, Objects: []map[string]any{}, Redactions: []string{},
	}
	groupVersion, err := schema.ParseGroupVersion(request.GroupVersion)
	if err != nil {
		return result, fmt.Errorf("invalid groupVersion %q: %w", request.GroupVersion, err)
	}
	if !resourceNamePattern.MatchString(request.Resource) || strings.Contains(request.Resource, "/") {
		return result, fmt.Errorf("invalid resource %q; use the plural resource name returned by k8s_discover_api_resources", request.Resource)
	}
	if err := validateNamespace(request.Namespace, true); err != nil {
		return result, err
	}
	if request.Name != "" {
		if problems := validation.IsDNS1123Subdomain(request.Name); len(problems) > 0 {
			return result, fmt.Errorf("invalid resource name %q: %s", request.Name, strings.Join(problems, "; "))
		}
		if request.Continue != "" {
			return result, fmt.Errorf("continue is only valid for list requests")
		}
	}
	if request.LabelSelector != "" {
		if _, err := metav1.ParseToLabelSelector(request.LabelSelector); err != nil {
			return result, fmt.Errorf("invalid labelSelector: %w", err)
		}
	}
	limit, err := normalizeLimit(request.Limit, defaultResourceLimit, maxResourceLimit)
	if err != nil {
		return result, err
	}
	gvr := groupVersion.WithResource(request.Resource)
	var resource dynamic.ResourceInterface = r.dynamic.Resource(gvr)
	if request.Namespace != "" {
		resource = r.dynamic.Resource(gvr).Namespace(request.Namespace)
	}
	secretPayload := request.Resource == "secrets"
	if request.Name != "" {
		object, getErr := resource.Get(ctx, request.Name, metav1.GetOptions{})
		if getErr != nil {
			return result, getErr
		}
		result.Objects = append(result.Objects, sanitizeGenericObject(object, secretPayload))
		result.Total = 1
	} else {
		objects, listErr := resource.List(ctx, metav1.ListOptions{
			LabelSelector: request.LabelSelector, FieldSelector: request.FieldSelector,
			Limit: int64(limit), Continue: request.Continue,
		})
		if listErr != nil {
			return result, listErr
		}
		for index := range objects.Items {
			result.Objects = append(result.Objects, sanitizeGenericObject(&objects.Items[index], secretPayload))
		}
		result.Total = len(result.Objects)
		result.Continue = objects.GetContinue()
	}
	if secretPayload {
		result.Redactions = append(result.Redactions, "Secret data and stringData are never returned; metadata and type only")
	} else {
		result.Redactions = append(result.Redactions, "credential-like fields and values are redacted; managedFields are omitted")
	}
	return result, nil
}

func sanitizeGenericObject(object *unstructured.Unstructured, secretPayload bool) map[string]any {
	copy := object.DeepCopy().Object
	metadata, _ := copy["metadata"].(map[string]any)
	if metadata != nil {
		delete(metadata, "managedFields")
	}
	if secretPayload {
		for key := range copy {
			if key != "apiVersion" && key != "kind" && key != "metadata" && key != "type" {
				delete(copy, key)
			}
		}
	}
	return scrubGenericMap(copy)
}

func scrubGenericMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		if sensitiveFieldKey.MatchString(key) {
			result[key] = "<redacted>"
			continue
		}
		result[key] = scrubGenericValue(value)
	}
	return result
}

func scrubGenericValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return scrubGenericMap(typed)
	case []any:
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			result = append(result, scrubGenericValue(item))
		}
		return result
	case string:
		return sanitize(typed, maxGenericString)
	default:
		return value
	}
}
