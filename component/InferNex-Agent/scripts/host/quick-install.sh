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
  --dashboard-listen-address A  Default: 0.0.0.0:8081
  --hardened-identity          Create a dedicated ServiceAccount/RBAC identity
  --generic-kubernetes        Force base Kubernetes/Helm compatibility mode
  --skip-checksums            Skip package-internal checksums after verifying the outer archive
  --skip-model-setup            Install first; configure the model later
  --evidence-root DIR           Allow read-only historical log analysis (repeatable)
  --execution-mode MODE         detect, diagnose (default), modify, install, or recover
  --no-root-collector           Disable the isolated fixed-profile root helper
  --disable-diagnostic-subagent Disable the restricted local diagnostic MCP endpoint
  --diagnostic-ssh-config FILE  OpenSSH config for operator-managed node aliases
  --diagnostic-ssh-target ALIAS Allow fixed probes on one SSH alias (repeatable)
  --non-interactive             Do not read from the terminal
  -h, --help                    Show this help
EOF
}

admin_kubeconfig=""
admin_kubeconfig_explicit="false"
agent_config_path="/etc/infernex-agent/agent.conf"
dashboard_listen_address="0.0.0.0:8081"
dashboard_listen_address_explicit="false"
skip_model_setup="false"
non_interactive="false"
hardened_identity="false"
force_generic_kubernetes="false"
skip_checksums="false"
workspace_namespace="infernex-agent-workspace"
execution_mode=""
pass_execution_mode="false"
diagnostic_ssh_config=""
enable_root_collector="true"
enable_diagnostic_subagent="true"
declare -a evidence_roots=()
declare -a diagnostic_ssh_targets=()

while (($#)); do
  case "$1" in
    --admin-kubeconfig)
      [[ $# -ge 2 ]] || bundle_die "--admin-kubeconfig requires a value"
      admin_kubeconfig="$2"
      [[ -n "$admin_kubeconfig" ]] || bundle_die "--admin-kubeconfig requires a nonempty file path"
      admin_kubeconfig_explicit="true"
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
    --evidence-root)
      [[ $# -ge 2 ]] || bundle_die "--evidence-root requires a value"
      evidence_roots+=("$2")
      shift 2
      ;;
    --execution-mode)
      [[ $# -ge 2 ]] || bundle_die "--execution-mode requires a value"
      execution_mode="$2"
      shift 2
      ;;
    --diagnostic-ssh-config)
      [[ $# -ge 2 ]] || bundle_die "--diagnostic-ssh-config requires a value"
      diagnostic_ssh_config="$2"
      shift 2
      ;;
    --diagnostic-ssh-target)
      [[ $# -ge 2 ]] || bundle_die "--diagnostic-ssh-target requires a value"
      diagnostic_ssh_targets+=("$2")
      shift 2
      ;;
    --no-root-collector)
      enable_root_collector="false"
      shift
      ;;
    --disable-diagnostic-subagent)
      enable_diagnostic_subagent="false"
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
    --skip-checksums)
      skip_checksums="true"
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
bundle_require_command tail

if [[ -n "$execution_mode" ]]; then
  effective_execution_mode="$execution_mode"
  pass_execution_mode="true"
elif [[ -f /etc/infernex-agent/agent.conf ]] &&
  grep -Eq '^--execution-mode=' /etc/infernex-agent/agent.conf; then
  effective_execution_mode="$(grep -E '^--execution-mode=' /etc/infernex-agent/agent.conf | tail -n 1)"
  effective_execution_mode="${effective_execution_mode#*=}"
else
  effective_execution_mode="diagnose"
  pass_execution_mode="true"
fi
case "$effective_execution_mode" in
  detect | diagnose | modify | install | recover) ;;
  *) bundle_die "execution mode must be detect, diagnose, modify, install, or recover" ;;
esac

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
  local address="$1" config="${2:-$agent_config_path}"
  local configured="" argument main_pid listeners row seen="false"
  systemctl is-active --quiet infernex-agent.service 2>/dev/null || return 1
  [[ -f "$config" ]] || return 1
  while IFS= read -r argument; do
    case "$argument" in --dashboard-listen-address=*) configured="${argument#*=}" ;; esac
  done <"$config"
  [[ -n "$configured" && "$(address_port "$configured")" == "$(address_port "$address")" ]] || return 1
  main_pid="$(systemctl show -p MainPID --value infernex-agent.service 2>/dev/null)"
  [[ "$main_pid" =~ ^[1-9][0-9]*$ ]] || return 1
  command -v ss >/dev/null 2>&1 || return 1
  listeners="$(ss -H -ltnp "sport = :$(address_port "$address")" 2>/dev/null)" || return 1
  [[ -n "$listeners" ]] || return 1
  while IFS= read -r row; do
    [[ "$row" == *"pid=${main_pid},"* ]] || return 1
    row="${row//pid=${main_pid},/}"
    [[ "$row" != *'pid='* ]] || return 1
    seen="true"
  done <<<"$listeners"
  [[ "$seen" == "true" ]]
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

select_existing_dashboard_bind() {
  local requested="$1" explicit="$2" config="$3" argument existing=""
  if [[ "$explicit" != "true" && -f "$config" ]]; then
    while IFS= read -r argument; do
      case "$argument" in --dashboard-listen-address=*) existing="${argument#*=}" ;; esac
    done <"$config"
    [[ -z "$existing" ]] || requested="$existing"
  fi
  printf '%s' "$requested"
}

