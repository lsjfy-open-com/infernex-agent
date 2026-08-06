#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
component_dir="${repo_root}/component/InferNex-Agent"
chart_dir="${component_dir}/chart/infernex-agent"
fixture_file="${component_dir}/test/e2e/fixtures.yaml"
config_crd="${repo_root}/component/InferNex-Bridge/config/crd/bases/infernex.infernex.io_infernexserviceconfigs.yaml"

agent_namespace="${AGENT_NAMESPACE:-infernex-system}"
model_namespace="models"
release_name="infernex-agent"
image_repository="${AGENT_IMAGE_REPOSITORY:-infernex-agent}"
image_tag="${AGENT_IMAGE_TAG:-e2e}"
local_port="${AGENT_LOCAL_PORT:-18080}"
dashboard_local_port="${AGENT_DASHBOARD_LOCAL_PORT:-18081}"

kubectl apply --server-side -f "${config_crd}"
kubectl wait --for=condition=Established \
  crd/infernexserviceconfigs.infernex.infernex.io \
  --timeout=60s
kubectl apply -f "${fixture_file}"

helm upgrade --install "${release_name}" "${chart_dir}" \
  --namespace "${agent_namespace}" \
  --create-namespace \
  --set "image.repository=${image_repository}" \
  --set "image.tag=${image_tag}" \
  --set "image.pullPolicy=Never" \
  --set "rbac.targetNamespaces[0]=${model_namespace}" \
  --set "supervisor.scanInterval=5s" \
  --set "supervisor.diagnostics.logs.enabled=true" \
  --set "experiments.enabled=true" \
  --set "experiments.templateNamespace=infernex-bridge-system" \
  --set "experiments.readinessTimeout=45s" \
  --set "experiments.soakDuration=1s" \
  --set "experiments.diagnosticInterval=1s"

kubectl -n "${agent_namespace}" rollout status \
  "deployment/${release_name}" \
  --timeout=120s

kubectl -n "${model_namespace}" patch infernexservice smoke \
  --subresource=status \
  --type=merge \
  --patch='{"status":{"mode":"aggregate","ready":true,"observedGeneration":1,"components":{"inferenceEngine":{"ready":true}},"conditions":[{"type":"Ready","status":"True","reason":"SmokeReady","message":"smoke service is ready","observedGeneration":1,"lastTransitionTime":"2026-07-30T00:00:00Z"}]}}'

service_uid="$(kubectl -n "${model_namespace}" get infernexservice smoke -o jsonpath='{.metadata.uid}')"
event_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
kubectl create -f - <<EOF
apiVersion: v1
kind: Event
metadata:
  name: smoke-event
  namespace: ${model_namespace}
involvedObject:
  apiVersion: infernex.infernex.io/v1alpha1
  kind: InferNexService
  name: smoke
  namespace: ${model_namespace}
  uid: ${service_uid}
type: Warning
reason: SmokeEvent
message: synthetic event for InferNex Agent smoke testing
source:
  component: infernex-agent-e2e
firstTimestamp: ${event_time}
lastTimestamp: ${event_time}
count: 1
EOF

service_account="system:serviceaccount:${agent_namespace}:${release_name}"
test "$(kubectl auth can-i list events --as="${service_account}" -n "${model_namespace}")" = "yes"
test "$(kubectl auth can-i get pods/log --as="${service_account}" -n "${model_namespace}")" = "yes"
test "$(kubectl auth can-i create infernexservices.infernex.infernex.io --as="${service_account}" -n "${model_namespace}")" = "yes"
test "$(kubectl auth can-i delete infernexservices.infernex.infernex.io --as="${service_account}" -n "${model_namespace}")" = "yes"
test "$(kubectl auth can-i get infernexserviceconfigs.infernex.infernex.io --as="${service_account}" -n infernex-bridge-system)" = "yes"
test "$(kubectl auth can-i get secrets --as="${service_account}" -n "${model_namespace}")" = "no"
test "$(kubectl auth can-i create deployments --as="${service_account}" -n "${model_namespace}")" = "no"

port_forward_log="$(mktemp)"
dashboard_port_forward_log="$(mktemp)"
kubectl -n "${agent_namespace}" port-forward \
  "service/${release_name}" "${local_port}:8080" \
  >"${port_forward_log}" 2>&1 &
