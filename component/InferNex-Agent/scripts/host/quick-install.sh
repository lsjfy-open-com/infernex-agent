#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
if [[ -f "${script_dir}/bin/bundle-lib.sh" ]]; then
  bundle_root="$script_dir"
  # shellcheck source=/dev/null
  source "${script_dir}/bin/bundle-lib.sh"
elif [[ -f "${script_dir}/bundle-lib.sh" ]]; then
  bundle_root="$(cd -- "${script_dir}/../.." && pwd)"
  # shellcheck source=/dev/null
  source "${script_dir}/bundle-lib.sh"
else
  bundle_root=""
  # shellcheck source=../offline/bundle-lib.sh
  source "${script_dir}/../offline/bundle-lib.sh"
fi

usage() {
  cat <<'EOF'
Install InferNex Agent on an existing InferNex management node.

Normal usage (no parameters):
  sudo ./install.sh

The installer automatically discovers the current kubeconfig, InferNex CRDs,
Bridge profiles, existing InferNexService namespaces, and host CPU
architecture. It installs one local Agent process and no Kubernetes Pod,
controller, or CRD. The only interactive configuration is the
OpenAI-compatible model endpoint used by the Agent.

Advanced recovery/automation options:
  --admin-kubeconfig FILE       Override kubeconfig discovery
  --bundle-dir DIR              Override the extracted Agent package directory
  --dashboard-listen-address A  Default: 127.0.0.1:8081
  --hardened-identity          Create a dedicated ServiceAccount/RBAC identity
  --generic-kubernetes        Force base Kubernetes/Helm compatibility mode
  --skip-model-setup            Install first; configure the model later
  --non-interactive             Do not read from the terminal
  -h, --help                    Show this help
EOF
}

admin_kubeconfig=""
dashboard_listen_address="127.0.0.1:8081"
dashboard_listen_address_explicit="false"
skip_model_setup="false"
non_interactive="false"
hardened_identity="false"
force_generic_kubernetes="false"
workspace_namespace="infernex-agent-workspace"

while (($#)); do
  case "$1" in
    --admin-kubeconfig)
      [[ $# -ge 2 ]] || bundle_die "--admin-kubeconfig requires a value"
      admin_kubeconfig="$2"
      shift 2
      ;;
    --bundle-dir)
      [[ $# -ge 2 ]] || bundle_die "--bundle-dir requires a value"
      bundle_root="$2"
      shift 2
      ;;
    --dashboard-listen-address)
      [[ $# -ge 2 ]] || bundle_die "--dashboard-listen-address requires a value"
      dashboard_listen_address="$2"
      dashboard_listen_address_explicit="true"
      shift 2
      ;;
    --skip-model-setup)
      skip_model_setup="true"
      shift
      ;;
    --hardened-identity)
      hardened_identity="true"
      shift
      ;;
    --generic-kubernetes)
      force_generic_kubernetes="true"
      shift
      ;;
    --non-interactive)
      non_interactive="true"
      skip_model_setup="true"
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *) bundle_die "unknown option: $1" ;;
  esac
done

[[ ${EUID} -eq 0 ]] || bundle_die "run the one-command installer with sudo"
bundle_require_command kubectl
bundle_require_command sort
bundle_require_command mktemp
bundle_require_command readlink
bundle_require_command awk
bundle_require_command grep

address_port() {
  printf '%s' "${1##*:}"
}

port_is_listening() {
  local port hex_port
  port="$(address_port "$1")"
  [[ "$port" =~ ^[0-9]+$ ]] || return 1
  printf -v hex_port '%04X' "$port"
  awk -v wanted="$hex_port" '
    NR > 1 {
      split($2, address, ":")
      if (toupper(address[length(address)]) == wanted && $4 == "0A") {
        found = 1
      }
    }
    END { exit found ? 0 : 1 }
  ' /proc/net/tcp /proc/net/tcp6 2>/dev/null
}

address_is_current_agent_listener() {
  local address="$1"
  systemctl is-active --quiet infernex-agent.service 2>/dev/null &&
    grep -Fxq -- "--dashboard-listen-address=${address}" \
      /etc/infernex-agent/agent.conf 2>/dev/null
}

listener_evidence() {
  local port
  port="$(address_port "$1")"
  if command -v ss >/dev/null 2>&1; then
    ss -H -ltnp "sport = :${port}" 2>/dev/null || true
  else
    printf 'TCP port %s is present in /proc/net/tcp but ss is unavailable\n' "$port"
  fi
}

