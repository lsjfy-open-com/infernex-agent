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
Install InferNex Agent as a hardened systemd service on Linux/openEuler.

Usage:
  sudo install-host.sh --kubeconfig FILE --scan-namespace NAMESPACE [options]

Options:
  --bundle-dir DIR                 Extracted host bundle root
  --binary FILE                   Agent binary outside a host bundle
  --kubeconfig FILE               Dedicated, self-contained kubeconfig
  --scan-namespace NAMESPACE      Namespace to scan (repeatable)
  --listen-address ADDRESS        MCP bind (default: 127.0.0.1:8080)
  --dashboard-listen-address ADDR Dashboard bind (default: 127.0.0.1:8081)
  --openai-base-url URL            Internal OpenAI-compatible /v1 endpoint
  --openai-model MODEL             Diagnostic model name
  --openai-api-key-file FILE       API key copied as a protected credential
  --enable-deployment              Enable constrained catalog tools
  --enable-recovery                Enable guarded recovery
  --recovery-template-namespace N  Profile namespace
  --recovery-min-critical-scans N  Default: 3
  --skip-checksums                  Skip host bundle checksum verification
  --no-start                        Install files without starting the service
  -h, --help                        Show this help

Use 0.0.0.0:8081 or a specific management IP only with a host firewall rule
that limits access to the internal operations network. MCP is local-only by
default and should normally remain so.
EOF
}

bundle_root="$(bundle_default_root || true)"
binary_source=""
kubeconfig_source=""
listen_address="127.0.0.1:8080"
dashboard_listen_address="127.0.0.1:8081"
openai_base_url=""
openai_model=""
openai_api_key_source=""
enable_deployment="false"
enable_recovery="false"
recovery_template_namespace="infernex-bridge-system"
recovery_min_scans="3"
verify_checksums="true"
start_service="true"
declare -a scan_namespaces=()

