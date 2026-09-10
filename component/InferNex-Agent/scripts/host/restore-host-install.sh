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
Restore one InferNex Agent host installation recovery point.

Usage:
  sudo restore-host-install.sh --backup-dir DIR --confirm [options]

Options:
  --backup-dir DIR    Exact /var/lib/infernex-agent/backups/install-* recovery point
  --kubeconfig FILE  Current recovery credential (default: /etc/infernex-agent/kubeconfig)
  --confirm          Required destructive-action acknowledgement
  -h, --help         Show this help

The command verifies checksums, restores Agent-managed cluster source resources
when the recovery point contains them, then restores the exact Agent host files
and systemd active/enabled state. Generic Kubernetes installations do not
change cluster resources, so their recovery points restore host state only.
EOF
}

backup_dir=""
kubeconfig="/etc/infernex-agent/kubeconfig"
confirm="false"
while (($#)); do
  case "$1" in
    --backup-dir)
      [[ $# -ge 2 ]] || bundle_die "--backup-dir requires a value"
      backup_dir="$2"
      shift 2
      ;;
    --kubeconfig)
      [[ $# -ge 2 ]] || bundle_die "--kubeconfig requires a value"
      kubeconfig="$2"
      shift 2
      ;;
    --confirm)
      confirm="true"
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
  bundle_die "restore-host-install.sh must run as root"
[[ "$confirm" == "true" ]] ||
  bundle_die "--confirm is required"
bundle_require_command systemctl
bundle_require_command sha256sum
bundle_require_command awk
bundle_require_command cp
bundle_require_command readlink
bundle_require_command grep

[[ -n "$backup_dir" && -d "$backup_dir" && ! -L "$backup_dir" ]] ||
  bundle_die "--backup-dir must name a recovery-point directory"
backup_dir="$(readlink -f -- "$backup_dir")"
case "$backup_dir" in
  /var/lib/infernex-agent/backups/install-*) ;;
  *) bundle_die "backup must be under /var/lib/infernex-agent/backups/install-*" ;;
esac
[[ -r "$kubeconfig" ]] ||
  bundle_die "recovery kubeconfig is not readable: ${kubeconfig}"
[[ -x /opt/infernex-agent/bin/infernex-agent ]] ||
  bundle_die "current Agent binary is required for cluster restore"
[[ -f "${backup_dir}/cluster-state.json" &&
  -f "${backup_dir}/cluster-state.sha256" &&
  -f "${backup_dir}/host/manifest" &&
  -f "${backup_dir}/host/checksums.sha256" &&
  -f "${backup_dir}/host/service-state" ]] ||
  bundle_die "recovery point is incomplete"

bundle_info "verifying recovery-point checksums"
(
  cd -- "$backup_dir"
  sha256sum --check cluster-state.sha256
)
(
  cd -- "${backup_dir}/host"
  sha256sum --check checksums.sha256
)

legacy_host_targets=(
  /opt/infernex-agent/bin/infernex-agent
  /opt/infernex-agent/bin/infernex-agent.previous
  /opt/infernex-agent/bin/run-agent.sh
  /etc/infernex-agent/kubeconfig
  /etc/infernex-agent/openai-api-key
  /etc/infernex-agent/agent.conf
  /opt/infernex-agent/bin/configure-model.sh
  /opt/infernex-agent/bin/restore-host-install.sh
  /opt/infernex-agent/bin/bundle-lib.sh
  /etc/systemd/system/infernex-agent.service
  /opt/infernex-agent/bin/chat.sh
  /usr/local/bin/infernex-agent
  /opt/infernex-agent/bin/tui.sh
  /opt/infernex-agent/pi-runtime
  /opt/infernex-agent/pi/infernex.ts
  /opt/infernex-agent/pi/LICENSE.pi.txt
  /opt/infernex-agent/bin/configure-evidence.sh
  /opt/infernex-agent/bin/configure-skills.sh
  /opt/infernex-agent/skills
)
collector_host_targets=(
  /opt/infernex-agent/bin/infernex-agent
  /opt/infernex-agent/bin/infernex-agent.previous
  /opt/infernex-agent/bin/run-agent.sh
  /etc/infernex-agent/kubeconfig
  /etc/infernex-agent/openai-api-key
  /etc/infernex-agent/agent.conf
  /opt/infernex-agent/bin/configure-model.sh
  /opt/infernex-agent/bin/restore-host-install.sh
  /opt/infernex-agent/bin/bundle-lib.sh
  /etc/systemd/system/infernex-agent.service
  /etc/systemd/system/infernex-agent-collector.service
  /opt/infernex-agent/bin/chat.sh
  /usr/local/bin/infernex-agent
  /opt/infernex-agent/bin/tui.sh
  /opt/infernex-agent/pi-runtime
  /opt/infernex-agent/pi/infernex.ts
  /opt/infernex-agent/pi/LICENSE.pi.txt
  /opt/infernex-agent/bin/configure-evidence.sh
  /opt/infernex-agent/bin/configure-skills.sh
  /opt/infernex-agent/skills
)

manifest_count="$(awk 'END {print NR}' "${backup_dir}/host/manifest")"
case "$manifest_count" in
  "${#legacy_host_targets[@]}")
    # Recovery points before the isolated collector helper used this exact order.
    legacy_manifest="true"
    host_targets=("${legacy_host_targets[@]}")
    ;;
  20)
    legacy_manifest="false"
    host_targets=("${collector_host_targets[@]}")
    ;;
  21)
    # Keep schemas stable. New targets are appended, never inserted.
    legacy_manifest="false"
    host_targets=("${collector_host_targets[@]}" /etc/infernex-agent/diagnostic-subagent-token)
    ;;
  22)
    legacy_manifest="false"
    host_targets=(
      "${collector_host_targets[@]}"
      /etc/infernex-agent/diagnostic-subagent-token
      /opt/infernex-agent/tools
    )
    ;;
  23)
    legacy_manifest="false"
    host_targets=(
      "${collector_host_targets[@]}"
      /etc/infernex-agent/diagnostic-subagent-token
      /opt/infernex-agent/tools
      /opt/infernex-agent/pi/host-tools.ts
    )
    ;;
  *) bundle_die "recovery manifest has an unsupported target count" ;;