select_dashboard_listener() {
  local requested="$1" host candidate
  if ! port_is_listening "$requested" ||
    address_is_current_agent_listener "$requested"; then
    printf '%s' "$requested"
    return
  fi

  bundle_warn "dashboard address ${requested} is already used by another process"
  listener_evidence "$requested" >&2
  [[ "$dashboard_listen_address_explicit" != "true" ]] || bundle_die \
    "the explicitly requested dashboard address is unavailable"

  host="${requested%:*}"
  for port in 18081 28081 38081 48081 58081; do
    candidate="${host}:${port}"
    if ! port_is_listening "$candidate"; then
      bundle_warn "using the next available dashboard address: ${candidate}"
      printf '%s' "$candidate"
      return
    fi
  done
  bundle_die "no available default dashboard port was found"
}

[[ -n "$bundle_root" && -d "$bundle_root" ]] ||
  bundle_die "an extracted InferNex Agent package is required"
bundle_root="$(cd -- "$bundle_root" && pwd)"
install_host="${bundle_root}/bin/install-host.sh"
configure_model="${bundle_root}/bin/configure-model.sh"
create_kubeconfig="${bundle_root}/bin/create-kubeconfig.sh"
[[ -x "$install_host" && -x "$configure_model" ]] ||
  bundle_die "bundle is incomplete; expected install helpers under ${bundle_root}/bin"
if [[ "$hardened_identity" == "true" && ! -x "$create_kubeconfig" ]]; then
  bundle_die "bundle lacks the optional hardened-identity helper"
fi

kubeconfig_works() {
  local candidate="$1"
  [[ -f "$candidate" && -r "$candidate" ]] || return 1
  kubectl --kubeconfig "$candidate" --request-timeout=10s get --raw=/version \
    >/dev/null 2>&1
}

discover_kubeconfig() {
  local candidate sudo_home=""
  declare -a candidates=()
  [[ -z "$admin_kubeconfig" ]] || candidates+=("$admin_kubeconfig")
  if [[ -n "${KUBECONFIG:-}" ]]; then
    IFS=: read -r -a env_candidates <<<"${KUBECONFIG}"
    candidates+=("${env_candidates[@]}")
  fi
  if [[ -n "${SUDO_USER:-}" && "${SUDO_USER}" != "root" ]]; then
    if command -v getent >/dev/null 2>&1; then
      sudo_home="$(getent passwd "${SUDO_USER}" | awk -F: '{print $6}')"
    fi
    [[ -n "$sudo_home" ]] || sudo_home="/home/${SUDO_USER}"
    candidates+=("${sudo_home}/.kube/config")
  fi
  candidates+=(
    "/root/.kube/config"
    "/etc/kubernetes/admin.conf"
    "/etc/rancher/k3s/k3s.yaml"
  )
  for candidate in "${candidates[@]}"; do
    [[ -n "$candidate" ]] || continue
    if kubeconfig_works "$candidate"; then
      readlink -f -- "$candidate"
      return 0
    fi
  done
  return 1
}

admin_kubeconfig="$(discover_kubeconfig || true)"
[[ -n "$admin_kubeconfig" ]] || bundle_die \
  "no working management kubeconfig was found (checked the invoking user's ~/.kube/config, /etc/kubernetes/admin.conf, and k3s)"
bundle_info "using the current Kubernetes context from: ${admin_kubeconfig}"
dashboard_listen_address="$(select_dashboard_listener "$dashboard_listen_address")"

kubectl_admin=(kubectl --kubeconfig "$admin_kubeconfig" --request-timeout=15s)
platform_mode="generic-kubernetes"
if [[ "$force_generic_kubernetes" != "true" ]] &&
  "${kubectl_admin[@]}" get crd infernexservices.infernex.infernex.io >/dev/null 2>&1 &&
  "${kubectl_admin[@]}" get crd infernexserviceconfigs.infernex.infernex.io >/dev/null 2>&1; then
  platform_mode="bridge"
fi

template_namespace=""
declare -a discovered_namespaces=()
if [[ "$platform_mode" == "bridge" ]]; then
  template_namespace="$(
    "${kubectl_admin[@]}" get infernexserviceconfigs.infernex.infernex.io -A \
      -o go-template='{{range .items}}{{if eq .metadata.name "infernex-default-aggregate-template"}}{{.metadata.namespace}}{{"\n"}}{{end}}{{end}}' |
      awk 'NF {print; exit}'
  )"
  if [[ -z "$template_namespace" ]]; then
    template_namespace="$(
      "${kubectl_admin[@]}" get infernexserviceconfigs.infernex.infernex.io -A \
        -o go-template='{{range .items}}{{.metadata.namespace}}{{"\n"}}{{end}}' |
        awk 'NF {print; exit}'
    )"
  fi
  [[ -n "$template_namespace" ]] || bundle_die \
    "InferNex Bridge CRDs exist, but no InferNexServiceConfig profile was found"
  bundle_info "detected InferNex Bridge mode; profile namespace: ${template_namespace}"

  bundle_info "ensuring the Agent-owned deployment workspace exists"
  if "${kubectl_admin[@]}" get namespace "$workspace_namespace" >/dev/null 2>&1; then
    workspace_owner="$(
      "${kubectl_admin[@]}" get namespace "$workspace_namespace" \
        -o jsonpath='{.metadata.labels.agent\.infernex\.io/workspace}'
    )"
    [[ "$workspace_owner" == "true" ]] || bundle_die \
      "namespace ${workspace_namespace} already exists without the Agent workspace label; refusing to claim it"
  else
    cat <<EOF | kubectl --kubeconfig "$admin_kubeconfig" apply -f - >/dev/null