port_forward_pid=$!
kubectl -n "${agent_namespace}" port-forward \
  "service/${release_name}-dashboard" "${dashboard_local_port}:8081" \
  >"${dashboard_port_forward_log}" 2>&1 &
dashboard_port_forward_pid=$!
cleanup() {
  kill "${port_forward_pid}" >/dev/null 2>&1 || true
  kill "${dashboard_port_forward_pid}" >/dev/null 2>&1 || true
  rm -f "${port_forward_log}" "${dashboard_port_forward_log}"
}
trap cleanup EXIT

for _ in $(seq 1 30); do
  if curl --fail --silent "http://127.0.0.1:${local_port}/readyz" >/dev/null; then
    break
  fi
  sleep 1
done
if ! curl --fail --silent --show-error \
  "http://127.0.0.1:${local_port}/readyz" >/dev/null; then
  cat "${port_forward_log}"
  exit 1
fi
for _ in $(seq 1 30); do
  if curl --fail --silent \
    "http://127.0.0.1:${dashboard_local_port}/readyz" >/dev/null; then
    break
  fi
  sleep 1
done
dashboard_snapshot="$(
  curl --fail --silent --show-error \
    "http://127.0.0.1:${dashboard_local_port}/api/v1/snapshot"
)"
jq -e '
  .ready == true and
  any(.namespaces[]; .name == "models") and
  any(.namespaces[].services[]; .detail.service.name == "smoke")
' <<<"${dashboard_snapshot}" >/dev/null
curl --fail --silent --show-error \
  "http://127.0.0.1:${dashboard_local_port}/" |
  grep -q "InferNex"

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
      "http://127.0.0.1:${local_port}/mcp"
}

list_result="$(mcp_call infernex_list_services '{"namespace":"models"}')"
jq -e '.result.structuredContent.services[0].name == "smoke"' <<<"${list_result}" >/dev/null

inspect_result="$(mcp_call infernex_inspect_service '{"namespace":"models","name":"smoke"}')"
jq -e '
  .result.structuredContent.service.ready == true and
  .result.structuredContent.service.model.uri == "https://example.invalid/models/smoke"
' <<<"${inspect_result}" >/dev/null

topology_result="$(mcp_call infernex_get_topology '{"namespace":"models","name":"smoke"}')"
jq -e '
  any(
    .result.structuredContent.workloads[];
    .kind == "Deployment" and
    .name == "smoke-engine" and
    .component == "engine-aggregate"
  )
' <<<"${topology_result}" >/dev/null

events_result="$(mcp_call infernex_get_events '{"namespace":"models","name":"smoke","sinceMinutes":60,"limit":10}')"
jq -e '
  any(
    .result.structuredContent.events[];
    .reason == "SmokeEvent" and
    .kind == "InferNexService" and
    .name == "smoke"
  )
' <<<"${events_result}" >/dev/null

diagnostic_result="$(mcp_call infernex_diagnose_service '{"namespace":"models","name":"smoke","sinceMinutes":60}')"
jq -e '
  any(
    .result.structuredContent.incidents[];
    .rootCategory == "kubernetes-warning" and .severity == "warning"
  )
' <<<"${diagnostic_result}" >/dev/null

start_result="$(mcp_call infernex_start_experiment '{"namespace":"models","baselineName":"smoke","candidatePrefix":"smoke-pass","featureProfiles":["smoke-feature-mooncake"],"confirm":true}')"
experiment_id="$(jq -er '.result.structuredContent.id' <<<"${start_result}")"
for _ in $(seq 1 30); do
  if kubectl -n "${model_namespace}" get infernexservice smoke-pass-s01 >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
kubectl -n "${model_namespace}" get infernexservice smoke-pass-s01 >/dev/null
candidate_generation="$(kubectl -n "${model_namespace}" get infernexservice smoke-pass-s01 -o jsonpath='{.metadata.generation}')"
kubectl -n "${model_namespace}" patch infernexservice smoke-pass-s01 \
  --subresource=status \
  --type=merge \
  --patch="{\"status\":{\"mode\":\"aggregate\",\"ready\":true,\"observedGeneration\":${candidate_generation},\"conditions\":[{\"type\":\"Ready\",\"status\":\"True\",\"reason\":\"ExperimentReady\",\"observedGeneration\":${candidate_generation},\"lastTransitionTime\":\"2026-08-06T00:00:00Z\"}]}}"