quick_dashboard_access_url() {
  local address="$1" port="${1##*:}" host="${1%:*}"
  case "$host" in
    0.0.0.0) host='<HostIP>' ;;
    :: | '[::]') host='[<HostIPv6>]' ;;
    *) [[ "$host" != *:* || "$host" == \[*\] ]] || host="[${host}]" ;;
  esac
  printf 'http://%s:%s/' "$host" "$port"
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

discovery_kubeconfig=""
runtime_kubeconfig=""
cleanup() {
  [[ -z "$discovery_kubeconfig" ]] || rm -f -- "$discovery_kubeconfig"
  [[ -z "$runtime_kubeconfig" ]] || rm -f -- "$runtime_kubeconfig"
}
trap cleanup EXIT

check_kubeconfig() {
  local candidate="$1" description="$2" context=""
  kubeconfig_failure=""
  kubeconfig_failure_description="$description"
  kubeconfig_failure_path="$candidate"
  if [[ ! -e "$candidate" ]]; then
    kubeconfig_failure="missing"
    return 1
  fi
  if [[ ! -f "$candidate" || ! -r "$candidate" ]]; then
    kubeconfig_failure="unreadable"
    return 1
  fi
  if ! kubectl --kubeconfig "$candidate" config view --raw --minify \
    >/dev/null 2>&1; then
    kubeconfig_failure="invalid"
    return 1
  fi
  if ! context="$(kubectl --kubeconfig "$candidate" config current-context 2>/dev/null)" ||
    [[ -z "$context" ]]; then
    kubeconfig_failure="no-context"
    return 1
  fi
  if ! kubectl --kubeconfig "$candidate" --request-timeout=10s get --raw=/version \
    >/dev/null 2>&1; then
    kubeconfig_failure="offline"
    return 1
  fi
  admin_kubeconfig="$(readlink -f -- "$candidate")"
}

report_kubeconfig_failure() {
  local action="$1"
  case "$kubeconfig_failure" in
    missing) bundle_warn "${action} ${kubeconfig_failure_description}: file does not exist: ${kubeconfig_failure_path}" ;;
    unreadable) bundle_warn "${action} ${kubeconfig_failure_description}: not a readable file: ${kubeconfig_failure_path}" ;;
    invalid) bundle_warn "${action} ${kubeconfig_failure_description}: invalid kubeconfig: ${kubeconfig_failure_path}" ;;
    no-context) bundle_warn "${action} ${kubeconfig_failure_description}: no usable current context: ${kubeconfig_failure_path}" ;;
    offline) bundle_warn "${action} ${kubeconfig_failure_description}: Kubernetes API is unavailable for its current context: ${kubeconfig_failure_path}" ;;
  esac
}

