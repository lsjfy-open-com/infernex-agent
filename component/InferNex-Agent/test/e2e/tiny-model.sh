#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
agent_dir="${repo_root}/component/InferNex-Agent"
agent_chart="${agent_dir}/chart/infernex-agent"
bridge_chart="${repo_root}/component/InferNex-Bridge/chart/infernex-bridge"
prerequisite_crds="${agent_dir}/test/e2e/prerequisite-crds.yaml"
gateway_crds_dir="${repo_root}/charts/infernex/charts/inference-gateway-crds/crds"

agent_namespace="${AGENT_NAMESPACE:-infernex-system}"
bridge_namespace="${BRIDGE_NAMESPACE:-infernex-bridge-system}"
model_namespace="${MODEL_NAMESPACE:-models}"
release_name="infernex-agent"
bridge_release_name="infernex-bridge"
agent_image_repository="${AGENT_IMAGE_REPOSITORY:-infernex-agent}"
agent_image_tag="${AGENT_IMAGE_TAG:-e2e}"
bridge_image_repository="${BRIDGE_IMAGE_REPOSITORY:-infernex-bridge}"
bridge_image_tag="${BRIDGE_IMAGE_TAG:-e2e}"
agent_local_port="${AGENT_LOCAL_PORT:-18080}"
model_local_port="${MODEL_LOCAL_PORT:-18081}"
model_name="${MODEL_NAME:-kind-smollm}"
catalog_id="smollm2-135m-q4"

# The read-only smoke fixture intentionally has no complete engine. Remove it
# before starting Bridge so only the catalog service enters reconciliation.
kubectl -n "${model_namespace}" delete \
  infernexservice/smoke deployment/smoke-engine \
  --ignore-not-found

kubectl apply --server-side -f "${prerequisite_crds}"
kubectl apply --server-side \
  -f "${gateway_crds_dir}/gateway-api-standard-install.yaml" \
  -f "${gateway_crds_dir}/inference-extension-manifests.yaml"
kubectl wait --for=condition=Established \
  crd/llminferenceservices.serving.kserve.io \
  crd/envoyproxies.gateway.envoyproxy.io \
  crd/httproutes.gateway.networking.k8s.io \
  crd/inferencepools.inference.networking.k8s.io \
  --timeout=60s

helm upgrade --install "${bridge_release_name}" "${bridge_chart}" \
  --namespace "${bridge_namespace}" \
  --create-namespace \
  --set "images.infernex-bridge.repository=${bridge_image_repository}" \
  --set "images.infernex-bridge.tag=${bridge_image_tag}" \
  --set "global.image.pullPolicy=Never" \
  --set "webhooks.enabled=false" \
  --set "templates.installDefaultConfigs=true"

kubectl -n "${bridge_namespace}" rollout status \
  "deployment/${bridge_release_name}" \
  --timeout=180s

helm upgrade --install "${release_name}" "${agent_chart}" \
  --namespace "${agent_namespace}" \
  --set "image.repository=${agent_image_repository}" \
  --set "image.tag=${agent_image_tag}" \
  --set "image.pullPolicy=Never" \
  --set "rbac.targetNamespaces[0]=${model_namespace}" \
  --set "tools.deployment.enabled=true" \
  --set "tools.deployment.testCatalog=true"

kubectl -n "${agent_namespace}" rollout status \
  "deployment/${release_name}" \
  --timeout=120s

service_account="system:serviceaccount:${agent_namespace}:${release_name}"
test "$(kubectl auth can-i create infernexservices.infernex.infernex.io --as="${service_account}" -n "${model_namespace}")" = "yes"
test "$(kubectl auth can-i delete infernexservices.infernex.infernex.io --as="${service_account}" -n "${model_namespace}")" = "yes"
test "$(kubectl auth can-i create deployments.apps --as="${service_account}" -n "${model_namespace}")" = "no"
test "$(kubectl auth can-i get secrets --as="${service_account}" -n "${model_namespace}")" = "no"
test "$(kubectl auth can-i create namespaces --as="${service_account}")" = "no"
test "$(kubectl auth can-i create clusterroles.rbac.authorization.k8s.io --as="${service_account}")" = "no"

agent_forward_log="$(mktemp)"
model_forward_log="$(mktemp)"
agent_forward_pid=""
model_forward_pid=""
bridge_paused="false"
cleanup() {
  if [[ -n "${model_forward_pid}" ]]; then
    kill "${model_forward_pid}" >/dev/null 2>&1 || true
  fi
  if [[ -n "${agent_forward_pid}" ]]; then
    kill "${agent_forward_pid}" >/dev/null 2>&1 || true
  fi
  if [[ "${bridge_paused}" == "true" ]]; then
    kubectl -n "${bridge_namespace}" scale \
      "deployment/${bridge_release_name}" --replicas=1 >/dev/null 2>&1 || true
  fi
  rm -f "${agent_forward_log}" "${model_forward_log}"
}
trap cleanup EXIT

