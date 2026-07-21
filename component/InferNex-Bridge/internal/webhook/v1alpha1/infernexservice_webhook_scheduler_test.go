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
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestApplyInferNexPatchesToLLMInferenceServiceConfig_schedulerNoWorkloadTemplate(t *testing.T) {
	raw := `{
  "apiVersion": "serving.kserve.io/v1alpha2",
  "kind": "LLMInferenceServiceConfig",
  "metadata": { "name": "kserve-config-llm-scheduler" },
  "spec": {
    "router": {
      "scheduler": {
        "template": {
          "containers": [{ "name": "main" }]
        }
      }
    }
  }
}`
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatal(err)
	}
	applyInferNexPatchesToLLMInferenceServiceConfig(obj)
	if _, found, _ := unstructured.NestedMap(obj, "spec", "template"); found {
		t.Fatalf("scheduler config should not have spec.template, got %v", obj["spec"])
	}
}

func TestApplyInferNexPatchesToLLMInferenceServiceConfig_schedulerRemovesSpuriousTemplate(t *testing.T) {
	raw := `{
  "apiVersion": "serving.kserve.io/v1alpha2",
  "kind": "LLMInferenceServiceConfig",
  "metadata": { "name": "kserve-config-llm-scheduler" },
  "spec": {
    "template": { "initContainers": [] },
    "router": {
      "scheduler": {
        "template": {
          "containers": [{ "name": "main" }]
        }
      }
    }
  }
}`
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatal(err)
	}
	applyInferNexPatchesToLLMInferenceServiceConfig(obj)
	if _, found, _ := unstructured.NestedMap(obj, "spec", "template"); found {
		t.Fatal("spurious spec.template.initContainers should be removed")
	}
}

