#!/usr/bin/env bash
set -euo pipefail
agent_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
install_script="${agent_dir}/scripts/host/install-host.sh"
quick_script="${agent_dir}/scripts/host/quick-install.sh"

[[ "$(sed -n 's/^dashboard_listen_address="\(.*\)"$/\1/p' "$install_script" | head -n 1)" == '0.0.0.0:8081' ]]
[[ "$(sed -n 's/^dashboard_listen_address="\(.*\)"$/\1/p' "$quick_script" | head -n 1)" == '0.0.0.0:8081' ]]

# Run the actual address-selection functions with an isolated old agent.conf.
eval "$(sed -n '/^select_dashboard_bind() {/,/^}/p' "$install_script")"
eval "$(sed -n '/^select_existing_dashboard_bind() {/,/^}/p' "$quick_script")"
eval "$(sed -n '/^dashboard_access_url() {/,/^}/p' "$install_script")"
eval "$(sed -n '/^quick_dashboard_access_url() {/,/^}/p' "$quick_script")"
conf="$(mktemp)"
trap 'rm -f "$conf"' EXIT
printf '%s\n' '--dashboard-listen-address=10.20.30.40:18081' >"$conf"
[[ "$(select_dashboard_bind '0.0.0.0:8081' false "$conf")" == '10.20.30.40:18081' ]]
[[ "$(select_dashboard_bind '0.0.0.0:8081' true "$conf")" == '0.0.0.0:8081' ]]
[[ "$(select_existing_dashboard_bind '0.0.0.0:8081' false "$conf")" == '10.20.30.40:18081' ]]
[[ "$(select_existing_dashboard_bind '0.0.0.0:8081' true "$conf")" == '0.0.0.0:8081' ]]
: >"$conf"
[[ "$(select_dashboard_bind '0.0.0.0:8081' false "$conf")" == '0.0.0.0:8081' ]]
[[ "$(select_existing_dashboard_bind '0.0.0.0:8081' false "$conf")" == '0.0.0.0:8081' ]]
[[ "$(dashboard_access_url '0.0.0.0:8081')" == 'http://<HostIP>:8081/' ]]
[[ "$(dashboard_access_url '10.20.30.40:8081')" == 'http://10.20.30.40:8081/' ]]
[[ "$(dashboard_access_url '[2001:db8::1]:8081')" == 'http://[2001:db8::1]:8081/' ]]
[[ "$(quick_dashboard_access_url '0.0.0.0:8081')" == 'http://<HostIP>:8081/' ]]

# Exercise the real listener ownership logic with a fake systemd MainPID and
# realistic ss rows. The old loopback listener must permit a wildcard upgrade.
eval "$(sed -n '/^address_port() {/,/^}/p' "$quick_script")"
eval "$(sed -n '/^address_is_current_agent_listener() {/,/^}/p' "$quick_script")"
eval "$(sed -n '/^select_dashboard_listener() {/,/^}/p' "$quick_script")"
bundle_warn() { :; }
listener_evidence() { :; }
bundle_die() { exit 42; }
port_is_listening() { return 0; }
systemctl() {
  case "$1" in
    is-active) return 0 ;;
    show) printf '1234\n' ;;
    *) return 1 ;;
  esac
}
fake_ss_mode=agent
ss() {
  case "$fake_ss_mode" in
    agent) printf 'LISTEN 0 4096 127.0.0.1:8081 0.0.0.0:* users:(("infernex-agent",pid=1234,fd=8))\n' ;;
    other) printf 'LISTEN 0 4096 10.20.30.40:8081 0.0.0.0:* users:(("other",pid=9999,fd=8))\n' ;;
    mixed) printf 'LISTEN 0 4096 127.0.0.1:8081 0.0.0.0:* users:(("infernex-agent",pid=1234,fd=8))\nLISTEN 0 4096 10.20.30.40:8081 0.0.0.0:* users:(("other",pid=9999,fd=8))\n' ;;
  esac
}
printf '%s\n' '--dashboard-listen-address=127.0.0.1:8081' >"$conf"
[[ "$(address_is_current_agent_listener '0.0.0.0:8081' "$conf" && printf yes)" == yes ]]
# Use the isolated config path with the unmodified selector.
agent_config_path="$conf"
dashboard_listen_address_explicit=true
[[ "$(select_dashboard_listener '0.0.0.0:8081')" == '0.0.0.0:8081' ]]
fake_ss_mode=other
if address_is_current_agent_listener '0.0.0.0:8081' "$conf"; then
  echo 'accepted other-process listener' >&2; exit 1
fi
if (select_dashboard_listener '0.0.0.0:8081') >/dev/null 2>&1; then
  echo 'accepted occupied explicit Dashboard address' >&2; exit 1
else
  [[ $? -eq 42 ]] || { echo 'unexpected explicit bind failure' >&2; exit 1; }
fi
fake_ss_mode=mixed
if address_is_current_agent_listener '0.0.0.0:8081' "$conf"; then
  echo 'accepted mixed Agent/other-process listeners' >&2; exit 1
fi
# A preserved loopback bind must likewise fail if another process took its port.
preserved="$(select_existing_dashboard_bind '0.0.0.0:8081' false "$conf")"
[[ "$preserved" == '127.0.0.1:8081' ]]
if (select_dashboard_listener "$preserved") >/dev/null 2>&1; then
  echo 'accepted occupied preserved Dashboard address' >&2; exit 1
else
  [[ $? -eq 42 ]] || { echo 'unexpected preserved bind failure' >&2; exit 1; }
fi
# The low-level installer validates the retained value after reading it.
eval "$(sed -n '/^validate_listen_address() {/,/^}/p' "$install_script")"
printf '%s\n' '--dashboard-listen-address=invalid' >"$conf"
preserved="$(select_dashboard_bind '0.0.0.0:8081' false "$conf")"
[[ "$preserved" == invalid ]]
if validate_listen_address "$preserved"; then
  echo 'accepted invalid preserved Dashboard address' >&2; exit 1
fi
# MCP and restricted diagnostic defaults are independent of the Dashboard bind.
[[ "$(sed -n 's/^listen_address="\(.*\)"$/\1/p' "$install_script" | head -n 1)" == '127.0.0.1:8080' ]]
[[ "$(sed -n 's/^diagnostic_subagent_listen_address="\(.*\)"$/\1/p' "$install_script" | head -n 1)" == '127.0.0.1:18082' ]]
printf 'Host Dashboard address defaults, override, upgrade preservation, and URLs pass\n'