kubectl -n "${agent_namespace}" port-forward \
  "service/${release_name}" "${agent_local_port}:8080" \
  >"${agent_forward_log}" 2>&1 &
agent_forward_pid=$!

for _ in $(seq 1 30); do
  if curl --fail --silent "http://127.0.0.1:${agent_local_port}/readyz" >/dev/null; then
    break
  fi
  sleep 1
done
curl --fail --silent --show-error \
  "http://127.0.0.1:${agent_local_port}/readyz" >/dev/null

mcp_call() {
  local tool_name="$1"
  local arguments="$2"
  jq -nc \
    --arg name "${tool_name}" \
    --argjson arguments "${arguments}" \
    '{jsonrpc:"2.0",id:1,method:"tools/call",params:{name:$name,arguments:$arguments}}' |
    curl --fail --silent --show-error \
      --header 'Content-Type: application/json' \
      --header 'Accept: application/json, text/event-stream' \
      --header 'MCP-Protocol-Version: 2025-06-18' \
      --data-binary @- \
      "http://127.0.0.1:${agent_local_port}/mcp"
}

assert_mcp_result() {
  local label="$1"
  local expression="$2"
  local payload="$3"
  if jq -e "${expression}" <<<"${payload}" >/dev/null; then
    return 0
  fi
  echo "${label} assertion failed; MCP response follows:" >&2
  jq . <<<"${payload}" >&2 || printf '%s\n' "${payload}" >&2
  return 1
}

request="$(
  jq -nc \
    --arg namespace "${model_namespace}" \
    --arg name "${model_name}" \
    --arg catalogId "${catalog_id}" \
    '{namespace:$namespace,name:$name,catalogId:$catalogId,confirm:true}'
)"
deploy_result="$(mcp_call infernex_deploy_model "${request}")"
assert_mcp_result "initial deployment" '
  .result.structuredContent.operation == "created" and
  (.result.structuredContent.changeId | type == "string" and length == 32) and
  .result.structuredContent.changeStatus == "applied" and
  .result.structuredContent.catalogId == "smollm2-135m-q4" and
  .result.structuredContent.resourceKind == "InferNexService"
' "${deploy_result}"

for _ in $(seq 1 60); do
  if kubectl -n "${model_namespace}" get \
    "deployment/${model_name}-engine-aggregate" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
kubectl -n "${model_namespace}" get \
  "deployment/${model_name}-engine-aggregate" >/dev/null
kubectl -n "${model_namespace}" rollout status \
  "deployment/${model_name}-engine-aggregate" \
  --timeout=360s
kubectl -n "${model_namespace}" wait \
  --for=jsonpath='{.status.ready}'=true \
  "infernexservice/${model_name}" \
  --timeout=120s

change_id="$(jq -r '.result.structuredContent.changeId' <<<"${deploy_result}")"
change_result=""
for _ in $(seq 1 30); do
  change_result="$(
    mcp_call infernex_get_change "$(
      jq -nc --arg changeId "${change_id}" '{changeId:$changeId}'
    )"
  )"
  if jq -e '.result.structuredContent.status == "committed"' \
    <<<"${change_result}" >/dev/null; then
    break
  fi
  sleep 1
done
assert_mcp_result "deployment change commit" '
  (.result.structuredContent.id | type == "string" and length == 32) and
  .result.structuredContent.status == "committed"
' "${change_result}"

kubectl -n "${model_namespace}" port-forward \
  "service/${model_name}-engine-aggregate" "${model_local_port}:8080" \
  >"${model_forward_log}" 2>&1 &
model_forward_pid=$!

for _ in $(seq 1 60); do
  if curl --fail --silent "http://127.0.0.1:${model_local_port}/health" >/dev/null; then
    break
  fi
  sleep 1
done
curl --fail --silent --show-error \
  "http://127.0.0.1:${model_local_port}/health" >/dev/null

inference_result="$(
  jq -nc '{
    model:"SmolLM2-135M-Instruct",
    messages:[{role:"user",content:"Reply with one short sentence about Kubernetes."}],
    max_tokens:24,
    temperature:0
  }' |
    curl --fail --silent --show-error \
      --header 'Content-Type: application/json' \
      --data-binary @- \
      "http://127.0.0.1:${model_local_port}/v1/chat/completions"
)"
jq -e '
  (.choices | length) > 0 and
  (.choices[0].message.content | type == "string" and length > 0)
' <<<"${inference_result}" >/dev/null
echo "Real tiny-model inference response:"
jq -c '{
  model,
  content:.choices[0].message.content,
  promptTokens:.usage.prompt_tokens,
  completionTokens:.usage.completion_tokens
}' <<<"${inference_result}"

