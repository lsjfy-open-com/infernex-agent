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
  sudo install-host.sh --kubeconfig FILE [--scan-namespace NAMESPACE] [options]

Options:
  --bundle-dir DIR                 Extracted Agent package root
  --binary FILE                   Agent binary outside a package
  --kubeconfig FILE               Dedicated, self-contained kubeconfig
  --scan-namespace NAMESPACE      Namespace to scan (repeatable)
  --generic-kubernetes            Install without InferNex Bridge CRDs
  --listen-address ADDRESS        MCP bind (default: 127.0.0.1:8080)
  --dashboard-listen-address ADDR Dashboard bind (default: 127.0.0.1:8081)
  --openai-base-url URL            Internal OpenAI-compatible /v1 endpoint
  --openai-model MODEL             Diagnostic model name
  --openai-api-key-file FILE       API key copied as a protected credential
  --openai-timeout DURATION        Per-attempt model timeout (default: 3m)
  --context-window-tokens N        Model context window (default: 32768)
  --max-output-tokens N            Output reservation and per-call maximum
  --reasoning-display MODE         TUI reasoning blocks: hidden (default) or visible
  --execution-mode MODE            detect, diagnose, modify, install, or recover
  --diagnostic-ssh-config FILE     OpenSSH config readable by the service user
  --diagnostic-ssh-target ALIAS    Allowed SSH alias for fixed probes (repeatable)
  --context-compaction-threshold P Compact at this usage percent (default: 80)
  --context-keep-recent-turns N    Recent user turns kept verbatim (default: 4)
  --tool-result-max-tokens N       Approximate cap for one tool result
  --evidence-root DIR              Allow read-only historical log analysis (repeatable)
  --report-directory DIR           Protected Markdown output directory
  --enable-log-diagnostics         Read bounded InferNex-owned Pod logs
  --max-diagnostics-per-scan N     Degraded services read per scan (default: 10)
  --enable-experiments             Run durable, single-feature experiments
  --experiment-template-namespace N Approved feature profile namespace
  --experiment-readiness-timeout D Default: 20m
  --experiment-soak-duration D     Default: 5m
  --experiment-diagnostic-interval D Default: 30s
  --enable-deployment              Enable constrained catalog tools
  --deployment-namespace N         Fixed Agent workspace namespace
  --deployment-template-namespace N Existing InferNexServiceConfig namespace
  --deployment-readiness-timeout D Roll back a failed new deployment (default: 10m)
  --enable-recovery                Enable guarded recovery
  --recovery-template-namespace N  Profile namespace
  --recovery-min-critical-scans N  Default: 3
  --skip-checksums                  Skip Agent package checksum verification
  --interactive-model-setup        Configure and test the model before first start
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
context_window_tokens="32768"
max_output_tokens=""
context_compaction_threshold="80"
context_keep_recent_turns="4"
tool_result_max_tokens=""
reasoning_display="hidden"
execution_mode="detect"
execution_mode_set="false"
diagnostic_ssh_config=""
diagnostic_ssh_config_set="false"
declare -a diagnostic_ssh_targets=()
diagnostic_ssh_targets_set="false"
context_window_set="false"
max_output_set="false"
context_threshold_set="false"
keep_recent_set="false"
tool_result_max_set="false"
reasoning_display_set="false"
report_directory=""
report_directory_set="false"
evidence_roots_set="false"
declare -a evidence_roots=()
enable_log_diagnostics="false"
max_diagnostics_per_scan="10"
enable_experiments="false"
experiment_template_namespace="infernex-bridge-system"
experiment_readiness_timeout="20m"
experiment_soak_duration="5m"
experiment_diagnostic_interval="30s"
enable_deployment="false"
deployment_namespace="infernex-agent-workspace"
deployment_template_namespace="infernex-bridge-system"
deployment_readiness_timeout="10m"
enable_recovery="false"
recovery_template_namespace="infernex-bridge-system"
recovery_min_scans="3"
verify_checksums="true"
start_service="true"
generic_kubernetes="false"
interactive_model_setup="false"
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
    --generic-kubernetes)
      generic_kubernetes="true"
      shift
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
    --context-window-tokens)
      [[ $# -ge 2 ]] || bundle_die "--context-window-tokens requires a value"
      context_window_tokens="$2"
      context_window_set="true"
      shift 2
      ;;
    --max-output-tokens)
      [[ $# -ge 2 ]] || bundle_die "--max-output-tokens requires a value"
      max_output_tokens="$2"
      max_output_set="true"
      shift 2
      ;;
    --reasoning-display)
      [[ $# -ge 2 ]] || bundle_die "--reasoning-display requires a value"
      reasoning_display="$2"
      reasoning_display_set="true"
      shift 2
      ;;
    --execution-mode)
      [[ $# -ge 2 ]] || bundle_die "--execution-mode requires a value"
      execution_mode="$2"
      execution_mode_set="true"
      shift 2
      ;;
    --diagnostic-ssh-config)
      [[ $# -ge 2 ]] || bundle_die "--diagnostic-ssh-config requires a value"
      diagnostic_ssh_config="$2"
      diagnostic_ssh_config_set="true"
      shift 2
      ;;
    --diagnostic-ssh-target)
      [[ $# -ge 2 ]] || bundle_die "--diagnostic-ssh-target requires a value"
      diagnostic_ssh_targets+=("$2")
      diagnostic_ssh_targets_set="true"
      shift 2
      ;;
    --context-compaction-threshold)
      [[ $# -ge 2 ]] || bundle_die "--context-compaction-threshold requires a value"
      context_compaction_threshold="$2"
      context_threshold_set="true"
      shift 2
      ;;
    --context-keep-recent-turns)
      [[ $# -ge 2 ]] || bundle_die "--context-keep-recent-turns requires a value"
      context_keep_recent_turns="$2"
      keep_recent_set="true"
      shift 2
      ;;
    --tool-result-max-tokens)
      [[ $# -ge 2 ]] || bundle_die "--tool-result-max-tokens requires a value"
      tool_result_max_tokens="$2"
      tool_result_max_set="true"
      shift 2
      ;;
    --evidence-root)
      [[ $# -ge 2 ]] || bundle_die "--evidence-root requires a value"
      evidence_roots+=("$2")
      evidence_roots_set="true"
      shift 2
      ;;
    --report-directory)
      [[ $# -ge 2 ]] || bundle_die "--report-directory requires a value"
      report_directory="$2"
      report_directory_set="true"
      shift 2
      ;;
    --enable-log-diagnostics)
      enable_log_diagnostics="true"
      shift
      ;;
    --max-diagnostics-per-scan)
      [[ $# -ge 2 ]] || bundle_die "--max-diagnostics-per-scan requires a value"
      max_diagnostics_per_scan="$2"
      shift 2
      ;;
    --enable-experiments)
      enable_experiments="true"
      enable_log_diagnostics="true"
      shift
      ;;
    --experiment-template-namespace)
      [[ $# -ge 2 ]] || bundle_die "--experiment-template-namespace requires a value"
      experiment_template_namespace="$2"
      shift 2
      ;;
    --experiment-readiness-timeout)
      [[ $# -ge 2 ]] || bundle_die "--experiment-readiness-timeout requires a value"
      experiment_readiness_timeout="$2"
      shift 2
      ;;
    --experiment-soak-duration)
      [[ $# -ge 2 ]] || bundle_die "--experiment-soak-duration requires a value"
      experiment_soak_duration="$2"
      shift 2
      ;;
    --experiment-diagnostic-interval)
      [[ $# -ge 2 ]] || bundle_die "--experiment-diagnostic-interval requires a value"
      experiment_diagnostic_interval="$2"
      shift 2
      ;;
    --enable-deployment)
      enable_deployment="true"
      shift
      ;;
    --deployment-namespace)
      [[ $# -ge 2 ]] || bundle_die "--deployment-namespace requires a value"
      deployment_namespace="$2"
      shift 2
      ;;
    --deployment-template-namespace)
      [[ $# -ge 2 ]] || bundle_die "--deployment-template-namespace requires a value"
      deployment_template_namespace="$2"
      shift 2
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
    --interactive-model-setup)
      interactive_model_setup="true"
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
bundle_require_command runuser
bundle_require_command cp
bundle_require_command date
bundle_require_command sha256sum
if [[ "$interactive_model_setup" == "true" ]]; then
  [[ -r /dev/tty && -w /dev/tty ]] || bundle_die \
    "--interactive-model-setup requires an interactive terminal"
fi

if [[ -n "$bundle_root" ]]; then
  bundle_root="$(cd -- "$bundle_root" && pwd)"
  if [[ "$verify_checksums" == "true" ]]; then
    bundle_verify_checksums "$bundle_root"
  fi
  bundle_format="$(bundle_property "$bundle_root" format)"
  [[ "$bundle_format" == "infernex-agent-linux-v1" ||
    "$bundle_format" == "infernex-agent-host-offline-v1" ]] ||
    bundle_die "unsupported Agent bundle format"
  bundle_architecture="$(bundle_property "$bundle_root" architecture)"
  [[ "$bundle_architecture" == "$(bundle_host_architecture)" ]] ||
    bundle_die "Agent package architecture ${bundle_architecture} does not match this host"
  if [[ -z "$binary_source" ]]; then
    binary_relative="$(bundle_property "$bundle_root" binary)"
    bundle_safe_relative_path "$binary_relative" ||
      bundle_die "unsafe binary path in bundle"
    binary_source="${bundle_root}/${binary_relative}"
  fi
fi

[[ -n "$binary_source" && -f "$binary_source" ]] ||
  bundle_die "an Agent --binary or extracted Agent package is required"
[[ -x "$binary_source" ]] ||
  bundle_die "Agent binary is not executable: ${binary_source}"
[[ -n "$kubeconfig_source" && -r "$kubeconfig_source" ]] ||
  bundle_die "--kubeconfig must name a readable file"
if [[ "$generic_kubernetes" != "true" ]]; then
  ((${#scan_namespaces[@]} > 0)) ||
    bundle_die "at least one --scan-namespace is required outside generic Kubernetes mode"
else
  [[ "$enable_deployment" == "false" && "$enable_recovery" == "false" &&
    "$enable_log_diagnostics" == "false" && "$enable_experiments" == "false" ]] ||
    bundle_die "generic Kubernetes mode cannot enable Bridge-specific deployment, recovery, diagnostics, or experiments"
  ((${#scan_namespaces[@]} == 0)) ||
    bundle_die "generic Kubernetes mode does not yet accept --scan-namespace"
fi

validate_dns_label() {
  local label="$1"
  [[ ${#label} -le 63 && "$label" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]]
}
for scan_namespace in "${scan_namespaces[@]}"; do
  validate_dns_label "$scan_namespace" ||
    bundle_die "invalid scan namespace: ${scan_namespace}"
done
validate_dns_label "$deployment_namespace" ||
  bundle_die "invalid deployment namespace: ${deployment_namespace}"
validate_dns_label "$deployment_template_namespace" ||
  bundle_die "invalid deployment template namespace: ${deployment_template_namespace}"
validate_dns_label "$recovery_template_namespace" ||
  bundle_die "invalid recovery template namespace: ${recovery_template_namespace}"
validate_dns_label "$experiment_template_namespace" ||
  bundle_die "invalid experiment template namespace: ${experiment_template_namespace}"
for duration in "$experiment_readiness_timeout" "$experiment_soak_duration" "$experiment_diagnostic_interval"; do
  [[ "$duration" =~ ^[1-9][0-9]*(s|m|h)$ ]] ||
    bundle_die "experiment durations must be positive values such as 10m"
done
[[ "$max_diagnostics_per_scan" =~ ^[0-9]+$ ]] &&
  ((max_diagnostics_per_scan >= 1 && max_diagnostics_per_scan <= 1000)) ||
  bundle_die "max diagnostics per scan must be between 1 and 1000"

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
  if [[ "$enable_experiments" == "true" ||
    ( "$enable_deployment" == "true" && "$scan_namespace" == "$deployment_namespace" ) ]]; then
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
  if [[ "$enable_log_diagnostics" == "true" ]]; then
    [[ "$(
      kubectl --kubeconfig "$kubeconfig_source" auth can-i \
        get pods --subresource=log --namespace "$scan_namespace"
    )" == "yes" ]] ||
      bundle_die "kubeconfig cannot read Pod logs in ${scan_namespace}"
  fi
done
if [[ "$enable_deployment" == "true" ]]; then
  for verb in get list; do
    [[ "$(
      kubectl --kubeconfig "$kubeconfig_source" auth can-i \
        "$verb" infernexserviceconfigs.infernex.infernex.io \
        --namespace "$deployment_template_namespace"
    )" == "yes" ]] ||
      bundle_die "kubeconfig cannot ${verb} deployment profiles in ${deployment_template_namespace}"
  done
fi
if [[ "$enable_recovery" == "true" ]]; then
  [[ "$(
    kubectl --kubeconfig "$kubeconfig_source" auth can-i \
      get infernexserviceconfigs.infernex.infernex.io \
      --namespace "$recovery_template_namespace"
  )" == "yes" ]] ||
    bundle_die "kubeconfig cannot get recovery profiles in ${recovery_template_namespace}"
fi
if [[ "$enable_experiments" == "true" ]]; then
  [[ "$(
    kubectl --kubeconfig "$kubeconfig_source" auth can-i \
      get infernexserviceconfigs.infernex.infernex.io \
      --namespace "$experiment_template_namespace"
  )" == "yes" ]] ||
    bundle_die "kubeconfig cannot get experiment profiles in ${experiment_template_namespace}"
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
installed_evidence_configurator="${install_root}/bin/configure-evidence.sh"
installed_skills_configurator="${install_root}/bin/configure-skills.sh"
installed_builtin_skills="${install_root}/skills"
installed_restorer="${install_root}/bin/restore-host-install.sh"
installed_bundle_lib="${install_root}/bin/bundle-lib.sh"
installed_chat="${install_root}/bin/chat.sh"
installed_tui="${install_root}/bin/tui.sh"
installed_pi_runtime="${install_root}/pi-runtime"
installed_pi="${installed_pi_runtime}/pi"
installed_pi_extension="${install_root}/pi/infernex.ts"
installed_pi_license="${install_root}/pi/LICENSE.pi.txt"
installed_cli="/usr/local/bin/infernex-agent"
installed_version=""
if [[ -x "$installed_binary" ]]; then
  installed_version="$(
    "$installed_binary" version 2>/dev/null | awk 'NR == 1 { print $3 }' || true
  )"
fi
[[ ! -e "$installed_pi_runtime" || ( -d "$installed_pi_runtime" && ! -L "$installed_pi_runtime" ) ]] ||
  bundle_die "refusing unsafe Pi runtime path: ${installed_pi_runtime}"

if ! id "$service_user" >/dev/null 2>&1; then
  bundle_info "creating system user ${service_user}"
  useradd --system --user-group \
    --home-dir "$state_root" --shell /sbin/nologin "$service_user"
fi
service_group="$(id -gn "$service_user")"
install -d -m 0755 -o root -g root "${install_root}/bin"
install -d -m 0750 -o "$service_user" -g "$service_group" "$config_root" "$state_root" "${state_root}/imports" "${state_root}/reports"
install -d -m 0755 -o root -g root "${config_root}/skills.d"

install_backup_root="${state_root}/backups/install-$(
  date -u +%Y%m%dT%H%M%SZ
)-$$"
install -d -m 0700 -o root -g root \
  "${state_root}/backups" "$install_backup_root" "${install_backup_root}/host"
cluster_snapshot="${install_backup_root}/cluster-state.json"
cluster_backup_available="false"
if [[ "$generic_kubernetes" == "true" ]]; then
  bundle_info "recording a no-mutation compatibility-mode installation baseline"
  cat >"$cluster_snapshot" <<'EOF'
{
  "apiVersion": "agent.infernex.io/v1alpha1",
  "kind": "InstallationBaseline",
  "platformMode": "generic-kubernetes",
  "clusterMutations": false,
  "note": "The Agent installation changed no Kubernetes resources. Host files are backed up separately."
}
EOF
  chmod 0600 "$cluster_snapshot"
else
  cluster_backup_args=(
    cluster-state backup
    --kubeconfig "$kubeconfig_source"
    --output "$cluster_snapshot"
    --purpose pre-host-install
  )
  for scan_namespace in "${scan_namespaces[@]}"; do
    cluster_backup_args+=(--namespace "$scan_namespace")
  done
  bundle_info "capturing the pre-install InferNexService cluster state"
  "$binary_source" "${cluster_backup_args[@]}"
  cluster_backup_available="true"
fi

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
  "$installed_chat"
  "$installed_cli"
  "$installed_tui"
  "$installed_pi_runtime"
  "$installed_pi_extension"
  "$installed_pi_license"
  "$installed_evidence_configurator"
  "$installed_skills_configurator"
  "$installed_builtin_skills"
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
  elif [[ -d "$backup" && ! -L "$backup" ]]; then
    (
      cd -- "${install_backup_root}/host"
      while IFS= read -r -d '' backup_file; do
        sha256sum "$backup_file"
      done < <(find "$target_index" -type f -print0 | LC_ALL=C sort -z)
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
      if [[ -L "$target" ]]; then
        rm -f -- "$target"
      elif [[ -d "$target" ]]; then
        rm -rf -- "$target"
      fi
      cp --archive --no-dereference -- "$backup" "$target" || true
    else
      if [[ -d "$target" && ! -L "$target" ]]; then
        rm -rf -- "$target" || true
      else
        rm -f -- "$target" || true
      fi
    fi
  done
  systemctl daemon-reload >/dev/null 2>&1 || true
  if [[ "$service_was_enabled" == "true" ]]; then
    systemctl enable infernex-agent.service >/dev/null 2>&1 || true
  else
    systemctl disable infernex-agent.service >/dev/null 2>&1 || true
  fi
  if [[ "$service_was_active" == "true" ]]; then
    systemctl reset-failed infernex-agent.service >/dev/null 2>&1 || true
    systemctl start infernex-agent.service >/dev/null 2>&1 || true
  fi
  if [[ "$cluster_backup_available" == "true" ]]; then
    "$binary_source" cluster-state restore \
      --kubeconfig "$kubeconfig_source" \
      --input "$cluster_snapshot" \
      --confirm >/dev/null 2>&1 ||
      bundle_warn "automatic cluster restore failed; use ${cluster_snapshot} for manual recovery"
  fi
  bundle_warn "pre-install backup retained at ${install_backup_root}"
  return "$exit_status"
}
trap rollback_failed_install EXIT

bundle_lib_source="${script_dir}/bundle-lib.sh"
if [[ ! -f "$bundle_lib_source" ]]; then
  bundle_lib_source="${script_dir}/../offline/bundle-lib.sh"
fi
[[ -f "${script_dir}/configure-model.sh" &&
  -f "${script_dir}/configure-evidence.sh" &&
  -f "${script_dir}/configure-skills.sh" &&
  -f "${script_dir}/chat.sh" &&
  -f "${script_dir}/tui.sh" &&
  -f "${script_dir}/restore-host-install.sh" &&
  -f "$bundle_lib_source" ]] ||
  bundle_die "host configuration and restore tools are missing"
install -m 0755 -o root -g root \
  "${script_dir}/configure-model.sh" \
  "$installed_configurator"
install -m 0755 -o root -g root \
  "${script_dir}/configure-evidence.sh" \
  "$installed_evidence_configurator"
install -m 0755 -o root -g root \
  "${script_dir}/configure-skills.sh" \
  "$installed_skills_configurator"
install -m 0755 -o root -g root \
  "${script_dir}/restore-host-install.sh" \
  "$installed_restorer"
install -m 0644 -o root -g root \
  "$bundle_lib_source" \
  "$installed_bundle_lib"
install -m 0755 -o root -g root \
  "${script_dir}/chat.sh" \
  "$installed_chat"
install -m 0755 -o root -g root \
  "${script_dir}/tui.sh" \
  "$installed_tui"
if [[ -n "$bundle_root" && -x "${bundle_root}/payload/pi-runtime/pi" &&
  -f "${bundle_root}/pi/infernex.ts" && -f "${bundle_root}/pi/LICENSE.pi.txt" ]]; then
  install -d -m 0755 -o root -g root "${install_root}/pi"
  if [[ -d "$installed_pi_runtime" ]]; then
    rm -rf -- "$installed_pi_runtime"
  fi
  install -d -m 0755 -o root -g root "$installed_pi_runtime"
  cp -a -- "${bundle_root}/payload/pi-runtime/." "$installed_pi_runtime/"
  chown -R root:root "$installed_pi_runtime"
  chmod 0755 "$installed_pi"
  install -m 0644 -o root -g root "${bundle_root}/pi/infernex.ts" "$installed_pi_extension"
  install -m 0644 -o root -g root "${bundle_root}/pi/LICENSE.pi.txt" "$installed_pi_license"
else
  if [[ -d "$installed_pi_runtime" ]]; then
    rm -rf -- "$installed_pi_runtime"
  fi
  rm -f -- "$installed_pi_extension" "$installed_pi_license"
fi

if [[ -n "$bundle_root" && -d "${bundle_root}/skills" ]]; then
  [[ ! -L "${bundle_root}/skills" ]] || bundle_die "refusing symlinked bundle Skill directory"
  if [[ -d "$installed_builtin_skills" ]]; then
    rm -rf -- "$installed_builtin_skills"
  fi
  install -d -m 0755 -o root -g root "$installed_builtin_skills"
  cp -a -- "${bundle_root}/skills/." "$installed_builtin_skills/"
  chown -R root:root "$installed_builtin_skills"
  while IFS= read -r skill_file; do chmod 0644 "$skill_file"; done < <(find "$installed_builtin_skills" -type f -name '*.md' -print)
  while IFS= read -r skill_dir; do chmod 0755 "$skill_dir"; done < <(find "$installed_builtin_skills" -type d -print)
fi

if [[ -f "$installed_binary" ]]; then
  install -m 0755 -o root -g root "$installed_binary" "${installed_binary}.previous"
fi
temporary_binary="${installed_binary}.new"
install -m 0755 -o root -g root "$binary_source" "$temporary_binary"
mv -f -- "$temporary_binary" "$installed_binary"
install -d -m 0755 -o root -g root /usr/local/bin
temporary_cli="$(mktemp /usr/local/bin/.infernex-agent.XXXXXX)"
bundle_write_host_cli "$temporary_cli" "$installed_binary" "$installed_chat" \
  "$installed_tui" "$installed_pi" "$installed_pi_extension"
chmod 0755 "$temporary_cli"
chown root:root "$temporary_cli"
mv -f -- "$temporary_cli" "$installed_cli"
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
if [[ -f "$agent_config" ]]; then
  while IFS= read -r existing_argument; do
    case "$existing_argument" in
      --evidence-roots=*)
        if [[ "$evidence_roots_set" == "false" ]]; then
          IFS=',' read -r -a evidence_roots <<<"${existing_argument#*=}"
        fi
        ;;
      --report-directory=*)
        [[ "$report_directory_set" == "true" ]] || report_directory="${existing_argument#*=}"
        ;;
    esac
  done <"$agent_config"
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
  "--max-diagnostics-per-scan=${max_diagnostics_per_scan}"
  "--skill-directories=${installed_builtin_skills},${config_root}/skills.d"
)
if [[ -n "$openai_base_url" ]]; then
  agent_args+=(
    "--openai-base-url=${openai_base_url}"
    "--openai-model=${openai_model}"
    "--openai-timeout=${openai_timeout:-3m}"
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
      --context-window-tokens=*)
        [[ "$context_window_set" == "true" ]] || context_window_tokens="${argument#*=}"
        ;;
      --max-output-tokens=*)
        if [[ "$max_output_set" == "false" && "$context_window_set" == "false" ]]; then
          max_output_tokens="${argument#*=}"
        fi
        ;;
      --context-compaction-threshold=*)
        [[ "$context_threshold_set" == "true" ]] || context_compaction_threshold="${argument#*=}"
        ;;
      --context-keep-recent-turns=*)
        [[ "$keep_recent_set" == "true" ]] || context_keep_recent_turns="${argument#*=}"
        ;;
      --tool-result-max-tokens=*)
        if [[ "$tool_result_max_set" == "false" && "$context_window_set" == "false" ]]; then
          tool_result_max_tokens="${argument#*=}"
        fi
        ;;
      --reasoning-display=*)
        [[ "$reasoning_display_set" == "true" ]] || reasoning_display="${argument#*=}"
        ;;
      --execution-mode=*)
        [[ "$execution_mode_set" == "true" ]] || execution_mode="${argument#*=}"
        ;;
      --diagnostic-ssh-config=*)
        [[ "$diagnostic_ssh_config_set" == "true" ]] || diagnostic_ssh_config="${argument#*=}"
        ;;
      --diagnostic-ssh-targets=*)
        if [[ "$diagnostic_ssh_targets_set" == "false" ]]; then
          IFS=',' read -r -a diagnostic_ssh_targets <<<"${argument#*=}"
        fi
        ;;
      --evidence-roots=*)
        if [[ "$evidence_roots_set" == "false" ]]; then
          IFS=',' read -r -a evidence_roots <<<"${argument#*=}"
        fi
        ;;
      --report-directory=*)
        [[ "$report_directory_set" == "true" ]] || report_directory="${argument#*=}"
        ;;
    esac
  done <"$agent_config"
  [[ "$preserved_base_url" == "$preserved_model" ]] ||
    bundle_die "${agent_config} contains incomplete model configuration"
  [[ "$preserved_api_key" != "true" || -f "$installed_api_key" ]] ||
    bundle_die "${agent_config} references a missing OpenAI API key"
fi

# alpha.5 wrote 4096 as its implicit default. Upgrade only that exact known
# default tuple; all explicit or otherwise customized operator values remain
# untouched. Later releases can use the installed version to make equally
# narrow migrations without guessing user intent.
if [[ "$installed_version" == "0.5.0-alpha.5" &&
  "$context_window_set" == "false" &&
  "$max_output_set" == "false" &&
  "$context_window_tokens" == "32768" &&
  "$max_output_tokens" == "4096" ]]; then
  bundle_info "migrating alpha.5 default max output tokens from 4096 to 8192"
  max_output_tokens="8192"
fi

[[ "$context_window_tokens" =~ ^[0-9]+$ ]] ||
  bundle_die "context window tokens must be a positive integer"
((context_window_tokens >= 2048 && context_window_tokens <= 4000000)) ||
  bundle_die "context window tokens must be between 2048 and 4000000"
if [[ -z "$max_output_tokens" ]]; then
	max_output_tokens=$((context_window_tokens / 4))
	((max_output_tokens <= 8192)) || max_output_tokens=8192
fi
if [[ -z "$tool_result_max_tokens" ]]; then
  tool_result_max_tokens=$((context_window_tokens * 15 / 100))
  ((tool_result_max_tokens <= 4096)) || tool_result_max_tokens=4096
fi
for value in "$max_output_tokens" "$context_compaction_threshold" \
  "$context_keep_recent_turns" "$tool_result_max_tokens"; do
  [[ "$value" =~ ^[0-9]+$ ]] || bundle_die "context values must be positive integers"
done
((max_output_tokens >= 128 && max_output_tokens < context_window_tokens)) ||
  bundle_die "max output tokens must be at least 128 and smaller than the context window"
((context_compaction_threshold >= 50 && context_compaction_threshold <= 95)) ||
  bundle_die "context compaction threshold must be between 50 and 95 percent"
((context_keep_recent_turns >= 1 && context_keep_recent_turns <= 32)) ||
  bundle_die "recent turns to keep must be between 1 and 32"
((tool_result_max_tokens >= 128 && tool_result_max_tokens < context_window_tokens)) ||
  bundle_die "tool result token limit must be at least 128 and smaller than the context window"
((max_output_tokens < context_window_tokens * context_compaction_threshold / 100)) ||
  bundle_die "max output tokens must be smaller than the compaction threshold budget"
[[ "$reasoning_display" == "hidden" || "$reasoning_display" == "visible" ]] ||
  bundle_die "reasoning display must be hidden or visible"
case "$execution_mode" in
  detect | diagnose | modify | install | recover) ;;
  *) bundle_die "execution mode must be detect, diagnose, modify, install, or recover" ;;
esac
if ((${#diagnostic_ssh_targets[@]} > 0)); then
  [[ -n "$diagnostic_ssh_config" ]] || bundle_die "diagnostic SSH targets require an OpenSSH config"
  [[ "$diagnostic_ssh_config" == /* && -r "$diagnostic_ssh_config" ]] ||
    bundle_die "diagnostic SSH config must be an absolute readable file"
  runuser -u "$service_user" -- test -r "$diagnostic_ssh_config" ||
    bundle_die "diagnostic SSH config is not readable by ${service_user}"
  for target in "${diagnostic_ssh_targets[@]}"; do
    [[ "$target" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] ||
      bundle_die "invalid diagnostic SSH target alias: ${target}"
  done
fi
declare -a canonical_evidence_roots=()
for evidence_root in "${evidence_roots[@]}"; do
  [[ "$evidence_root" == /* && "$evidence_root" != *','* ]] ||
    bundle_die "evidence roots must be absolute paths without commas"
  canonical_evidence_root="$(readlink -f -- "$evidence_root")"
  [[ -d "$canonical_evidence_root" ]] ||
    bundle_die "evidence root is not an existing directory: ${evidence_root}"
  runuser -u "$service_user" -- test -r "$canonical_evidence_root" &&
    runuser -u "$service_user" -- test -x "$canonical_evidence_root" ||
    bundle_die "evidence root is not readable/traversable by ${service_user}: ${canonical_evidence_root}"
  canonical_evidence_roots+=("$canonical_evidence_root")
done
if [[ -n "$report_directory" ]]; then
  [[ "$report_directory" == /* && "$report_directory" != *','* ]] ||
    bundle_die "report directory must be an absolute path without commas"
  report_directory="$(readlink -f -- "$report_directory")"
  [[ -d "$report_directory" ]] || bundle_die "report directory must already exist"
  runuser -u "$service_user" -- test -w "$report_directory" &&
    runuser -u "$service_user" -- test -x "$report_directory" ||
    bundle_die "report directory is not writable/traversable by ${service_user}: ${report_directory}"
fi
agent_args+=(
  "--context-window-tokens=${context_window_tokens}"
  "--max-output-tokens=${max_output_tokens}"
  "--context-compaction-threshold=${context_compaction_threshold}"
  "--context-keep-recent-turns=${context_keep_recent_turns}"
  "--tool-result-max-tokens=${tool_result_max_tokens}"
  "--reasoning-display=${reasoning_display}"
  "--execution-mode=${execution_mode}"
)
if ((${#diagnostic_ssh_targets[@]} > 0)); then
  diagnostic_ssh_targets_csv="$(IFS=,; printf '%s' "${diagnostic_ssh_targets[*]}")"
  agent_args+=(
    "--diagnostic-ssh-config=${diagnostic_ssh_config}"
    "--diagnostic-ssh-targets=${diagnostic_ssh_targets_csv}"
  )
fi
if ((${#canonical_evidence_roots[@]} > 0)); then
  evidence_roots_csv="$(IFS=,; printf '%s' "${canonical_evidence_roots[*]}")"
  agent_args+=("--evidence-roots=${evidence_roots_csv}")
fi
if [[ -n "$report_directory" ]]; then
  agent_args+=("--report-directory=${report_directory}")
fi
if [[ "$enable_deployment" == "true" ]]; then
  scan_namespaces_csv="$(IFS=,; printf '%s' "${scan_namespaces[*]}")"
  agent_args+=(
    "--enable-deployment"
    "--deployment-namespace=${deployment_namespace}"
    "--deployment-template-namespace=${deployment_template_namespace}"
    "--deployment-source-namespaces=${scan_namespaces_csv}"
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
if [[ "$enable_log_diagnostics" == "true" ]]; then
  agent_args+=("--enable-log-diagnostics")
fi
if [[ "$enable_experiments" == "true" ]]; then
  agent_args+=(
    "--enable-experiments"
    "--experiment-template-namespace=${experiment_template_namespace}"
    "--experiment-readiness-timeout=${experiment_readiness_timeout}"
    "--experiment-soak-duration=${experiment_soak_duration}"
    "--experiment-diagnostic-interval=${experiment_diagnostic_interval}"
  )
fi
if [[ "$enable_deployment" == "true" || "$enable_experiments" == "true" ]]; then
  agent_args+=("--state-dir=${state_root}")
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

if [[ "$interactive_model_setup" == "true" ]]; then
  printf '\nConfigure the model used by the Agent before starting the service.\n' >/dev/tty
  "$installed_configurator" \
    --interactive --test-tools --no-restart </dev/tty >/dev/tty
fi

systemctl daemon-reload

collect_install_failure_evidence() {
  local output_file="$1"
  {
    printf 'stage=systemd-activation\n'
    printf 'mcp_address=%s\n' "$listen_address"
    printf 'dashboard_address=%s\n' "$dashboard_listen_address"
    printf '\n[systemctl status]\n'
    systemctl status infernex-agent.service --no-pager --full 2>&1 |
      awk '{ print substr($0, 1, 1000) }' || true
    printf '\n[recent journal]\n'
    journalctl -u infernex-agent.service --no-pager -n 100 2>&1 |
      awk '{ print substr($0, 1, 1000) }' || true
    if command -v ss >/dev/null 2>&1; then
      printf '\n[listening TCP sockets for configured ports]\n'
      ss -H -ltnp "sport = :${listen_address##*:}" 2>&1 || true
      ss -H -ltnp "sport = :${dashboard_listen_address##*:}" 2>&1 || true
    fi
  } >"$output_file"
}

offer_ai_install_diagnosis() {
  local evidence_file
  grep -q '^--openai-base-url=' "$agent_config" 2>/dev/null || return 0
  evidence_file="$(mktemp /tmp/infernex-agent-install-evidence.XXXXXX)"
  chmod 0600 "$evidence_file"
  collect_install_failure_evidence "$evidence_file"
  bundle_warn "service activation failed; requesting an advisory diagnosis from the configured model"
  "$installed_binary" install-diagnose \
    --config "$agent_config" --evidence "$evidence_file" ||
    bundle_warn "the configured model could not diagnose this installation failure"
  rm -f -- "$evidence_file"
}

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
  systemctl reset-failed infernex-agent.service >/dev/null 2>&1 || true
  if ! systemctl "$service_action" infernex-agent.service; then
    journalctl -u infernex-agent.service --no-pager -n 100 >&2 || true
    offer_ai_install_diagnosis
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
  if ! "${script_dir}/verify-host.sh" "${verify_args[@]}"; then
    offer_ai_install_diagnosis
    bundle_die "infernex-agent.service did not become ready"
  fi
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
bundle_info "interactive terminal: sudo ${installed_chat}"
if [[ -x "$installed_pi" ]]; then
  bundle_info "Pi TUI: sudo ${installed_tui}"
fi
