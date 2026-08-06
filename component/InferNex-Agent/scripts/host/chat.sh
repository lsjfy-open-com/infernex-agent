#!/usr/bin/env bash
set -euo pipefail

service_user="infernex-agent"
agent_binary="/opt/infernex-agent/bin/infernex-agent"
agent_config="/etc/infernex-agent/agent.conf"

if [[ ${EUID} -ne 0 ]]; then
  printf 'error: run with sudo so the terminal can use the protected Agent identity\n' >&2
  printf 'example: sudo /opt/infernex-agent/bin/chat.sh\n' >&2
  exit 1
fi
command -v runuser >/dev/null 2>&1 || {
  printf 'error: runuser is required (provided by util-linux on openEuler)\n' >&2
  exit 1
}
id "$service_user" >/dev/null 2>&1 || {
  printf 'error: service user does not exist: %s\n' "$service_user" >&2
  exit 1
}
[[ -x "$agent_binary" ]] || {
  printf 'error: Agent binary is not executable: %s\n' "$agent_binary" >&2
  exit 1
}
[[ -r "$agent_config" ]] || {
  printf 'error: Agent configuration is not readable: %s\n' "$agent_config" >&2
  exit 1
}

exec runuser -u "$service_user" -- \
  "$agent_binary" chat --config "$agent_config" "$@"
