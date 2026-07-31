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
  --openai-timeout DURATION        Model request timeout (default: 60s)
  --enable-deployment              Enable constrained catalog tools
  --deployment-readiness-timeout D Roll back a failed new deployment (default: 10m)
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
openai_timeout=""
enable_deployment="false"
deployment_readiness_timeout="10m"
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
    --openai-timeout)
      [[ $# -ge 2 ]] || bundle_die "--openai-timeout requires a value"
      openai_timeout="$2"
      shift 2
      ;;
    --enable-deployment)
      enable_deployment="true"
      shift
      ;;
    --deployment-readiness-timeout)
      [[ $# -ge 2 ]] || bundle_die "--deployment-readiness-timeout requires a value"
      deployment_readiness_timeout="$2"
      shift 2
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
bundle_require_command awk
bundle_require_command wc
bundle_require_command readlink
bundle_require_command cp
bundle_require_command date
bundle_require_command sha256sum

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

health_url_for_address() {
  local address="$1"
  local port="${address##*:}"
  local host="${address%:*}"
  case "$host" in
    0.0.0.0 | :: | '[::]') host="127.0.0.1" ;;
  esac
  if [[ "$host" == *:* && "$host" != \[*\] ]]; then
    host="[${host}]"
  fi
  printf 'http://%s:%s' "$host" "$port"
}

if [[ -n "$openai_base_url" || -n "$openai_model" ]]; then
  [[ -n "$openai_base_url" && -n "$openai_model" ]] ||
    bundle_die "--openai-base-url and --openai-model must be provided together"
  [[ "$openai_base_url" =~ ^https?://[^[:space:]@]+$ ]] ||
    bundle_die "OpenAI base URL must be http(s), contain no credentials, and contain no spaces"
  [[ "$openai_base_url" != *'?'* && "$openai_base_url" != *'#'* ]] ||
    bundle_die "OpenAI base URL must not contain a query string or fragment"
  [[ "$openai_model" != *[[:space:]]* &&
    "$openai_model" != *$'\r'* &&
    "$openai_model" != *$'\n'* ]] ||
    bundle_die "OpenAI model name must not contain whitespace or control characters"
fi
if [[ -n "$openai_api_key_source" ]]; then
  [[ -n "$openai_base_url" ]] ||
    bundle_die "--openai-api-key-file requires OpenAI endpoint configuration"
  [[ -f "$openai_api_key_source" && -r "$openai_api_key_source" ]] ||
    bundle_die "OpenAI API key file is not readable"
  api_key_size="$(wc -c <"$openai_api_key_source")"
  ((api_key_size > 0 && api_key_size <= 65536)) ||
    bundle_die "OpenAI API key file must contain between 1 and 65536 bytes"
  if LC_ALL=C grep -q $'\r' "$openai_api_key_source" ||
    ! awk 'NR > 1 { exit 1 }' "$openai_api_key_source"; then
    bundle_die "OpenAI API key file must contain exactly one text line"
  fi
fi
if [[ -n "$openai_timeout" ]]; then
  [[ -n "$openai_base_url" ]] ||
    bundle_die "--openai-timeout requires OpenAI endpoint configuration"
  [[ "$openai_timeout" =~ ^[1-9][0-9]*(ms|s|m|h)$ ]] ||
    bundle_die "OpenAI timeout must be a positive duration such as 60s or 2m"
fi
[[ "$recovery_min_scans" =~ ^[0-9]+$ ]] &&
  ((recovery_min_scans >= 2 && recovery_min_scans <= 100)) ||
  bundle_die "recovery critical scans must be between 2 and 100"
[[ "$deployment_readiness_timeout" =~ ^[1-9][0-9]*(s|m|h)$ ]] ||
  bundle_die "deployment readiness timeout must be a positive duration such as 10m"

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
agent_config="${config_root}/agent.conf"
installed_configurator="${install_root}/bin/configure-model.sh"
installed_restorer="${install_root}/bin/restore-host-install.sh"
installed_bundle_lib="${install_root}/bin/bundle-lib.sh"

if ! id "$service_user" >/dev/null 2>&1; then
  bundle_info "creating system user ${service_user}"
  useradd --system --user-group \
    --home-dir "$state_root" --shell /sbin/nologin "$service_user"
fi
service_group="$(id -gn "$service_user")"
install -d -m 0755 -o root -g root "${install_root}/bin"
install -d -m 0750 -o "$service_user" -g "$service_group" "$config_root" "$state_root"

install_backup_root="${state_root}/backups/install-$(
  date -u +%Y%m%dT%H%M%SZ
)-$$"
install -d -m 0700 -o root -g root \
  "${state_root}/backups" "$install_backup_root" "${install_backup_root}/host"
cluster_snapshot="${install_backup_root}/cluster-state.json"
cluster_backup_args=(
  cluster-state backup
  --kubeconfig "$kubeconfig_source"
  --output "$cluster_snapshot"
  --purpose pre-host-install
)
for scan_namespace in "${scan_namespaces[@]}"; do
  cluster_backup_args+=(--namespace "$scan_namespace")
done
bundle_info "capturing the pre-install InferNex cluster state"
"$binary_source" "${cluster_backup_args[@]}"

host_backup_targets=(
  "$installed_binary"
  "${installed_binary}.previous"
  "$runner_path"
  "$installed_kubeconfig"
  "$installed_api_key"
  "$agent_config"
  "$installed_configurator"
  "$installed_restorer"
  "$installed_bundle_lib"
  "$unit_path"
)
host_backup_manifest="${install_backup_root}/host/manifest"
: >"$host_backup_manifest"
chmod 0600 "$host_backup_manifest"
for target_index in "${!host_backup_targets[@]}"; do
  target="${host_backup_targets[$target_index]}"
  if [[ -e "$target" ]]; then
    cp --archive --no-dereference -- "$target" \
      "${install_backup_root}/host/${target_index}"
    printf '%s\tpresent\t%s\n' "$target_index" "$target" >>"$host_backup_manifest"
  else
    printf '%s\tabsent\t%s\n' "$target_index" "$target" >>"$host_backup_manifest"
  fi
done
(
  cd -- "$install_backup_root"
  sha256sum cluster-state.json >cluster-state.sha256
)
service_was_active="false"
service_was_enabled="false"
systemctl is-active --quiet infernex-agent.service && service_was_active="true"
systemctl is-enabled --quiet infernex-agent.service && service_was_enabled="true"
printf 'active=%s\nenabled=%s\n' "$service_was_active" "$service_was_enabled" \
  >"${install_backup_root}/host/service-state"
chmod 0600 "${install_backup_root}/host/service-state"
: >"${install_backup_root}/host/checksums.sha256"
for target_index in "${!host_backup_targets[@]}"; do
  backup="${install_backup_root}/host/${target_index}"
  if [[ -f "$backup" ]]; then
    (
      cd -- "${install_backup_root}/host"
      sha256sum "$target_index"
    ) >>"${install_backup_root}/host/checksums.sha256"
  fi
done
(
  cd -- "${install_backup_root}/host"
  sha256sum manifest service-state
) >>"${install_backup_root}/host/checksums.sha256"
chmod 0600 "${install_backup_root}/host/checksums.sha256"
installation_committed="false"

rollback_failed_install() {
  local exit_status=$?
  [[ "$installation_committed" == "false" ]] || return "$exit_status"
  bundle_warn "installation failed; restoring the previous host installation and cluster baseline"
  systemctl stop infernex-agent.service >/dev/null 2>&1 || true
  for target_index in "${!host_backup_targets[@]}"; do
    target="${host_backup_targets[$target_index]}"
    backup="${install_backup_root}/host/${target_index}"
    if [[ -e "$backup" ]]; then
      cp --archive --no-dereference -- "$backup" "$target" || true
    else
      rm -f -- "$target" || true
    fi
  done
  systemctl daemon-reload >/dev/null 2>&1 || true
  if [[ "$service_was_enabled" == "true" ]]; then
    systemctl enable infernex-agent.service >/dev/null 2>&1 || true
  else
    systemctl disable infernex-agent.service >/dev/null 2>&1 || true
  fi
  if [[ "$service_was_active" == "true" ]]; then
    systemctl start infernex-agent.service >/dev/null 2>&1 || true
  fi
  "$binary_source" cluster-state restore \
    --kubeconfig "$kubeconfig_source" \
    --input "$cluster_snapshot" \
    --confirm >/dev/null 2>&1 ||
    bundle_warn "automatic cluster restore failed; use ${cluster_snapshot} for manual recovery"
  bundle_warn "pre-install backup retained at ${install_backup_root}"
  return "$exit_status"
}
trap rollback_failed_install EXIT

bundle_lib_source="${script_dir}/bundle-lib.sh"
if [[ ! -f "$bundle_lib_source" ]]; then
  bundle_lib_source="${script_dir}/../offline/bundle-lib.sh"
fi
[[ -f "${script_dir}/configure-model.sh" &&
  -f "${script_dir}/restore-host-install.sh" &&
  -f "$bundle_lib_source" ]] ||
  bundle_die "host configuration and restore tools are missing"
install -m 0755 -o root -g root \
  "${script_dir}/configure-model.sh" \
  "$installed_configurator"
install -m 0755 -o root -g root \
  "${script_dir}/restore-host-install.sh" \
  "$installed_restorer"
install -m 0644 -o root -g root \
  "$bundle_lib_source" \
  "$installed_bundle_lib"

if [[ -f "$installed_binary" ]]; then
  install -m 0755 -o root -g root "$installed_binary" "${installed_binary}.previous"
fi
temporary_binary="${installed_binary}.new"
install -m 0755 -o root -g root "$binary_source" "$temporary_binary"
mv -f -- "$temporary_binary" "$installed_binary"
source_kubeconfig_resolved="$(readlink -f -- "$kubeconfig_source")"
installed_kubeconfig_resolved="$(
  readlink -f -- "$installed_kubeconfig" 2>/dev/null || true
)"
if [[ "$source_kubeconfig_resolved" != "$installed_kubeconfig_resolved" ]]; then
  install -m 0600 -o "$service_user" -g "$service_group" \
    "$kubeconfig_source" "$installed_kubeconfig"
else
  chmod 0600 "$installed_kubeconfig"
  chown "$service_user":"$service_group" "$installed_kubeconfig"
fi

preserve_model_config="false"
if [[ -z "$openai_base_url" &&
  -z "$openai_model" &&
  -z "$openai_api_key_source" &&
  -z "$openai_timeout" &&
  -f "$agent_config" ]]; then
  preserve_model_config="true"
fi
if [[ "$preserve_model_config" == "true" ]]; then
  bundle_info "preserving existing model configuration"
elif [[ -n "$openai_api_key_source" ]]; then
  source_api_key_resolved="$(readlink -f -- "$openai_api_key_source")"
  installed_api_key_resolved="$(
    readlink -f -- "$installed_api_key" 2>/dev/null || true
  )"
  if [[ "$source_api_key_resolved" != "$installed_api_key_resolved" ]]; then
    install -m 0600 -o "$service_user" -g "$service_group" \
      "$openai_api_key_source" "$installed_api_key"
  else
    chmod 0600 "$installed_api_key"
    chown "$service_user":"$service_group" "$installed_api_key"
  fi
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
  agent_args+=(
    "--openai-base-url=${openai_base_url}"
    "--openai-model=${openai_model}"
    "--openai-timeout=${openai_timeout:-60s}"
  )
