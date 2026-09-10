#!/usr/bin/env bash
set -euo pipefail

[[ $# -eq 1 ]] || {
  printf 'usage: candidate-smoke.sh /path/to/infernex-agent\n' >&2
  exit 2
}
candidate="$(readlink -f -- "$1")"
[[ -x "$candidate" ]] || {
  printf 'candidate is not executable: %s\n' "$candidate" >&2
  exit 1
}

work_dir="$(mktemp -d "${TMPDIR:-/tmp}/infernex-candidate-smoke.XXXXXX")"
cleanup() {
  case "$work_dir" in
    "${TMPDIR:-/tmp}"/infernex-candidate-smoke.*) rm -rf -- "$work_dir" ;;
  esac
}
trap cleanup EXIT

target="${work_dir}/installed"
state_dir="${work_dir}/state"
printf 'previous-binary-fixture\n' >"$target"
chmod 0755 "$target"

"$candidate" candidate verify --file "$candidate" --json \
  | grep -q '"static":true'
"$candidate" candidate apply \
  --file "$candidate" \
  --target "$target" \
  --state-dir "$state_dir" \
  --no-restart
cmp -- "$candidate" "$target"

"$target" candidate rollback \
  --target "$target" \
  --state-dir "$state_dir" \
  --no-restart
grep -q '^previous-binary-fixture$' "$target"
grep -q '"status": "rolled_back"' "${state_dir}/current.json"

printf 'candidate smoke test passed\n'