apiVersion: v1
kind: Namespace
metadata:
  name: ${workspace_namespace}
  labels:
    agent.infernex.io/workspace: "true"
    app.kubernetes.io/managed-by: infernex-agent
EOF
  fi

  mapfile -t discovered_namespaces < <(
    "${kubectl_admin[@]}" get infernexservices.infernex.infernex.io -A \
      -o go-template='{{range .items}}{{.metadata.namespace}}{{"\n"}}{{end}}' |
      awk 'NF' | sort -u
  )
  discovered_namespaces+=("$workspace_namespace")
  mapfile -t discovered_namespaces < <(printf '%s\n' "${discovered_namespaces[@]}" | awk 'NF' | sort -u)
  bundle_info "discovered InferNexService namespaces: ${discovered_namespaces[*]}"
else
  [[ "$hardened_identity" != "true" ]] || bundle_die \
    "--hardened-identity currently requires InferNex Bridge CRDs; use the current kubeconfig for base compatibility mode"
  bundle_warn "InferNex Bridge CRDs were not found; installing in base Kubernetes/Helm compatibility mode"
  bundle_info "this mode changes no Kubernetes resources and keeps Bridge-specific deployment disabled"
fi
runtime_kubeconfig="$(mktemp /tmp/infernex-agent-runtime-kubeconfig.XXXXXX)"
cleanup() {
  rm -f -- "$runtime_kubeconfig"
}
trap cleanup EXIT

if [[ "$platform_mode" == "bridge" && "$hardened_identity" == "true" ]]; then
  create_args=(
    --admin-kubeconfig "$admin_kubeconfig"
    --output "$runtime_kubeconfig"
    --force
    --enable-deployment
    --deployment-namespace "$workspace_namespace"
    --deployment-template-namespace "$template_namespace"
    --enable-log-diagnostics
  )
  for namespace in "${discovered_namespaces[@]}"; do
    create_args+=(--target-namespace "$namespace")
  done
  bundle_info "creating the optional dedicated Agent identity and scoped RBAC"
  "$create_kubeconfig" "${create_args[@]}"
else
  # Match kubectl-ai/K8sGPT's local-CLI model: reuse the operator's current
  # context, but flatten it into a protected service credential so the Agent
  # never depends on the invoking user's home directory.
  umask 077
  kubectl --kubeconfig "$admin_kubeconfig" config view \
    --raw --flatten --minify >"$runtime_kubeconfig"
  [[ -s "$runtime_kubeconfig" ]] || bundle_die "could not flatten the current kubeconfig"
  if grep -Eq '^[[:space:]]+(exec|auth-provider):' "$runtime_kubeconfig"; then
    bundle_die "the current kubeconfig depends on an external credential plugin; rerun with --hardened-identity or provide a self-contained admin.conf"
  fi
  bundle_info "reusing the current kubectl identity; no ServiceAccount or RBAC objects were created"
fi

install_args=(
  --bundle-dir "$bundle_root"
  --kubeconfig "$runtime_kubeconfig"
  --dashboard-listen-address "$dashboard_listen_address"
)
if [[ "$skip_model_setup" != "true" && "$non_interactive" != "true" ]]; then
  if [[ -r /dev/tty && -w /dev/tty ]]; then
    install_args+=(--interactive-model-setup)
  else
    bundle_warn "no interactive terminal detected; model setup was skipped"
  fi
fi
if [[ "$platform_mode" == "bridge" ]]; then
  install_args+=(
    --enable-deployment
    --deployment-namespace "$workspace_namespace"
    --deployment-template-namespace "$template_namespace"
    --enable-log-diagnostics
  )
  for namespace in "${discovered_namespaces[@]}"; do
    install_args+=(--scan-namespace "$namespace")
  done
else
  install_args+=(--generic-kubernetes)
fi

bundle_info "installing the static Agent binary and systemd service"
"$install_host" "${install_args[@]}"

trap - EXIT
cleanup
bundle_info "InferNex Agent is ready"
bundle_info "detected platform mode: ${platform_mode}"
bundle_info "start the Agentic terminal with: sudo infernex-agent chat"
bundle_info "dashboard: http://${dashboard_listen_address}/ (use an SSH tunnel when bound to 127.0.0.1)"