fi
if [[ -n "$openai_api_key_source" ]]; then
  agent_args+=("--openai-api-key-file=${installed_api_key}")
fi
if [[ "$preserve_model_config" == "true" ]]; then
  preserved_base_url="false"
  preserved_model="false"
  preserved_api_key="false"
  while IFS= read -r argument; do
    [[ -n "$argument" && "$argument" == --* ]] ||
      bundle_die "${agent_config} contains an invalid argument"
    case "$argument" in
      --openai-base-url=*)
        preserved_base_url="true"
        agent_args+=("$argument")
        ;;
      --openai-model=*)
        preserved_model="true"
        agent_args+=("$argument")
        ;;
      --openai-api-key-file=*)
        preserved_api_key="true"
        agent_args+=("$argument")
        ;;
      --openai-timeout=*)
        agent_args+=("$argument")
        ;;
    esac
  done <"$agent_config"
  [[ "$preserved_base_url" == "$preserved_model" ]] ||
    bundle_die "${agent_config} contains incomplete model configuration"
  [[ "$preserved_api_key" != "true" || -f "$installed_api_key" ]] ||
    bundle_die "${agent_config} references a missing OpenAI API key"
fi
if [[ "$enable_deployment" == "true" ]]; then
  agent_args+=(
    "--enable-deployment"
    "--state-dir=${state_root}"
    "--deployment-readiness-timeout=${deployment_readiness_timeout}"
  )
