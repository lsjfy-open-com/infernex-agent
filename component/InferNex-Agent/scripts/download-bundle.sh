#!/usr/bin/env bash
set -euo pipefail

repository="lsjfy-open-com/infernex-agent"

usage() {
  cat <<'EOF'
Download and verify one published InferNex Agent asset.

Usage:
  download-bundle.sh [options]

Options:
  --version VERSION        Required Release version
  --architecture ARCH      amd64, arm64, or auto (default: auto)
  --output-dir DIR         Download directory (default: current directory)
  --no-extract             Verify the archive without extracting it
  -h, --help               Show this help

Normal users do not need this helper: use the one-line installer. This script
downloads and verifies the single management-node Agent package. Kubernetes
development bundles are intentionally not downloadable from the normal
Release.
EOF
}

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command is missing: $1"
}

version=""
architecture="auto"
output_dir="."
extract="true"

while (($#)); do
  case "$1" in
    --version)
      [[ $# -ge 2 ]] || die "--version requires a value"
      version="$2"
      shift 2
      ;;
    --architecture)
      [[ $# -ge 2 ]] || die "--architecture requires a value"
      architecture="$2"
      shift 2
      ;;
    --output-dir)
      [[ $# -ge 2 ]] || die "--output-dir requires a value"
      output_dir="$2"
      shift 2
      ;;
    --no-extract)
      extract="false"
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      die "unknown option: $1"
      ;;
  esac
done

[[ -n "$version" ]] || die "--version is required"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]] ||
  die "invalid version: $version"

if [[ "$architecture" == "auto" ]]; then
  case "$(uname -m)" in
    x86_64 | amd64) architecture="amd64" ;;
    aarch64 | arm64) architecture="arm64" ;;
    *) die "unsupported machine architecture: $(uname -m); pass --architecture" ;;
  esac
fi
case "$architecture" in
  amd64 | arm64) ;;
  *) die "--architecture must be amd64, arm64, or auto" ;;
esac

require_command curl
require_command sha256sum
if [[ "$extract" == "true" ]]; then
  require_command tar
fi

mkdir -p -- "$output_dir"
output_dir="$(cd -- "$output_dir" && pwd)"
bundle_name="infernex-agent-${version}-linux-${architecture}"
asset_name="${bundle_name}.tar.gz"
checksum_name="${asset_name}.sha256"
release_base="https://github.com/${repository}/releases/download/infernex-agent-v${version}"
asset_path="${output_dir}/${asset_name}"
checksum_path="${output_dir}/${checksum_name}"

download() {
  local name="$1"
  local target="$2"
  local temporary="${target}.part"
  rm -f -- "$temporary"
  printf 'downloading %s\n' "${release_base}/${name}"
  curl --fail --location --show-error --silent \
    --retry 3 --retry-delay 2 \
    --output "$temporary" \
    "${release_base}/${name}"
  mv -f -- "$temporary" "$target"
}

if [[ ! -f "$asset_path" || ! -f "$checksum_path" ]]; then
  download "$asset_name" "$asset_path"
  download "$checksum_name" "$checksum_path"
else
  printf 'using existing download %s\n' "$asset_path"
fi

printf 'verifying %s\n' "$checksum_name"
(
  cd -- "$output_dir"
  sha256sum --check "$checksum_name"
) || die "checksum verification failed; remove both downloaded files and retry"

if [[ "$extract" == "true" ]]; then
  bundle_dir="${output_dir}/${bundle_name}"
  if [[ -e "$bundle_dir" ]]; then
    die "refusing to overwrite existing extraction path: $bundle_dir"
  fi
  while IFS= read -r entry; do
    case "$entry" in
      "$bundle_name" | "$bundle_name"/*) ;;
      *) die "archive contains an unexpected path: $entry" ;;
    esac
    case "/${entry}/" in
      */../*) die "archive contains a parent-directory path: $entry" ;;
    esac
  done < <(tar -tzf "$asset_path")
  printf 'extracting %s\n' "$asset_name"
  tar -C "$output_dir" \
    --no-same-owner --no-same-permissions \
    -xzf "$asset_path"
  [[ -d "$bundle_dir" && ! -L "$bundle_dir" ]] || {
    die "archive did not create the expected bundle directory: $bundle_dir"
  }

  printf '\nverified bundle: %s\n' "$bundle_dir"
  cat <<EOF

Next step for the management-node Agent:
  cd '${bundle_dir}'
  sudo ./install.sh
EOF
else
  printf 'verified archive: %s\n' "$asset_path"
fi
