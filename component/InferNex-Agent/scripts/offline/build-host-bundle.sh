#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
agent_dir="$(cd -- "${script_dir}/../.." && pwd)"
repo_root="$(cd -- "${agent_dir}/../.." && pwd)"

# shellcheck source=bundle-lib.sh
source "${script_dir}/bundle-lib.sh"

usage() {
  cat <<'EOF'
Build a static-binary InferNex Agent bundle for a Linux/openEuler host.

Usage:
  build-host-bundle.sh [options]

Options:
  --version VERSION       Agent version (default: Chart.yaml version)
  --architecture ARCH    amd64 or arm64 (default: current host)
  --binary FILE          Reuse an already-built static Linux binary
  --output-dir DIR       Destination directory (default: ./dist)
  --force                Replace an existing bundle with the same name
  -h, --help             Show this help

Without --binary, the command cross-compiles with the local Go toolchain using
CGO_ENABLED=0. The result does not depend on glibc and is suitable for
openEuler, provided the CPU architecture matches.
EOF
}

version="$(
  awk '$1 == "version:" {print $2; exit}' \
    "${agent_dir}/chart/infernex-agent/Chart.yaml"
)"
architecture="$(bundle_host_architecture)"
binary_source=""
output_dir="${PWD}/dist"
force="false"
go_bin="${GO_BIN:-go}"

while (($#)); do
  case "$1" in
    --version)
      [[ $# -ge 2 ]] || bundle_die "--version requires a value"
      version="$2"
      shift 2
      ;;
    --architecture)
      [[ $# -ge 2 ]] || bundle_die "--architecture requires a value"
      architecture="$2"
      shift 2
      ;;
    --binary)
      [[ $# -ge 2 ]] || bundle_die "--binary requires a value"
      binary_source="$2"
      shift 2
      ;;
    --output-dir)
      [[ $# -ge 2 ]] || bundle_die "--output-dir requires a value"
      output_dir="$2"
      shift 2
      ;;
    --force)
      force="true"
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

[[ "$version" =~ ^[0-9A-Za-z][0-9A-Za-z.+-]*$ ]] ||
  bundle_die "invalid version: ${version}"
case "$architecture" in
  amd64 | arm64) ;;
  *) bundle_die "--architecture must be amd64 or arm64" ;;
esac
if [[ -n "$binary_source" ]]; then
  [[ -f "$binary_source" ]] ||
    bundle_die "binary does not exist: ${binary_source}"
fi

bundle_require_command sha256sum
bundle_require_command tar
if [[ -z "$binary_source" ]]; then
  bundle_require_command "$go_bin"
fi

mkdir -p -- "$output_dir"
output_dir="$(cd -- "$output_dir" && pwd)"
bundle_name="infernex-agent-host-offline-${version}-linux-${architecture}"
archive_name="${bundle_name}.tar.gz"
archive_output="${output_dir}/${archive_name}"
archive_checksum="${archive_output}.sha256"
if [[ "$force" != "true" && ( -e "$archive_output" || -e "$archive_checksum" ) ]]; then
  bundle_die "${archive_name} already exists; pass --force to replace it"
fi

work_dir="$(mktemp -d "${TMPDIR:-/tmp}/infernex-agent-host-bundle.XXXXXX")"
cleanup() {
  local resolved
  resolved="$(cd -- "$work_dir" 2>/dev/null && pwd || true)"
  if [[ -n "$resolved" && "$resolved" == "${TMPDIR:-/tmp}"/infernex-agent-host-bundle.* ]]; then
    rm -rf -- "$resolved"
  fi
}
trap cleanup EXIT

bundle_root="${work_dir}/${bundle_name}"
mkdir -p "${bundle_root}/bin" "${bundle_root}/docs" "${bundle_root}/payload"
binary_target="${bundle_root}/payload/infernex-agent"

if [[ -n "$binary_source" ]]; then
  bundle_info "copying supplied linux/${architecture} binary"
  install -m 0755 "$binary_source" "$binary_target"
else
  bundle_info "building static linux/${architecture} binary"
  (
    cd -- "$agent_dir"
    CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" \
      "$go_bin" build -trimpath \
      -ldflags="-s -w -X main.version=${version}" \
      -o "$binary_target" ./cmd/infernex-agent
  )
  chmod 0755 "$binary_target"
fi
[[ -s "$binary_target" ]] || bundle_die "built Agent binary is empty"

install -m 0755 \
  "${script_dir}/bundle-lib.sh" \
  "${agent_dir}/scripts/host/configure-model.sh" \
  "${agent_dir}/scripts/host/create-kubeconfig.sh" \
  "${agent_dir}/scripts/host/install-host.sh" \
  "${agent_dir}/scripts/host/restore-host-install.sh" \
  "${agent_dir}/scripts/host/uninstall-host.sh" \
  "${agent_dir}/scripts/host/verify-host.sh" \
  "${bundle_root}/bin/"
install -m 0644 \
  "${agent_dir}/docs/host-install-openeuler-zh.md" \
  "${bundle_root}/README.md"
install -m 0644 \
  "${agent_dir}/docs/product-guide-zh.md" \
  "${agent_dir}/docs/product-design-zh.md" \
  "${agent_dir}/docs/model-configuration-zh.md" \
  "${agent_dir}/docs/security-boundaries-zh.md" \
  "${agent_dir}/docs/operations-runbook-zh.md" \
  "${agent_dir}/docs/change-safety-zh.md" \
  "${bundle_root}/docs/"
install -m 0644 "${repo_root}/LICENSE" "${bundle_root}/LICENSE"

created_utc="$(date -u +'%Y-%m-%dT%H:%M:%SZ')"
cat >"${bundle_root}/bundle.properties" <<EOF
format=infernex-agent-host-offline-v1
agent_version=${version}
architecture=${architecture}
binary=payload/infernex-agent
created_utc=${created_utc}
EOF

bundle_info "writing content checksums"
(
  cd -- "$bundle_root"
  while IFS= read -r file; do
    sha256sum "$file"
  done < <(find . -type f ! -name SHA256SUMS -print | LC_ALL=C sort)
) >"${bundle_root}/SHA256SUMS"

temporary_archive="${work_dir}/${archive_name}"
tar -C "$work_dir" -czf "$temporary_archive" "$bundle_name"
mv -f -- "$temporary_archive" "$archive_output"
(
  cd -- "$output_dir"
  sha256sum "$archive_name"
) >"$archive_checksum"

bundle_info "host bundle created: ${archive_output}"
bundle_info "outer checksum: ${archive_checksum}"