func TestApplyInferNexPatchesToLLMInferenceServiceConfig_schedulerStripTokenizer(t *testing.T) {
	raw := `{
  "apiVersion": "serving.kserve.io/v1alpha2",
  "kind": "LLMInferenceServiceConfig",
  "metadata": { "name": "kserve-config-llm-scheduler" },
  "spec": {
    "router": {
      "scheduler": {
        "template": {
          "containers": [
            {
              "name": "main",
              "image": "ghcr.io/llm-d/llm-d-inference-scheduler:v0.7.1",
              "command": ["/app/epp", "--pool-name", "pool", "--grpc-port", "9002"],
              "securityContext": {
                "capabilities": { "drop": ["ALL"] }
              },
              "volumeMounts": [
                { "name": "tls-certs", "mountPath": "/var/run/kserve/tls" },
                { "name": "tokenizer-uds", "mountPath": "/tmp/tokenizer" }
              ]
            },
            {
              "name": "tokenizer",
              "image": "ghcr.io/llm-d/llm-d-uds-tokenizer:v0.7.1",
              "ports": [{ "containerPort": 8082, "name": "health" }],
              "startupProbe": {
                "httpGet": { "path": "/healthz", "port": 8082 }
              },
              "readinessProbe": {
                "httpGet": { "path": "/healthz", "port": 8082 }
              },
              "livenessProbe": {
                "httpGet": { "path": "/healthz", "port": 8082 }
              },
              "volumeMounts": [
                { "name": "tokenizer-tmp", "mountPath": "/tmp" },
                { "name": "tokenizer-cache", "mountPath": "/.cache" },
                { "name": "tokenizer-uds", "mountPath": "/tmp/tokenizer" }
              ]
            }
          ],
          "volumes": [
            { "name": "tls-certs", "secret": { "secretName": "svc-kserve-self-signed-certs" } },
            { "name": "tokenizer-uds", "emptyDir": {} },
            { "name": "tokenizer-tmp", "emptyDir": {} },
            { "name": "tokenizer-cache", "emptyDir": {} }
          ]
        }
      }
    }
  }
}`
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatal(err)
	}
	applyInferNexPatchesToLLMInferenceServiceConfig(obj)

	containers, found, err := unstructured.NestedSlice(obj, "spec", "router", "scheduler", "template", "containers")
	if err != nil || !found {
		t.Fatalf("containers: found=%v err=%v", found, err)
	}
	if len(containers) != 2 {
		t.Fatalf("expected main+tokenizer after patch, got %d", len(containers))
	}
	var main, tokenizer map[string]interface{}
	for _, c := range containers {
		cm, ok := c.(map[string]interface{})
		if !ok {
			t.Fatalf("container type %T", c)
		}
		switch name, _ := cm["name"].(string); name {
		case "main":
			main = cm
		case "tokenizer":
			tokenizer = cm
		default:
			t.Fatalf("unexpected container %q", name)
		}
	}
	if main == nil || tokenizer == nil {
		t.Fatal("missing main or tokenizer container")
	}
	if _, ok := main["command"]; ok {
		t.Fatal("main should not retain llm-d command after patch")
	}
	mounts, _ := main["volumeMounts"].([]interface{})
	for _, m := range mounts {
		mm, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		if volName, _ := mm["name"].(string); volName == "tokenizer-uds" {
			t.Fatalf("main should not mount tokenizer-uds, got %v", mounts)
		}
	}
	sc, ok := main["securityContext"].(map[string]interface{})
	if !ok {
		t.Fatal("main securityContext should be preserved")
	}
	caps, ok := sc["capabilities"].(map[string]interface{})
	if !ok {
		t.Fatal("main capabilities should be preserved")
	}
	drop, ok := caps["drop"].([]interface{})
	if !ok || len(drop) != 1 {
		t.Fatalf("scheduler preset main should keep drop ALL, got %v", drop)
	}
	if s, _ := drop[0].(string); !strings.EqualFold(s, "ALL") {
		t.Fatalf("scheduler preset main drop should be ALL, got %q", s)
	}
	if img, _ := tokenizer["image"].(string); img == "" {
		t.Fatal("tokenizer image should be preserved for strategic merge override")
	}
	for _, field := range llmdTokenizerContainerFields {
		if _, ok := tokenizer[field]; ok {
			t.Fatalf("tokenizer should not have llm-d field %q", field)
		}
	}
	tokenizerMounts, _ := tokenizer["volumeMounts"].([]interface{})
	var keptTmp bool
	for _, m := range tokenizerMounts {
		mm, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		volName, _ := mm["name"].(string)
		mountPath, _ := mm["mountPath"].(string)
		if volName == hermesTokenizerWritableTmpVolume && mountPath == "/tmp" {
			keptTmp = true
		}
		if _, drop := llmdTokenizerVolumesToDrop[volName]; drop {
			t.Fatalf("tokenizer should not mount llm-d volume %q", volName)
		}
	}
	if !keptTmp {
		t.Fatalf("tokenizer should keep %s mount at /tmp, got %v", hermesTokenizerWritableTmpVolume, tokenizerMounts)
	}

	volumes, found, err := unstructured.NestedSlice(obj, "spec", "router", "scheduler", "template", "volumes")
	if err != nil || !found {
		t.Fatalf("volumes: found=%v err=%v", found, err)
	}
	var keptTmpVol bool
	for _, v := range volumes {
		vm, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := vm["name"].(string)
		if name == hermesTokenizerWritableTmpVolume {
			keptTmpVol = true
		}
		if _, drop := llmdTokenizerVolumesToDrop[name]; drop {
			t.Fatalf("volume %q should be removed", name)
		}
	}
	if !keptTmpVol {
		t.Fatal("pod should keep tokenizer-tmp emptyDir volume")
	}
}

func TestStripLLMDTokenizerFromSchedulerTemplate_noScheduler(t *testing.T) {
	obj := map[string]interface{}{
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"containers": []interface{}{
					map[string]interface{}{"name": "main"},
				},
			},
		},
	}
	stripLLMDTokenizerFromSchedulerTemplate(obj)
	containers, found, err := unstructured.NestedSlice(obj, "spec", "template", "containers")
	if err != nil || !found || len(containers) != 1 {
		t.Fatalf("workload template should be untouched: found=%v len=%d err=%v", found, len(containers), err)
	}
}

func TestTryMutateConfigInNamespace_schedulerConfigName(t *testing.T) {
	t.Parallel()
	found := false
	for _, name := range infernexMutateTargetLLMInferenceServiceConfigNames {
		if name == schedulerLLMInferenceServiceConfigName {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("%q should be in infernexMutateTargetLLMInferenceServiceConfigNames", schedulerLLMInferenceServiceConfigName)
	}
}
