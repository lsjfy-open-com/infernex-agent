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

# Keep the source documentation hierarchy in both distributions. Historical
# proposals and videos stay in the repository rather than normal user bundles.
bundle_copy_documentation() {
  local agent_dir="$1" root="$2" file relative
  while IFS= read -r file; do
    relative="${file#"${agent_dir}/docs/"}"
    mkdir -p "${root}/docs/$(dirname -- "$relative")"
    install -m 0644 "$file" "${root}/docs/${relative}"
  done < <(find "${agent_dir}/docs" -type f -name '*.md' ! -path '*/archive/*' | LC_ALL=C sort)
  cat >"${root}/README.md" <<'DOC'
# InferNex Agent installation bundle

For the standard management-node package, verify the adjacent archive SHA256,
extract it and run `sudo ./install.sh`. Existing model configuration is preserved
on upgrade. Choose the package for the management node's CPU architecture.

See [installation](docs/guides/offline-install-zh.md),
[model configuration](docs/guides/model-configuration-zh.md),
[Pi TUI](docs/guides/pi-tui-zh.md), and
[current capability boundaries](docs/architecture/kubernetes-first-zh.md).

The advanced Kubernetes bundle uses its bin/install-agent.sh entrypoint as
documented in the installation guide. Historical proposals remain in the source
repository and are not evidence of implemented capabilities.
DOC
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
  [[ "$value" =~ ^[A-Za-z0-9._/@+:-]+$ ]] &&
    [[ "$value" != /* ]] &&
    [[ "/${value}/" != *"/../"* ]]
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
      if (path !~ /^\.\057[A-Za-z0-9._@+\/:-]+$/ ||
          ("/" substr(path, 3) "/") ~ /\/\.\.\//) {
        exit 1
      }
    }
  ' "$checksum_file"; then
    bundle_die "SHA256SUMS contains an unsafe path"
  fi

  bundle_info "verifying bundle checksums"
  (cd -- "$root" && sha256sum --check SHA256SUMS)
}

bundle_write_host_cli() {
  local target="$1"
  local agent_binary="$2"
  local chat_script="$3"
  local tui_script="$4"
  local pi_binary="$5"
  local pi_extension="$6"
  local agent_q chat_q tui_q pi_q extension_q

  printf -v agent_q '%q' "$agent_binary"
  printf -v chat_q '%q' "$chat_script"
  printf -v tui_q '%q' "$tui_script"
  printf -v pi_q '%q' "$pi_binary"
  printf -v extension_q '%q' "$pi_extension"
  cat >"$target" <<EOF
#!/usr/bin/env bash
# Managed by InferNex Agent host installer.
set -euo pipefail

case "\${1:-}" in
  chat)
    shift
    if ((\$# == 0)) && [[ -x ${pi_q} && -f ${extension_q} ]]; then
      exec ${tui_q}
    fi
    if [[ "\${1:-}" == "--classic" ]]; then
      shift
    fi
    exec ${chat_q} "\$@"
    ;;
  chat-classic)
    shift
    exec ${chat_q} "\$@"
    ;;
  tui)
    shift
    exec ${tui_q} "\$@"
    ;;
  *)
    exec ${agent_q} "\$@"
    ;;
esac
EOF
  bash -n "$target"
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