pass_plan=""
for _ in $(seq 1 30); do
  pass_plan="$(mcp_call infernex_get_experiment "$(jq -nc --arg id "${experiment_id}" '{experimentId:$id}')")"
  [[ "$(jq -r '.result.structuredContent.status' <<<"${pass_plan}")" == "completed" ]] && break
  sleep 1
done
jq -e '
  .result.structuredContent.status == "completed" and
  .result.structuredContent.stableService == "smoke-pass-s01" and
  .result.structuredContent.stages[0].status == "passed"
' <<<"${pass_plan}" >/dev/null
kubectl -n "${model_namespace}" get infernexservice smoke smoke-pass-s01 >/dev/null

rollback_result="$(mcp_call infernex_start_experiment '{"namespace":"models","baselineName":"smoke","candidatePrefix":"smoke-fail","featureProfiles":["smoke-feature-mooncake"],"confirm":true}')"
rollback_experiment_id="$(jq -er '.result.structuredContent.id' <<<"${rollback_result}")"
for _ in $(seq 1 30); do
  if kubectl -n "${model_namespace}" get infernexservice smoke-fail-s01 >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
candidate_uid="$(kubectl -n "${model_namespace}" get infernexservice smoke-fail-s01 -o jsonpath='{.metadata.uid}')"
candidate_generation="$(kubectl -n "${model_namespace}" get infernexservice smoke-fail-s01 -o jsonpath='{.metadata.generation}')"
failure_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
kubectl create -f - <<EOF
apiVersion: v1
kind: Event
metadata:
  name: smoke-fail-npu
  namespace: ${model_namespace}
involvedObject:
  apiVersion: infernex.infernex.io/v1alpha1
  kind: InferNexService
  name: smoke-fail-s01
  namespace: ${model_namespace}
  uid: ${candidate_uid}
type: Warning
reason: NPUDeviceLost
message: NPU device lost after ACL error
source:
  component: infernex-agent-e2e
firstTimestamp: ${failure_time}
lastTimestamp: ${failure_time}
count: 1
EOF
kubectl -n "${model_namespace}" patch infernexservice smoke-fail-s01 \
  --subresource=status \
  --type=merge \
  --patch="{\"status\":{\"mode\":\"aggregate\",\"ready\":true,\"observedGeneration\":${candidate_generation},\"conditions\":[{\"type\":\"Ready\",\"status\":\"True\",\"reason\":\"ExperimentReady\",\"observedGeneration\":${candidate_generation},\"lastTransitionTime\":\"2026-08-06T00:00:00Z\"}]}}"

failed_plan=""
for _ in $(seq 1 30); do
  failed_plan="$(mcp_call infernex_get_experiment "$(jq -nc --arg id "${rollback_experiment_id}" '{experimentId:$id}')")"
  [[ "$(jq -r '.result.structuredContent.status' <<<"${failed_plan}")" == "failed" ]] && break
  sleep 1
done
jq -e '
  .result.structuredContent.status == "failed" and
  .result.structuredContent.stableService == "smoke" and
  .result.structuredContent.stages[0].status == "rolled-back" and
  any(.result.structuredContent.stages[0].comparison.regressionCategories[]; . == "npu-device-failure")
' <<<"${failed_plan}" >/dev/null
if kubectl -n "${model_namespace}" get infernexservice smoke-fail-s01 >/dev/null 2>&1; then
  echo "failed experiment candidate was not rolled back" >&2
  exit 1
fi
kubectl -n "${model_namespace}" get infernexservice smoke >/dev/null

experiment_snapshot="$(curl --fail --silent --show-error "http://127.0.0.1:${dashboard_local_port}/api/v1/experiments")"
jq -e --arg pass "${experiment_id}" --arg failed "${rollback_experiment_id}" '
  any(.[]; .id == $pass and .status == "completed") and
  any(.[]; .id == $failed and .status == "failed")
' <<<"${experiment_snapshot}" >/dev/null

echo "InferNex Agent Kind smoke test passed"
