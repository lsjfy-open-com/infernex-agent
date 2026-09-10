#!/usr/bin/env bash
set -euo pipefail
agent_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# Exercise schema selection without running restoration or touching host files.
selection="$(sed -n '/^legacy_host_targets=(/,/^for target_index in/{ /^for target_index in/!p; }' "${agent_dir}/scripts/host/restore-host-install.sh")"
for count in 19 20 21 22 23; do
  schema="$(
    SCHEMA_COUNT="$count" SCHEMA_SELECTION="$selection" bash -c '
      set -euo pipefail
      backup_dir=/unused
      awk() { printf "%s\n" "$SCHEMA_COUNT"; }
      bundle_die() { echo "$*" >&2; exit 1; }
      eval "$SCHEMA_SELECTION"
      printf "%s\n" "${host_targets[@]}"
    '
  )"
  [[ "$(printf '%s\n' "$schema" | tail -n 1)" == /opt/infernex-agent/pi/host-tools.ts ]]
  [[ "$(printf '%s\n' "$schema" | sed -n '15p')" == /opt/infernex-agent/pi/infernex.ts || "$count" != 19 ]]
  if ((count >= 20)); then
    [[ "$(printf '%s\n' "$schema" | sed -n '16p')" == /opt/infernex-agent/pi/infernex.ts ]]
    [[ "$(printf '%s\n' "$schema" | sed -n '17p')" == /opt/infernex-agent/pi/LICENSE.pi.txt ]]
  fi
  expected=$((count + 1))
  ((count < 23)) || expected=23
  [[ "$(printf '%s\n' "$schema" | wc -l | tr -d ' ')" == "$expected" ]]
done
printf 'Host recovery schemas 19–23 retain original indices and handle mode module\n'