inspect_request="$(
  jq -nc --arg namespace "${model_namespace}" --arg name "${model_name}" \
    '{namespace:$namespace,name:$name}'
)"
inspect_result=""
for _ in $(seq 1 30); do
  inspect_result="$(mcp_call infernex_inspect_service "${inspect_request}")"
  if jq -e '
    .result.structuredContent.service.ready == true and
    .result.structuredContent.service.model.name == "SmolLM2-135M-Instruct"
  ' <<<"${inspect_result}" >/dev/null; then
    break
  fi
  sleep 1
done
assert_mcp_result "Agent service inspection" '
  .result.structuredContent.service.ready == true and
  .result.structuredContent.service.model.name == "SmolLM2-135M-Instruct"
' "${inspect_result}"

topology_result="$(
  mcp_call infernex_get_topology "$(
    jq -nc --arg namespace "${model_namespace}" --arg name "${model_name}" \
      '{namespace:$namespace,name:$name}'
  )"
)"
assert_mcp_result "Agent topology observation" '
  any(
    .result.structuredContent.workloads[];
    .kind == "Deployment" and
    .component == "engine-aggregate" and
    .desired == 1 and
    .ready == 1
  ) and
  any(.result.structuredContent.pods[]; .ready == true)
' "${topology_result}"

second_deploy_result="$(mcp_call infernex_deploy_model "${request}")"
assert_mcp_result "idempotent deployment" '
  .result.structuredContent.operation == "already-exists"
' "${second_deploy_result}"

# Exercise the real automatic rollback path without waiting for a deadline.
# Pause Bridge so it cannot overwrite the injected status, create a separate
# catalog object, then report Degraded for its observed generation.
rollback_name="${model_name}-rollback-probe"
rollback_request="$(
  jq -nc \
    --arg namespace "${model_namespace}" \
    --arg name "${rollback_name}" \
    --arg catalogId "${catalog_id}" \
    '{namespace:$namespace,name:$name,catalogId:$catalogId,confirm:true}'
)"
kubectl -n "${bridge_namespace}" scale \
  "deployment/${bridge_release_name}" --replicas=0
bridge_paused="true"
kubectl -n "${bridge_namespace}" wait \
  --for=delete \
  pod \
  --selector="app.kubernetes.io/instance=${bridge_release_name}" \
  --timeout=60s >/dev/null 2>&1 || true
rollback_deploy_result="$(mcp_call infernex_deploy_model "${rollback_request}")"
rollback_change_id="$(
  jq -r '.result.structuredContent.changeId' <<<"${rollback_deploy_result}"
)"
rollback_generation="$(
  kubectl -n "${model_namespace}" get "infernexservice/${rollback_name}" \
    -o jsonpath='{.metadata.generation}'
)"
kubectl -n "${model_namespace}" patch \
  "infernexservice/${rollback_name}" \
  --subresource=status \
  --type=merge \
  -p "$(
    jq -nc \
      --argjson generation "${rollback_generation}" \
      '{
        status:{
          observedGeneration:$generation,
          ready:false,
          conditions:[{
            type:"Degraded",
            status:"True",
            reason:"KindRollbackInjection",
            message:"Intentional CI rollback verification",
            lastTransitionTime:(now | todate)
          }]
        }
      }'
  )"

rollback_change_result=""
for _ in $(seq 1 30); do
  rollback_change_result="$(
    mcp_call infernex_get_change "$(
      jq -nc --arg changeId "${rollback_change_id}" '{changeId:$changeId}'
    )"
  )"
  if jq -e '.result.structuredContent.status == "rolled-back"' \
    <<<"${rollback_change_result}" >/dev/null; then
    break
  fi
  sleep 1
done
assert_mcp_result "degraded deployment rollback" '
  .result.structuredContent.status == "rolled-back"
' "${rollback_change_result}"
test -z "$(
  kubectl -n "${model_namespace}" get \
    "infernexservice/${rollback_name}" --ignore-not-found -o name
)"
kubectl -n "${bridge_namespace}" scale \
  "deployment/${bridge_release_name}" --replicas=1
kubectl -n "${bridge_namespace}" rollout status \
  "deployment/${bridge_release_name}" --timeout=120s
bridge_paused="false"

delete_result="$(mcp_call infernex_delete_model "${request}")"
assert_mcp_result "catalog deletion" '
  .result.structuredContent.operation == "deleted"
' "${delete_result}"

wait_for_absent() {
  local resource="$1"
  for _ in $(seq 1 60); do
    if [[ -z "$(kubectl -n "${model_namespace}" get "${resource}" --ignore-not-found -o name)" ]]; then
      return 0
    fi
    sleep 2
  done
  echo "${resource} was not deleted" >&2
  return 1
}

wait_for_absent "infernexservice/${model_name}"
wait_for_absent "deployment/${model_name}-engine-aggregate"
wait_for_absent "service/${model_name}-engine-aggregate"

echo "InferNex Agent real tiny-model Kind test passed"
