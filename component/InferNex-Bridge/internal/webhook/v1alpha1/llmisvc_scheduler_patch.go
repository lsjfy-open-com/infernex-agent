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

package v1alpha1

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	llmdTokenizerContainerName       = "tokenizer"
	llmdSchedulerMainContainerName   = "main"
	hermesTokenizerWritableTmpVolume = "tokenizer-tmp"
)

// llmdTokenizerVolumesToDrop removes llm-d-only pod volumes and mounts (not tokenizer-tmp).
var llmdTokenizerVolumesToDrop = map[string]struct{}{
	"tokenizer-cache": {},
	"tokenizer-uds":   {},
}

// llmdTokenizerContainerFields are stripped from the preset tokenizer container so
// LLMInferenceService router.scheduler.template can override image, probes, and env.
var llmdTokenizerContainerFields = []string{
	"ports",
	"startupProbe",
	"readinessProbe",
	"livenessProbe",
	"workingDir",
	"env",
}

var schedulerTemplatePath = []string{"spec", "router", "scheduler", "template"}

// stripLLMDTokenizerFromSchedulerTemplate sanitizes the llm-d scheduler preset
// (kserve-config-llm-scheduler spec.router.scheduler.template): keeps the tokenizer
// container shell and writable /tmp (tokenizer-tmp); removes llm-d-only volumes/mounts;
// removes main.command so Hermes EPP uses image ENTRYPOINT + LLMISVC args.
func stripLLMDTokenizerFromSchedulerTemplate(obj map[string]interface{}) {
	containersPath := append(schedulerTemplatePath, "containers")
	containers, found, err := unstructured.NestedSlice(obj, containersPath...)
	if err != nil || !found {
		return
	}

	out := make([]interface{}, 0, len(containers))
	for _, c := range containers {
		cm, ok := c.(map[string]interface{})
		if !ok {
			out = append(out, c)
			continue
		}
		name, _ := cm["name"].(string)
		trimmed := strings.TrimSpace(name)
		if strings.EqualFold(trimmed, llmdTokenizerContainerName) {
			sanitizeLLMDTokenizerContainer(cm)
		} else {
			if strings.EqualFold(trimmed, llmdSchedulerMainContainerName) {
				delete(cm, "command")
			}
			stripLLMDOnlyTokenizerVolumeMounts(cm)
		}
		out = append(out, cm)
	}
	_ = unstructured.SetNestedSlice(obj, out, containersPath...)

	volumesPath := append(schedulerTemplatePath, "volumes")
	volumes, found, err := unstructured.NestedSlice(obj, volumesPath...)
	if err != nil || !found {
		return
	}
	filteredVolumes := filterPodVolumesDropLLMDOnly(volumes)
	if len(filteredVolumes) == 0 {
		unstructured.RemoveNestedField(obj, volumesPath...)
		return
	}
	_ = unstructured.SetNestedSlice(obj, filteredVolumes, volumesPath...)

	removeSpuriousWorkloadTemplate(obj)
}

// removeSpuriousWorkloadTemplate deletes spec.template when a prior patch only injected an empty
// initContainers list (scheduler Config has no workload template in KServe upstream).
func removeSpuriousWorkloadTemplate(obj map[string]interface{}) {
	tpl, found, err := unstructured.NestedMap(obj, "spec", "template")
	if err != nil || !found {
		return
	}
	if len(tpl) == 0 {
		unstructured.RemoveNestedField(obj, "spec", "template")
		return
	}
	if len(tpl) != 1 {
		return
	}
	inits, ok := tpl["initContainers"].([]interface{})
	if !ok || len(inits) != 0 {
		return
	}
	unstructured.RemoveNestedField(obj, "spec", "template")
}

func sanitizeLLMDTokenizerContainer(container map[string]interface{}) {
	if container == nil {
		return
	}
	for _, field := range llmdTokenizerContainerFields {
		delete(container, field)
	}
	stripLLMDOnlyTokenizerVolumeMounts(container)
}

func stripLLMDOnlyTokenizerVolumeMounts(container map[string]interface{}) {
	if container == nil {
		return
	}
	mounts, ok := container["volumeMounts"].([]interface{})
	if !ok || len(mounts) == 0 {
		return
	}
	filtered := make([]interface{}, 0, len(mounts))
	for _, m := range mounts {
		mm, ok := m.(map[string]interface{})
		if !ok {
			filtered = append(filtered, m)
			continue
		}
		volName, _ := mm["name"].(string)
		if _, drop := llmdTokenizerVolumesToDrop[strings.TrimSpace(volName)]; drop {
			continue
		}
		filtered = append(filtered, mm)
	}
	if len(filtered) == 0 {
		delete(container, "volumeMounts")
		return
	}
	container["volumeMounts"] = filtered
}

func filterPodVolumesDropLLMDOnly(volumes []interface{}) []interface{} {
	filtered := make([]interface{}, 0, len(volumes))
	for _, v := range volumes {
		vm, ok := v.(map[string]interface{})
		if !ok {
			filtered = append(filtered, v)
			continue
		}
		name, _ := vm["name"].(string)
		if _, drop := llmdTokenizerVolumesToDrop[strings.TrimSpace(name)]; drop {
			continue
		}
		filtered = append(filtered, vm)
	}
	return filtered
}
