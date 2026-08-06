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

The installer automatically discovers the management kubeconfig, InferNex
CRDs, Bridge template namespace, existing InferNexService namespaces, and host
CPU architecture. It creates a dedicated least-privilege Agent identity and a
fixed Agent workspace. The only interactive configuration is the
OpenAI-compatible model endpoint used by the Agent.

Advanced recovery/automation options:
  --admin-kubeconfig FILE       Override kubeconfig discovery
  --bundle-dir DIR              Override extracted host bundle directory
  --dashboard-listen-address A  Default: 127.0.0.1:8081
  --skip-model-setup            Install first; configure the model later
  --non-interactive             Do not read from the terminal
  -h, --help                    Show this help
EOF
}

admin_kubeconfig=""
dashboard_listen_address="127.0.0.1:8081"
skip_model_setup="false"
non_interactive="false"
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
      shift 2
      ;;
    --skip-model-setup)
      skip_model_setup="true"
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

[[ -n "$bundle_root" && -d "$bundle_root" ]] ||
  bundle_die "an extracted host bundle is required"
bundle_root="$(cd -- "$bundle_root" && pwd)"
create_kubeconfig="${bundle_root}/bin/create-kubeconfig.sh"
install_host="${bundle_root}/bin/install-host.sh"
configure_model="${bundle_root}/bin/configure-model.sh"
[[ -x "$create_kubeconfig" && -x "$install_host" && -x "$configure_model" ]] ||
  bundle_die "bundle is incomplete; expected install helpers under ${bundle_root}/bin"

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
bundle_info "discovered Kubernetes management identity: ${admin_kubeconfig}"

kubectl_admin=(kubectl --kubeconfig "$admin_kubeconfig" --request-timeout=15s)
"${kubectl_admin[@]}" get crd infernexservices.infernex.infernex.io >/dev/null ||
  bundle_die "this cluster does not contain the InferNexService CRD; install InferNex first"
"${kubectl_admin[@]}" get crd infernexserviceconfigs.infernex.infernex.io >/dev/null ||
  bundle_die "this cluster does not contain the InferNexServiceConfig CRD; install/upgrade InferNex Bridge first"

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
  "no InferNexServiceConfig was found; Bridge must provide at least one engine profile"
bundle_info "discovered InferNex Bridge profile namespace: ${template_namespace}"

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

declare -a discovered_namespaces=()
mapfile -t discovered_namespaces < <(
  "${kubectl_admin[@]}" get infernexservices.infernex.infernex.io -A \
    -o go-template='{{range .items}}{{.metadata.namespace}}{{"\n"}}{{end}}' |
    awk 'NF' | sort -u
)
discovered_namespaces+=("$workspace_namespace")
mapfile -t discovered_namespaces < <(printf '%s\n' "${discovered_namespaces[@]}" | awk 'NF' | sort -u)

bundle_info "discovered InferNex workload namespaces: ${discovered_namespaces[*]}"
bootstrap_kubeconfig="$(mktemp /tmp/infernex-agent-bootstrap-kubeconfig.XXXXXX)"
cleanup() {
  rm -f -- "$bootstrap_kubeconfig"
}
trap cleanup EXIT

create_args=(
  --admin-kubeconfig "$admin_kubeconfig"
  --output "$bootstrap_kubeconfig"
  --force
  --enable-deployment
  --deployment-namespace "$workspace_namespace"
  --deployment-template-namespace "$template_namespace"
  --enable-log-diagnostics
)
install_args=(
  --bundle-dir "$bundle_root"
  --kubeconfig "$bootstrap_kubeconfig"
  --dashboard-listen-address "$dashboard_listen_address"
  --enable-deployment
  --deployment-namespace "$workspace_namespace"
  --deployment-template-namespace "$template_namespace"
  --enable-log-diagnostics
)
for namespace in "${discovered_namespaces[@]}"; do
  create_args+=(--target-namespace "$namespace")
  install_args+=(--scan-namespace "$namespace")
done

bundle_info "creating the dedicated Agent identity and automatically scoped RBAC"
"$create_kubeconfig" "${create_args[@]}"
bundle_info "installing the static Agent binary and systemd service"
"$install_host" "${install_args[@]}"

if [[ "$skip_model_setup" != "true" && "$non_interactive" != "true" ]]; then
  if [[ -r /dev/tty && -w /dev/tty ]]; then
    printf '\nOnly the Agent model interface remains to be configured.\n' >/dev/tty
    "$configure_model" --interactive --test-tools </dev/tty >/dev/tty
  else
    bundle_warn "no interactive terminal detected; model setup was skipped"
  fi
fi

trap - EXIT
cleanup
bundle_info "InferNex Agent is ready"
bundle_info "start the Agentic terminal with: sudo infernex-agent chat"
bundle_info "dashboard: http://${dashboard_listen_address}/ (use an SSH tunnel when bound to 127.0.0.1)"
