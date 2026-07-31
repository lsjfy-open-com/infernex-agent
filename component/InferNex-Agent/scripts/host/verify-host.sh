#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
if [[ -f "${script_dir}/bundle-lib.sh" ]]; then
  # shellcheck source=/dev/null
  source "${script_dir}/bundle-lib.sh"
else
  # shellcheck source=../offline/bundle-lib.sh
  source "${script_dir}/../offline/bundle-lib.sh"
fi

usage() {
  cat <<'EOF'
Verify the host/systemd InferNex Agent.

Usage:
  verify-host.sh [options]

Options:
  --service NAME                 systemd unit (default: infernex-agent.service)
  --mcp-url URL                  Base health URL (default: http://127.0.0.1:8080)
  --dashboard-url URL            Dashboard base URL (default: http://127.0.0.1:8081)
  --kubeconfig FILE              Verify the installed Kubernetes identity
  --target-namespace NAMESPACE   Verify namespace read permission (repeatable)
  -h, --help                     Show this help
EOF
}

service_name="infernex-agent.service"
mcp_url="http://127.0.0.1:8080"
dashboard_url="http://127.0.0.1:8081"
kubeconfig=""
declare -a target_namespaces=()

while (($#)); do
  case "$1" in
    --service)
      [[ $# -ge 2 ]] || bundle_die "--service requires a value"
      service_name="$2"
      shift 2
      ;;
    --mcp-url)
      [[ $# -ge 2 ]] || bundle_die "--mcp-url requires a value"
      mcp_url="${2%/}"
      shift 2
      ;;
    --dashboard-url)
      [[ $# -ge 2 ]] || bundle_die "--dashboard-url requires a value"
      dashboard_url="${2%/}"
      shift 2
      ;;
    --kubeconfig)
      [[ $# -ge 2 ]] || bundle_die "--kubeconfig requires a value"
      kubeconfig="$2"
      shift 2
      ;;
    --target-namespace)
      [[ $# -ge 2 ]] || bundle_die "--target-namespace requires a value"
      target_namespaces+=("$2")
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

bundle_require_command systemctl
bundle_require_command curl
if ! systemctl is-active --quiet "$service_name"; then
  systemctl status "$service_name" --no-pager >&2 || true
  bundle_die "${service_name} is not active"
fi

ready="false"
for _ in $(seq 1 30); do
  if curl --fail --silent --show-error "${mcp_url}/readyz" >/dev/null 2>&1 &&
    curl --fail --silent --show-error "${dashboard_url}/readyz" >/dev/null 2>&1; then
    ready="true"
    break
  fi
  sleep 1
done
if [[ "$ready" != "true" ]]; then
  journalctl -u "$service_name" --no-pager -n 100 >&2 || true
  bundle_die "Agent health endpoints did not become ready"
fi

snapshot="$(curl --fail --silent --show-error "${dashboard_url}/api/v1/snapshot")"
[[ "$snapshot" == *'"services"'* ]] ||
  bundle_die "dashboard snapshot response is invalid"

if [[ -n "$kubeconfig" ]]; then
  bundle_require_command kubectl
  [[ -r "$kubeconfig" ]] || bundle_die "kubeconfig is not readable"
  for target_namespace in "${target_namespaces[@]}"; do
    kubectl --kubeconfig "$kubeconfig" \
      get infernexservices.infernex.infernex.io \
      --namespace "$target_namespace" --request-timeout=10s >/dev/null ||
      bundle_die "installed identity cannot reach InferNexService API in ${target_namespace}"
    [[ "$(
      kubectl --kubeconfig "$kubeconfig" auth can-i \
        list infernexservices.infernex.infernex.io --namespace "$target_namespace"
    )" == "yes" ]] ||
      bundle_die "installed identity cannot list InferNexService in ${target_namespace}"
  done
fi

bundle_info "systemd service, Kubernetes identity, MCP, and dashboard are healthy"
