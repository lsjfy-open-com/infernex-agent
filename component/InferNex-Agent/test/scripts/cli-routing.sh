#!/usr/bin/env bash
set -euo pipefail

agent_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=../../scripts/offline/bundle-lib.sh
source "${agent_dir}/scripts/offline/bundle-lib.sh"

test_root="$(mktemp -d "${TMPDIR:-/tmp}/infernex-agent-cli-test.XXXXXX")"
cleanup() {
  local resolved
  resolved="$(cd -- "$test_root" 2>/dev/null && pwd || true)"
  if [[ -n "$resolved" && "$resolved" == "${TMPDIR:-/tmp}"/infernex-agent-cli-test.* ]]; then
    rm -rf -- "$resolved"
  fi
}
trap cleanup EXIT

export ROUTE_LOG="${test_root}/route.log"
for command_name in agent chat tui; do
  cat >"${test_root}/${command_name}" <<EOF
#!/usr/bin/env bash
printf '${command_name}:%s\n' "\$*" >>"\$ROUTE_LOG"
EOF
  chmod 0755 "${test_root}/${command_name}"
done
mkdir -p "${test_root}/pi"
cp -- "${test_root}/agent" "${test_root}/pi/pi"
printf 'extension\n' >"${test_root}/pi/infernex.ts"

cli="${test_root}/infernex-agent"
bundle_write_host_cli "$cli" "${test_root}/agent" "${test_root}/chat" \
  "${test_root}/tui" "${test_root}/pi/pi" "${test_root}/pi/infernex.ts"
chmod 0755 "$cli"

"$cli" chat
"$cli" chat --classic
"$cli" chat --ask 'status'
"$cli" chat-classic --usage
"$cli" tui -- --resume
"$cli" version

cat >"${test_root}/expected.log" <<'EOF'
tui:
chat:
chat:--ask status
chat:--usage
tui:-- --resume
agent:version
EOF
cmp "${test_root}/expected.log" "$ROUTE_LOG"

: >"$ROUTE_LOG"
rm -f -- "${test_root}/pi/pi"
"$cli" chat
grep -Fxq 'chat:' "$ROUTE_LOG"

printf 'CLI routing tests passed\n'