die_for_kubeconfig_failure() {
  case "$kubeconfig_failure" in
    missing) bundle_die "${kubeconfig_failure_description} does not exist: ${kubeconfig_failure_path}" ;;
    unreadable) bundle_die "${kubeconfig_failure_description} is not a readable file: ${kubeconfig_failure_path}" ;;
    invalid) bundle_die "${kubeconfig_failure_description} has an invalid kubeconfig or current context: ${kubeconfig_failure_path}" ;;
    no-context) bundle_die "${kubeconfig_failure_description} has no usable current context: ${kubeconfig_failure_path}" ;;
    offline) bundle_die "${kubeconfig_failure_description} is present but cannot reach the Kubernetes API with its current credentials: ${kubeconfig_failure_path}; check connectivity, context, and authentication with kubectl" ;;
    *) bundle_die "${kubeconfig_failure_description} is not usable: ${kubeconfig_failure_path}" ;;
  esac
}

known_admin_kubeconfigs() {
  local root_home="${1:-/root}"
  printf '%s\n' \
    "${root_home}/.kube/config" \
    /etc/kubernetes/admin.conf \
    /etc/rancher/k3s/k3s.yaml \
    /etc/rancher/rke2/rke2.yaml
}

discover_kubeconfig() {
  local operator_home="${1-${HOME:-}}" root_home="${2:-/root}" home_parent="${3:-/home}"
  local candidate sudo_home="" installed="" argument="" path="" seen="" duplicate="false" any_present="false"
  # Seed the array for Bash 3 with nounset enabled; expanding an empty array is
  # otherwise treated as an unbound variable on older installer hosts.
  local -a candidates=() env_candidates=() seen_paths=("")

  if [[ "$admin_kubeconfig_explicit" == "true" ]]; then
    if check_kubeconfig "$admin_kubeconfig" "--admin-kubeconfig"; then
      return
    fi
    die_for_kubeconfig_failure
  fi

  if [[ -n "${KUBECONFIG:-}" ]]; then
    IFS=: read -r -a env_candidates <<<"$KUBECONFIG"
    # kubectl merges the list according to its own precedence rules. A single
    # --kubeconfig argument would silently discard contexts from later files.
    discovery_kubeconfig="$(umask 077; mktemp /tmp/infernex-agent-admin-kubeconfig.XXXXXX)"
    if KUBECONFIG="$KUBECONFIG" kubectl config view --raw --flatten --minify \
      >"$discovery_kubeconfig" 2>/dev/null &&
      check_kubeconfig "$discovery_kubeconfig" "merged KUBECONFIG"; then
      return
    fi
    bundle_warn "merged KUBECONFIG is unavailable; trying its individual files and host defaults"
    rm -f -- "$discovery_kubeconfig"
    discovery_kubeconfig=""
    for path in "${env_candidates[@]}"; do
      [[ -n "$path" ]] || continue
      duplicate="false"
      for seen in "${seen_paths[@]}"; do
        if [[ "$seen" == "$path" ]]; then duplicate="true"; break; fi
      done
      [[ "$duplicate" == "false" ]] || continue
      seen_paths+=("$path")
      if check_kubeconfig "$path" "KUBECONFIG entry"; then
        return
      fi
      [[ "$kubeconfig_failure" == "missing" ]] || any_present="true"
      report_kubeconfig_failure "skipping"
    done
  fi

  # Preserve the installed Agent's cluster on upgrade unless the operator
  # explicitly supplied --admin-kubeconfig or KUBECONFIG.
  if [[ -f "$agent_config_path" ]]; then
    while IFS= read -r argument; do
      case "$argument" in --kubeconfig=*) installed="${argument#*=}" ;; esac
    done <"$agent_config_path"
    [[ -z "$installed" ]] || candidates+=("$installed")
  fi
  candidates+=("$(dirname -- "$agent_config_path")/kubeconfig")

  # Preserve the alpha.15/16 order under sudo, including its passwd lookup
  # fallback for minimal hosts that do not provide getent.
  if [[ -n "${SUDO_USER:-}" && "$SUDO_USER" != "root" ]]; then
    if command -v getent >/dev/null 2>&1; then
      sudo_home="$(getent passwd "$SUDO_USER" 2>/dev/null | awk -F: 'NR == 1 {print $6}' || true)"
    fi
    [[ -n "$sudo_home" ]] || sudo_home="${home_parent}/${SUDO_USER}"
    candidates+=("${sudo_home}/.kube/config")
  fi
  [[ -z "$operator_home" ]] || candidates+=("${operator_home}/.kube/config")

  while IFS= read -r candidate; do candidates+=("$candidate"); done \
    < <(known_admin_kubeconfigs "$root_home")
  for candidate in "${candidates[@]}"; do
    [[ -n "$candidate" ]] || continue
    duplicate="false"
    for seen in "${seen_paths[@]}"; do
      if [[ "$seen" == "$candidate" ]]; then duplicate="true"; break; fi
    done
    [[ "$duplicate" == "false" ]] || continue
    seen_paths+=("$candidate")
    if check_kubeconfig "$candidate" "discovered kubeconfig"; then
      return
    fi
    [[ "$kubeconfig_failure" == "missing" ]] || any_present="true"
    [[ "$kubeconfig_failure" == "missing" ]] || report_kubeconfig_failure "skipping"
  done
  if [[ "$any_present" == "true" ]]; then
    bundle_die "management kubeconfig files were found, but none had a usable context and Kubernetes API; pass --admin-kubeconfig /path/to/config"
  fi
  bundle_die "no management kubeconfig file was found; pass --admin-kubeconfig /path/to/config (checked KUBECONFIG, existing Agent config, SUDO_USER and HOME, root, and known Kubernetes admin paths)"
}

