#!/usr/bin/env bash
set -euo pipefail

agent_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=../../scripts/offline/bundle-lib.sh
source "${agent_dir}/scripts/offline/bundle-lib.sh"

test_root="$(mktemp -d "${TMPDIR:-/tmp}/infernex-agent-bundle-test.XXXXXX")"
cleanup() {
  local resolved
  resolved="$(cd -- "$test_root" 2>/dev/null && pwd || true)"
  if [[ -n "$resolved" && "$resolved" == "${TMPDIR:-/tmp}"/infernex-agent-bundle-test.* ]]; then
    rm -rf -- "$resolved"
  fi
}
trap cleanup EXIT

scoped_file="${test_root}/payload/pi-runtime/node_modules/@scope/file.js"
mkdir -p -- "$(dirname -- "$scoped_file")"
printf 'runtime\n' >"$scoped_file"
(
  cd -- "$test_root"
  sha256sum ./payload/pi-runtime/node_modules/@scope/file.js >SHA256SUMS
)
bundle_verify_checksums "$test_root"

bundle_safe_relative_path 'payload/pi-runtime/node_modules/@scope/file.js'
bundle_safe_relative_path 'payload/file..name'
! bundle_safe_relative_path '/payload/file'
! bundle_safe_relative_path 'payload/../file'

printf '%064d  ./../outside\n' 0 >"${test_root}/SHA256SUMS"
if (bundle_verify_checksums "$test_root" >/dev/null 2>&1); then
  printf 'traversal checksum path was accepted\n' >&2
  exit 1
fi

bash "${agent_dir}/scripts/host/quick-install.sh" --help |
  grep -Fq -- '--skip-checksums'

printf 'bundle path safety tests passed\n'
