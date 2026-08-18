#!/usr/bin/env bash
set -euo pipefail

agent_binary="/opt/infernex-agent/bin/infernex-agent"
agent_config="/etc/infernex-agent/agent.conf"

if [[ ${EUID} -ne 0 ]]; then
	printf 'error: run with sudo so the TUI can read the protected Agent configuration and inherit operator access\n' >&2
	printf 'example: sudo /opt/infernex-agent/bin/tui.sh\n' >&2
	exit 1
fi

exec "$agent_binary" tui --config "$agent_config" "$@"