esac
if ((manifest_count < 23)); then
  # This module did not exist in older schemas; remove it when restoring them.
  host_targets+=(/opt/infernex-agent/pi/host-tools.ts)
fi
for target_index in "${!host_targets[@]}"; do
  target="${host_targets[$target_index]}"
  manifest_status="$(
    awk -F '\t' -v item_index="$target_index" -v target="$target" \
      '$1 == item_index && $3 == target {print $2}' \
      "${backup_dir}/host/manifest"
  )"
  if [[ -z "$manifest_status" && "$target_index" -ge "$manifest_count" ]]; then
    # Targets appended by a newer Agent version were absent in an older baseline.
    manifest_status="absent"
  fi
  [[ "$manifest_status" == "present" || "$manifest_status" == "absent" ]] ||
    bundle_die "recovery manifest does not match target ${target}"
  if [[ "$manifest_status" == "present" ]]; then
    [[ -e "${backup_dir}/host/${target_index}" &&
      ! -L "${backup_dir}/host/${target_index}" ]] ||
      bundle_die "backup file is missing or unsafe for ${target}"
  fi
done

if grep -q '"kind": "InstallationBaseline"' "${backup_dir}/cluster-state.json"; then
  bundle_info "compatibility-mode baseline changed no cluster resources; skipping cluster restore"
else
  bundle_info "restoring Agent-managed cluster source resources"
  /opt/infernex-agent/bin/infernex-agent cluster-state restore \
    --kubeconfig "$kubeconfig" \
    --input "${backup_dir}/cluster-state.json" \
    --confirm
fi

service_was_active="$(
  awk -F= '$1 == "active" {print $2}' "${backup_dir}/host/service-state"
)"
service_was_enabled="$(
  awk -F= '$1 == "enabled" {print $2}' "${backup_dir}/host/service-state"
)"
collector_was_active="$(
  awk -F= '$1 == "collector_active" {print $2}' "${backup_dir}/host/service-state"
)"
collector_was_enabled="$(
  awk -F= '$1 == "collector_enabled" {print $2}' "${backup_dir}/host/service-state"
)"
# Older recovery points predate the isolated collector helper.
collector_was_active="${collector_was_active:-false}"
collector_was_enabled="${collector_was_enabled:-false}"
[[ "$service_was_active" == "true" || "$service_was_active" == "false" ]] ||
  bundle_die "invalid saved active state"
[[ "$service_was_enabled" == "true" || "$service_was_enabled" == "false" ]] ||
  bundle_die "invalid saved enabled state"
[[ "$collector_was_active" == "true" || "$collector_was_active" == "false" ]] ||
  bundle_die "invalid saved collector active state"
[[ "$collector_was_enabled" == "true" || "$collector_was_enabled" == "false" ]] ||
  bundle_die "invalid saved collector enabled state"

bundle_info "restoring Agent host files"
systemctl stop infernex-agent.service >/dev/null 2>&1 || true
systemctl stop infernex-agent-collector.service >/dev/null 2>&1 || true
for target_index in "${!host_targets[@]}"; do
  target="${host_targets[$target_index]}"
  manifest_status="$(
    awk -F '\t' -v item_index="$target_index" -v target="$target" \
      '$1 == item_index && $3 == target {print $2}' \
      "${backup_dir}/host/manifest"
  )"
  if [[ -z "$manifest_status" && "$target_index" -ge "$manifest_count" ]]; then
    manifest_status="absent"
  fi
  if [[ "$manifest_status" == "present" ]]; then
    if [[ -L "$target" ]]; then
      rm -f -- "$target"
    elif [[ -d "$target" ]]; then
      rm -rf -- "$target"
    fi
    cp --archive --no-dereference -- \
      "${backup_dir}/host/${target_index}" "$target"
  else
    if [[ -d "$target" && ! -L "$target" ]]; then
      rm -rf -- "$target"
    else
      rm -f -- "$target"
    fi
  fi
done
if [[ "$legacy_manifest" == "true" ]]; then
  # The helper did not exist in this baseline, but may exist in the version
  # currently being rolled back. It is an exact Agent-owned path.
  rm -f -- /etc/systemd/system/infernex-agent-collector.service
fi
if ((manifest_count < 21)); then
  rm -f -- /etc/infernex-agent/diagnostic-subagent-token
fi

systemctl daemon-reload
if [[ "$collector_was_enabled" == "true" ]]; then
  systemctl enable infernex-agent-collector.service >/dev/null
else
  systemctl disable infernex-agent-collector.service >/dev/null 2>&1 || true
fi
if [[ "$collector_was_active" == "true" ]]; then
  systemctl reset-failed infernex-agent-collector.service >/dev/null 2>&1 || true
  systemctl start infernex-agent-collector.service
fi
if [[ "$service_was_enabled" == "true" ]]; then
  systemctl enable infernex-agent.service >/dev/null
else
  systemctl disable infernex-agent.service >/dev/null 2>&1 || true
fi
if [[ "$service_was_active" == "true" ]]; then
  systemctl reset-failed infernex-agent.service >/dev/null 2>&1 || true
  systemctl start infernex-agent.service
fi

bundle_info "host installation recovery completed"
bundle_info "recovery point retained at ${backup_dir}"
