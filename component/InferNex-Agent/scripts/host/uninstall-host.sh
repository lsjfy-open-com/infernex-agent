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
Remove the host/systemd InferNex Agent.

Usage:
  sudo uninstall-host.sh [options]

Options:
  --purge-credentials  Also remove /etc/infernex-agent
  --purge-user         Also remove the infernex-agent system user
  -h, --help           Show this help

Kubernetes RBAC and the ServiceAccount token Secret are deliberately retained.
Remove them separately after confirming no other host Agent uses the identity.
EOF
}

purge_credentials="false"
purge_user="false"
while (($#)); do
  case "$1" in
    --purge-credentials)
      purge_credentials="true"
      shift
      ;;
    --purge-user)
      purge_user="true"
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

[[ ${EUID} -eq 0 ]] || bundle_die "uninstall-host.sh must run as root"
bundle_require_command systemctl

systemctl disable --now infernex-agent.service >/dev/null 2>&1 || true
rm -f -- /etc/systemd/system/infernex-agent.service
systemctl daemon-reload

safe_remove_tree() {
  local expected="$1"
  local resolved
  [[ -e "$expected" ]] || return 0
  resolved="$(readlink -f -- "$expected")"
  [[ "$resolved" == "$expected" ]] ||
    bundle_die "refusing to remove unexpected path: ${resolved}"
  rm -rf -- "$resolved"
}

safe_remove_tree /opt/infernex-agent
safe_remove_tree /var/lib/infernex-agent
if [[ "$purge_credentials" == "true" ]]; then
  safe_remove_tree /etc/infernex-agent
else
  bundle_warn "credentials retained in /etc/infernex-agent"
fi
if [[ "$purge_user" == "true" ]] && id infernex-agent >/dev/null 2>&1; then
  userdel infernex-agent
fi

bundle_info "host Agent removed; InferNex workloads and Kubernetes RBAC were not changed"
