#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
component_dir="${repo_root}/component/InferNex-Agent"
chart_dir="${component_dir}/chart/infernex-agent"
fixture_file="${component_dir}/test/e2e/fixtures.yaml"

agent_namespace="${AGENT_NAMESPACE:-infernex-system}"
model_namespace="models"
release_name="infernex-agent"
image_repository="${AGENT_IMAGE_REPOSITORY:-infernex-agent}"
image_tag="${AGENT_IMAGE_TAG:-e2e}"
local_port="${AGENT_LOCAL_PORT:-18080}"

kubectl apply -f "${fixture_file}"

helm upgrade --install "${release_name}" "${chart_dir}" \
  --namespace "${agent_namespace}" \
  --create-namespace \
  --set "image.repository=${image_repository}" \
  --set "image.tag=${image_tag}" \
  --set "image.pullPolicy=Never" \
  --set "rbac.targetNamespaces[0]=${model_namespace}"

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
test "$(kubectl auth can-i get secrets --as="${service_account}" -n "${model_namespace}")" = "no"
test "$(kubectl auth can-i create deployments --as="${service_account}" -n "${model_namespace}")" = "no"

port_forward_log="$(mktemp)"
kubectl -n "${agent_namespace}" port-forward \
  "service/${release_name}" "${local_port}:8080" \
  >"${port_forward_log}" 2>&1 &
port_forward_pid=$!
cleanup() {
  kill "${port_forward_pid}" >/dev/null 2>&1 || true
  rm -f "${port_forward_log}"
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

echo "InferNex Agent Kind smoke test passed"
