#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=bundle-lib.sh
source "${script_dir}/bundle-lib.sh"

usage() {
  cat <<'EOF'
Verify an extracted bundle or a deployed InferNex Agent.

Usage:
  verify-agent.sh [options]

Options:
  --bundle-dir DIR              Extracted bundle root
  --checksums-only              Verify bundle files and exit
  --skip-checksums              Skip bundle checksum verification
  --namespace NAMESPACE         Agent namespace (default: infernex-system)
  --release NAME                Helm release (default: infernex-agent)
  --target-namespace NAMESPACE  Verify read RBAC (repeatable)
  --timeout DURATION            Rollout timeout (default: 5m)
  -h, --help                    Show this help
EOF
}

bundle_root="$(bundle_default_root || true)"
checksums_only="false"
verify_checksums="true"
namespace="infernex-system"
release_name="infernex-agent"
timeout="5m"
declare -a target_namespaces=()

while (($#)); do
  case "$1" in
    --bundle-dir)
      [[ $# -ge 2 ]] || bundle_die "--bundle-dir requires a value"
      bundle_root="$2"
      shift 2
      ;;
    --checksums-only)
      checksums_only="true"
      shift
      ;;
    --skip-checksums)
      verify_checksums="false"
      shift
      ;;
    --namespace)
      [[ $# -ge 2 ]] || bundle_die "--namespace requires a value"
      namespace="$2"
      shift 2
      ;;
    --release)
      [[ $# -ge 2 ]] || bundle_die "--release requires a value"
      release_name="$2"
      shift 2
      ;;
    --target-namespace)
      [[ $# -ge 2 ]] || bundle_die "--target-namespace requires a value"
      target_namespaces+=("$2")
      shift 2
      ;;
    --timeout)
      [[ $# -ge 2 ]] || bundle_die "--timeout requires a value"
      timeout="$2"
      shift 2
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      bundle_die "unknown option: $1"
      ;;
  esac
done

if [[ "$verify_checksums" == "true" ]]; then
  [[ -n "$bundle_root" ]] ||
    bundle_die "--bundle-dir is required to verify checksums"
  bundle_root="$(cd -- "$bundle_root" && pwd)"
  bundle_verify_checksums "$bundle_root"
  [[ "$(bundle_property "$bundle_root" format)" == "infernex-agent-offline-v1" ]] ||
    bundle_die "unsupported bundle format"
fi
if [[ "$checksums_only" == "true" ]]; then
  bundle_info "bundle files are valid"
  exit 0
fi

bundle_require_command kubectl
bundle_require_command helm
bundle_require_command curl

kubectl get crd infernexservices.infernex.infernex.io >/dev/null ||
  bundle_die "InferNexService CRD is missing"
helm --namespace "$namespace" status "$release_name" >/dev/null ||
  bundle_die "Helm release is not deployed: ${namespace}/${release_name}"

deployment="$(
  kubectl --namespace "$namespace" get deployment \
    -l "app.kubernetes.io/instance=${release_name},app.kubernetes.io/name=infernex-agent" \
    -o jsonpath='{.items[0].metadata.name}'
)"
[[ -n "$deployment" ]] || bundle_die "Agent Deployment was not found"

bundle_info "waiting for deployment/${deployment}"
kubectl --namespace "$namespace" rollout status "deployment/${deployment}" \
  --timeout "$timeout"

service_account="$(
  kubectl --namespace "$namespace" get deployment "$deployment" \
    -o jsonpath='{.spec.template.spec.serviceAccountName}'
)"
for target_namespace in "${target_namespaces[@]}"; do
  allowed="$(
    kubectl auth can-i list infernexservices.infernex.infernex.io \
      --namespace "$target_namespace" \
      --as "system:serviceaccount:${namespace}:${service_account}"
  )"
  [[ "$allowed" == "yes" ]] ||
    bundle_die "Agent cannot list InferNexService in ${target_namespace}"
done

mcp_port=$((20000 + ($$ % 10000)))
dashboard_port=$((mcp_port + 1))
port_forward_log="$(mktemp "${TMPDIR:-/tmp}/infernex-agent-port-forward.XXXXXX")"
port_forward_pid=""
cleanup() {
  if [[ -n "$port_forward_pid" ]]; then
    kill "$port_forward_pid" >/dev/null 2>&1 || true
    wait "$port_forward_pid" >/dev/null 2>&1 || true
  fi
  rm -f -- "$port_forward_log"
}
trap cleanup EXIT

kubectl --namespace "$namespace" port-forward "deployment/${deployment}" \
  "${mcp_port}:8080" "${dashboard_port}:8081" \
  >"$port_forward_log" 2>&1 &
port_forward_pid="$!"

ready="false"
for _ in $(seq 1 30); do
  if curl --fail --silent --show-error \
    "http://127.0.0.1:${mcp_port}/readyz" >/dev/null 2>&1 &&
    curl --fail --silent --show-error \
      "http://127.0.0.1:${dashboard_port}/readyz" >/dev/null 2>&1; then
    ready="true"
    break
  fi
  if ! kill -0 "$port_forward_pid" >/dev/null 2>&1; then
    cat "$port_forward_log" >&2
    bundle_die "kubectl port-forward exited early"
  fi
  sleep 1
done
[[ "$ready" == "true" ]] || {
  cat "$port_forward_log" >&2
  bundle_die "Agent health endpoints did not become ready"
}

snapshot="$(
  curl --fail --silent --show-error \
    "http://127.0.0.1:${dashboard_port}/api/v1/snapshot"
)"
[[ "$snapshot" == *'"services"'* ]] ||
  bundle_die "dashboard snapshot response is invalid"

pod_node="$(
  kubectl --namespace "$namespace" get pods \
    -l "app.kubernetes.io/instance=${release_name},app.kubernetes.io/name=infernex-agent" \
    -o jsonpath='{.items[0].spec.nodeName}'
)"
bundle_info "Agent is healthy on node ${pod_node}"
bundle_info "MCP and dashboard health checks passed"