discover_kubeconfig
bundle_info "using the current Kubernetes context from: ${admin_kubeconfig}"
dashboard_listen_address="$(select_existing_dashboard_bind "$dashboard_listen_address" \
  "$dashboard_listen_address_explicit" "$agent_config_path")"
# An existing bind is an operator choice. If its port has become occupied,
# report the conflict instead of silently selecting another port on upgrade.
if [[ "$dashboard_listen_address_explicit" != "true" && \
  -f "$agent_config_path" ]] &&
  grep -q '^--dashboard-listen-address=' "$agent_config_path"; then
  dashboard_listen_address_explicit="true"
fi
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
  if [[ "$effective_execution_mode" != "detect" ]]; then
    create_args+=(--enable-pod-exec)
  fi
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
if [[ "$pass_execution_mode" == "true" ]]; then
  install_args+=(--execution-mode "$effective_execution_mode")
fi
if [[ "$enable_root_collector" == "true" && "$effective_execution_mode" != "detect" ]]; then
  install_args+=(--enable-root-collector)
fi
if [[ "$enable_diagnostic_subagent" != "true" ]]; then
  install_args+=(--disable-diagnostic-subagent)
fi
for evidence_root in "${evidence_roots[@]}"; do
  install_args+=(--evidence-root "$evidence_root")
done
if [[ -n "$diagnostic_ssh_config" ]]; then
  install_args+=(--diagnostic-ssh-config "$diagnostic_ssh_config")
fi
for diagnostic_ssh_target in "${diagnostic_ssh_targets[@]}"; do
  install_args+=(--diagnostic-ssh-target "$diagnostic_ssh_target")
done
if [[ "$skip_checksums" == "true" ]]; then
  install_args+=(--skip-checksums)
fi
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
bundle_info "start the Agentic TUI with: sudo infernex-agent chat"
bundle_info "legacy line terminal: sudo infernex-agent chat --classic"
bundle_info "dashboard access: $(quick_dashboard_access_url "$dashboard_listen_address") (for wildcard binds, use a reachable management IP)"