fi
if [[ "$enable_recovery" == "true" ]]; then
  agent_args+=(
    "--enable-auto-recovery"
    "--recovery-template-namespace=${recovery_template_namespace}"
    "--recovery-min-critical-scans=${recovery_min_scans}"
  )
fi

temporary_runner="$(mktemp "${install_root}/bin/.run-agent.XXXXXX")"
cat >"$temporary_runner" <<EOF
#!/usr/bin/env bash
set -euo pipefail
mapfile -t agent_args <${agent_config@Q}
((
  \${#agent_args[@]} > 0
)) || {
  echo "InferNex Agent configuration contains no arguments" >&2
  exit 1
}
for argument in "\${agent_args[@]}"; do
  [[ -n "\$argument" && "\$argument" == --* ]] || {
    echo "InferNex Agent configuration contains an invalid argument" >&2
    exit 1
  }
done
exec ${installed_binary@Q} "\${agent_args[@]}"
EOF
chmod 0755 "$temporary_runner"
chown root:root "$temporary_runner"
mv -f -- "$temporary_runner" "$runner_path"

temporary_config="$(mktemp "${config_root}/.agent.conf.XXXXXX")"
printf '%s\n' "${agent_args[@]}" >"$temporary_config"
chmod 0640 "$temporary_config"
chown root:"$service_group" "$temporary_config"
mv -f -- "$temporary_config" "$agent_config"

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
  if ! systemctl enable infernex-agent.service; then
    bundle_die "failed to enable infernex-agent.service"
  fi
  if systemctl is-active --quiet infernex-agent.service; then
    service_action="restart"
  else
    service_action="start"
  fi
  if ! systemctl "$service_action" infernex-agent.service; then
    journalctl -u infernex-agent.service --no-pager -n 100 >&2 || true
    bundle_die "failed to start infernex-agent.service"
  fi
  verify_args=(
    --kubeconfig "$installed_kubeconfig"
    --mcp-url "$(health_url_for_address "$listen_address")"
    --dashboard-url "$(health_url_for_address "$dashboard_listen_address")"
  )
  for scan_namespace in "${scan_namespaces[@]}"; do
    verify_args+=(--target-namespace "$scan_namespace")
  done
  "${script_dir}/verify-host.sh" \
    "${verify_args[@]}"
else
  bundle_info "files installed; run systemctl enable --now infernex-agent.service when ready"
fi

installation_committed="true"
trap - EXIT
bundle_info "host installation completed"
bundle_info "pre-install recovery point: ${install_backup_root}"
bundle_info "dashboard listener: ${dashboard_listen_address}"
bundle_info "MCP listener: ${listen_address}"
if [[ -n "$openai_base_url" || "$preserve_model_config" == "true" ]]; then
  bundle_info "model configuration: ${agent_config}"
else
  bundle_info "model analysis is disabled; configure it later with ${installed_configurator}"
fi
