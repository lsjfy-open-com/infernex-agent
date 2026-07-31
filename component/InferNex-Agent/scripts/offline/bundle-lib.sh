#!/usr/bin/env bash

# Shared, intentionally dependency-light helpers for InferNex Agent offline
# bundles. This file is sourced by the bundle-side commands.

bundle_die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

bundle_info() {
  printf '==> %s\n' "$*"
}

bundle_warn() {
  printf 'WARN: %s\n' "$*" >&2
}

bundle_require_command() {
  command -v "$1" >/dev/null 2>&1 ||
    bundle_die "required command not found: $1"
}

bundle_default_root() {
  local script_dir
  script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[1]}")" && pwd)"
  if [[ -f "${script_dir}/../bundle.properties" ]]; then
    (cd -- "${script_dir}/.." && pwd)
    return
  fi
  return 1
}

bundle_property() {
  local root="$1"
  local key="$2"
  local value

  [[ "$key" =~ ^[a-z_]+$ ]] ||
    bundle_die "invalid bundle property key: ${key}"
  [[ -f "${root}/bundle.properties" ]] ||
    bundle_die "bundle.properties is missing from ${root}"

  value="$(
    awk -F= -v wanted="$key" '
      $1 == wanted {
        if (seen++) {
          exit 2
        }
        sub(/^[^=]*=/, "")
        print
      }
      END {
        if (!seen) {
          exit 3
        }
      }
    ' "${root}/bundle.properties"
  )" || bundle_die "invalid or missing bundle property: ${key}"

  [[ "$value" =~ ^[A-Za-z0-9._:/@+-]+$ ]] ||
    bundle_die "unsafe value for bundle property ${key}"
  printf '%s\n' "$value"
}

bundle_safe_relative_path() {
  local value="$1"
  [[ "$value" =~ ^[A-Za-z0-9._/+:-]+$ ]] &&
    [[ "$value" != /* ]] &&
    [[ "$value" != *".."* ]]
}

bundle_verify_checksums() {
  local root="$1"
  local checksum_file="${root}/SHA256SUMS"

  bundle_require_command sha256sum
  [[ -f "$checksum_file" ]] ||
    bundle_die "SHA256SUMS is missing from ${root}"

  if ! awk '
    {
      path = $2
      sub(/^\*/, "", path)
      if (path !~ /^\.\057[A-Za-z0-9._+\/:-]+$/ || path ~ /\.\./) {
        exit 1
      }
    }
  ' "$checksum_file"; then
    bundle_die "SHA256SUMS contains an unsafe path"
  fi

  bundle_info "verifying bundle checksums"
  (cd -- "$root" && sha256sum --check SHA256SUMS)
}

bundle_host_architecture() {
  case "$(uname -m)" in
    x86_64 | amd64)
      printf 'amd64\n'
      ;;
    aarch64 | arm64)
      printf 'arm64\n'
      ;;
    *)
      bundle_die "unsupported host architecture: $(uname -m)"
      ;;
  esac
}