while (($#)); do
  case "$1" in
    --bundle-dir)
      [[ $# -ge 2 ]] || bundle_die "--bundle-dir requires a value"
      bundle_root="$2"
      shift 2
      ;;
    --binary)
      [[ $# -ge 2 ]] || bundle_die "--binary requires a value"
      binary_source="$2"
      shift 2
      ;;
    --kubeconfig)
      [[ $# -ge 2 ]] || bundle_die "--kubeconfig requires a value"
      kubeconfig_source="$2"
      shift 2
      ;;
    --scan-namespace)
      [[ $# -ge 2 ]] || bundle_die "--scan-namespace requires a value"
      scan_namespaces+=("$2")
      shift 2
      ;;
    --listen-address)
      [[ $# -ge 2 ]] || bundle_die "--listen-address requires a value"
      listen_address="$2"
      shift 2
      ;;
    --dashboard-listen-address)
      [[ $# -ge 2 ]] || bundle_die "--dashboard-listen-address requires a value"
      dashboard_listen_address="$2"
      shift 2
      ;;
    --openai-base-url)
      [[ $# -ge 2 ]] || bundle_die "--openai-base-url requires a value"
      openai_base_url="$2"
      shift 2
      ;;
    --openai-model)
      [[ $# -ge 2 ]] || bundle_die "--openai-model requires a value"
      openai_model="$2"
      shift 2
      ;;
    --openai-api-key-file)
      [[ $# -ge 2 ]] || bundle_die "--openai-api-key-file requires a value"
      openai_api_key_source="$2"
      shift 2
      ;;
    --enable-deployment)
      enable_deployment="true"
      shift
      ;;
    --enable-recovery)
      enable_recovery="true"
      shift
      ;;
    --recovery-template-namespace)
      [[ $# -ge 2 ]] || bundle_die "--recovery-template-namespace requires a value"
      recovery_template_namespace="$2"
      shift 2
      ;;
    --recovery-min-critical-scans)
      [[ $# -ge 2 ]] || bundle_die "--recovery-min-critical-scans requires a value"
      recovery_min_scans="$2"
      shift 2
      ;;
    --skip-checksums)
      verify_checksums="false"
      shift
      ;;
    --no-start)
      start_service="false"
      shift
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

[[ ${EUID} -eq 0 ]] ||
  bundle_die "install-host.sh must run as root"
bundle_require_command install
bundle_require_command systemctl
bundle_require_command kubectl
bundle_require_command curl
bundle_require_command useradd

if [[ -n "$bundle_root" ]]; then
  bundle_root="$(cd -- "$bundle_root" && pwd)"
  if [[ "$verify_checksums" == "true" ]]; then
    bundle_verify_checksums "$bundle_root"
  fi
  [[ "$(bundle_property "$bundle_root" format)" == "infernex-agent-host-offline-v1" ]] ||
    bundle_die "unsupported host bundle format"
  bundle_architecture="$(bundle_property "$bundle_root" architecture)"
  [[ "$bundle_architecture" == "$(bundle_host_architecture)" ]] ||
    bundle_die "host bundle architecture ${bundle_architecture} does not match this host"
  if [[ -z "$binary_source" ]]; then
    binary_relative="$(bundle_property "$bundle_root" binary)"
    bundle_safe_relative_path "$binary_relative" ||
      bundle_die "unsafe binary path in bundle"
    binary_source="${bundle_root}/${binary_relative}"
  fi
fi

[[ -n "$binary_source" && -f "$binary_source" ]] ||
  bundle_die "an Agent --binary or extracted host bundle is required"
[[ -x "$binary_source" ]] ||
  bundle_die "Agent binary is not executable: ${binary_source}"
[[ -n "$kubeconfig_source" && -r "$kubeconfig_source" ]] ||
  bundle_die "--kubeconfig must name a readable file"
((${#scan_namespaces[@]} > 0)) ||
  bundle_die "at least one --scan-namespace is required"

validate_dns_label() {
  local label="$1"
  [[ ${#label} -le 63 && "$label" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]]
}
for scan_namespace in "${scan_namespaces[@]}"; do
  validate_dns_label "$scan_namespace" ||
    bundle_die "invalid scan namespace: ${scan_namespace}"
done
validate_dns_label "$recovery_template_namespace" ||
  bundle_die "invalid recovery template namespace: ${recovery_template_namespace}"

validate_listen_address() {
  local address="$1"
  local port="${address##*:}"
  [[ "$address" != *[[:space:]]* && "$address" == *:* ]] &&
    [[ "$port" =~ ^[0-9]+$ ]] &&
    ((port >= 1024 && port <= 65535))
}
validate_listen_address "$listen_address" ||
  bundle_die "invalid or privileged MCP listen address: ${listen_address}"
validate_listen_address "$dashboard_listen_address" ||
  bundle_die "invalid or privileged dashboard listen address: ${dashboard_listen_address}"
[[ "$listen_address" != "$dashboard_listen_address" ]] ||
  bundle_die "MCP and dashboard listen addresses must differ"

if [[ -n "$openai_base_url" || -n "$openai_model" ]]; then
  [[ -n "$openai_base_url" && -n "$openai_model" ]] ||
    bundle_die "--openai-base-url and --openai-model must be provided together"
  [[ "$openai_base_url" =~ ^https?://[^[:space:]@]+$ ]] ||
    bundle_die "OpenAI base URL must be http(s), contain no credentials, and contain no spaces"
fi
if [[ -n "$openai_api_key_source" ]]; then
  [[ -n "$openai_base_url" ]] ||
    bundle_die "--openai-api-key-file requires OpenAI endpoint configuration"
  [[ -f "$openai_api_key_source" && -r "$openai_api_key_source" ]] ||
    bundle_die "OpenAI API key file is not readable"
fi
[[ "$recovery_min_scans" =~ ^[0-9]+$ ]] &&
  ((recovery_min_scans >= 2 && recovery_min_scans <= 100)) ||
  bundle_die "recovery critical scans must be between 2 and 100"

if grep -Eq '^[[:space:]]+(certificate-authority|client-certificate|client-key|tokenFile|exec|auth-provider):' \
  "$kubeconfig_source"; then
  bundle_die "kubeconfig must be self-contained; flatten it or use create-kubeconfig.sh"
fi
if grep -Eq '^[[:space:]]+insecure-skip-tls-verify:[[:space:]]*true' \
  "$kubeconfig_source"; then
  bundle_die "kubeconfig must verify the Kubernetes API server certificate"
fi
for scan_namespace in "${scan_namespaces[@]}"; do
  kubectl --kubeconfig "$kubeconfig_source" \
    get infernexservices.infernex.infernex.io \
    --namespace "$scan_namespace" --request-timeout=10s >/dev/null ||
    bundle_die "kubeconfig cannot reach InferNexService API in ${scan_namespace}"
  [[ "$(
    kubectl --kubeconfig "$kubeconfig_source" auth can-i \
      list infernexservices.infernex.infernex.io --namespace "$scan_namespace"
  )" == "yes" ]] ||
    bundle_die "kubeconfig cannot list InferNexService in ${scan_namespace}"
  if [[ "$enable_deployment" == "true" ]]; then
    for verb in create delete; do
      [[ "$(
        kubectl --kubeconfig "$kubeconfig_source" auth can-i \
          "$verb" infernexservices.infernex.infernex.io --namespace "$scan_namespace"
      )" == "yes" ]] ||
        bundle_die "kubeconfig cannot ${verb} InferNexService in ${scan_namespace}"
    done
  elif [[ "$enable_recovery" == "true" ]]; then
    [[ "$(
      kubectl --kubeconfig "$kubeconfig_source" auth can-i \
        create infernexservices.infernex.infernex.io --namespace "$scan_namespace"
    )" == "yes" ]] ||
      bundle_die "kubeconfig cannot create recovery InferNexService in ${scan_namespace}"
  fi
done
if [[ "$enable_recovery" == "true" ]]; then
  [[ "$(
    kubectl --kubeconfig "$kubeconfig_source" auth can-i \
      get infernexserviceconfigs.infernex.infernex.io \
      --namespace "$recovery_template_namespace"
  )" == "yes" ]] ||
    bundle_die "kubeconfig cannot get recovery profiles in ${recovery_template_namespace}"
fi

service_user="infernex-agent"
install_root="/opt/infernex-agent"
config_root="/etc/infernex-agent"
state_root="/var/lib/infernex-agent"
unit_path="/etc/systemd/system/infernex-agent.service"
installed_binary="${install_root}/bin/infernex-agent"
runner_path="${install_root}/bin/run-agent.sh"
installed_kubeconfig="${config_root}/kubeconfig"
installed_api_key="${config_root}/openai-api-key"

if ! id "$service_user" >/dev/null 2>&1; then
  bundle_info "creating system user ${service_user}"
  useradd --system --user-group \
    --home-dir "$state_root" --shell /sbin/nologin "$service_user"
fi
service_group="$(id -gn "$service_user")"
install -d -m 0755 -o root -g root "${install_root}/bin"
install -d -m 0750 -o "$service_user" -g "$service_group" "$config_root" "$state_root"

if [[ -f "$installed_binary" ]]; then
  install -m 0755 -o root -g root "$installed_binary" "${installed_binary}.previous"
fi
temporary_binary="${installed_binary}.new"
install -m 0755 -o root -g root "$binary_source" "$temporary_binary"
mv -f -- "$temporary_binary" "$installed_binary"
install -m 0600 -o "$service_user" -g "$service_group" \
  "$kubeconfig_source" "$installed_kubeconfig"
if [[ -n "$openai_api_key_source" ]]; then
  install -m 0600 -o "$service_user" -g "$service_group" \
    "$openai_api_key_source" "$installed_api_key"
else
  rm -f -- "$installed_api_key"
fi

scan_namespaces_csv="$(IFS=,; printf '%s' "${scan_namespaces[*]}")"
agent_args=(
  "--transport=streamable-http"
  "--listen-address=${listen_address}"
  "--dashboard-listen-address=${dashboard_listen_address}"
  "--kubeconfig=${installed_kubeconfig}"
  "--scan-namespaces=${scan_namespaces_csv}"
)
if [[ -n "$openai_base_url" ]]; then
  agent_args+=("--openai-base-url=${openai_base_url}" "--openai-model=${openai_model}")
fi
if [[ -n "$openai_api_key_source" ]]; then
  agent_args+=("--openai-api-key-file=${installed_api_key}")
fi
if [[ "$enable_deployment" == "true" ]]; then
  agent_args+=("--enable-deployment")
fi
if [[ "$enable_recovery" == "true" ]]; then
  agent_args+=(
    "--enable-auto-recovery"
    "--recovery-template-namespace=${recovery_template_namespace}"
    "--recovery-min-critical-scans=${recovery_min_scans}"
  )
fi

temporary_runner="$(mktemp "${install_root}/bin/.run-agent.XXXXXX")"
{
  printf '#!/usr/bin/env bash\nset -euo pipefail\nexec %q' "$installed_binary"
  for argument in "${agent_args[@]}"; do
    printf ' \\\n  %q' "$argument"
  done
  printf '\n'
} >"$temporary_runner"
chmod 0755 "$temporary_runner"
chown root:root "$temporary_runner"
mv -f -- "$temporary_runner" "$runner_path"

temporary_unit="$(mktemp /etc/systemd/system/.infernex-agent.service.XXXXXX)"
cat >"$temporary_unit" <<EOF
[Unit]
Description=InferNex management-plane Agent
Documentation=https://github.com/lsjfy-open-com/infernex-agent
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=${service_user}
Group=${service_group}
ExecStart=${runner_path}
Restart=on-failure
RestartSec=5s
TimeoutStopSec=15s
WorkingDirectory=${state_root}
UMask=0077
NoNewPrivileges=true
PrivateDevices=true
PrivateTmp=true
ProtectClock=true
ProtectControlGroups=true
ProtectHome=true
ProtectHostname=true
ProtectKernelLogs=true
ProtectKernelModules=true
ProtectKernelTunables=true
ProtectSystem=strict
ReadOnlyPaths=${config_root}
ReadWritePaths=${state_root}
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
RestrictRealtime=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
CapabilityBoundingSet=
AmbientCapabilities=
SystemCallArchitectures=native

[Install]
WantedBy=multi-user.target
EOF
chmod 0644 "$temporary_unit"
chown root:root "$temporary_unit"
mv -f -- "$temporary_unit" "$unit_path"

if command -v restorecon >/dev/null 2>&1; then
  restorecon -RF "$install_root" "$config_root" "$state_root" "$unit_path" || true
fi
systemctl daemon-reload

if [[ "$start_service" == "true" ]]; then
  bundle_info "enabling and starting infernex-agent.service"
  if ! systemctl enable --now infernex-agent.service; then
    journalctl -u infernex-agent.service --no-pager -n 100 >&2 || true
    bundle_die "failed to start infernex-agent.service"
  fi
  mcp_port="${listen_address##*:}"
  dashboard_port="${dashboard_listen_address##*:}"
  verify_args=(
    --kubeconfig "$installed_kubeconfig"
    --mcp-url "http://127.0.0.1:${mcp_port}"
    --dashboard-url "http://127.0.0.1:${dashboard_port}"
  )
  for scan_namespace in "${scan_namespaces[@]}"; do
    verify_args+=(--target-namespace "$scan_namespace")
  done
  "${script_dir}/verify-host.sh" \
    "${verify_args[@]}"
else
  bundle_info "files installed; run systemctl enable --now infernex-agent.service when ready"
fi

bundle_info "host installation completed"
bundle_info "dashboard listener: ${dashboard_listen_address}"
bundle_info "MCP listener: ${listen_address}"
