#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=bundle-lib.sh
source "${script_dir}/bundle-lib.sh"

usage() {
  cat <<'EOF'
Import all images in an InferNex Agent offline bundle into the local runtime.

Usage:
  load-images.sh [options]

Options:
  --bundle-dir DIR              Extracted bundle root
  --runtime RUNTIME             auto, ctr, nerdctl, docker, podman, or k3s
  --containerd-address SOCKET   Optional ctr/nerdctl containerd socket
  --containerd-namespace NAME   Containerd namespace (default: k8s.io)
  --skip-checksums              Skip content checksum verification
  -h, --help                    Show this help

Run this command on every Kubernetes node that may host the Agent, unless the
image has been mirrored to an internal registry.
EOF
}

bundle_root="$(bundle_default_root || true)"
runtime="auto"
containerd_address=""
containerd_namespace="k8s.io"
verify_checksums="true"

while (($#)); do
  case "$1" in
    --bundle-dir)
      [[ $# -ge 2 ]] || bundle_die "--bundle-dir requires a value"
      bundle_root="$2"
      shift 2
      ;;
    --runtime)
      [[ $# -ge 2 ]] || bundle_die "--runtime requires a value"
      runtime="$2"
      shift 2
      ;;
    --containerd-address)
      [[ $# -ge 2 ]] || bundle_die "--containerd-address requires a value"
      containerd_address="$2"
      shift 2
      ;;
    --containerd-namespace)
      [[ $# -ge 2 ]] || bundle_die "--containerd-namespace requires a value"
      containerd_namespace="$2"
      shift 2
      ;;
    --skip-checksums)
      verify_checksums="false"
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

[[ -n "$bundle_root" ]] ||
  bundle_die "--bundle-dir is required outside an extracted bundle"
bundle_root="$(cd -- "$bundle_root" && pwd)"

if [[ "$verify_checksums" == "true" ]]; then
  bundle_verify_checksums "$bundle_root"
fi

[[ "$(bundle_property "$bundle_root" format)" == "infernex-agent-offline-v1" ]] ||
  bundle_die "unsupported bundle format"
bundle_architecture="$(bundle_property "$bundle_root" architecture)"
host_architecture="$(bundle_host_architecture)"
[[ "$bundle_architecture" == "$host_architecture" ]] ||
  bundle_die "bundle architecture ${bundle_architecture} does not match host ${host_architecture}"

archive_relative="$(bundle_property "$bundle_root" image_archive)"
bundle_safe_relative_path "$archive_relative" ||
  bundle_die "unsafe image archive path"
archive_path="${bundle_root}/${archive_relative}"
[[ -f "$archive_path" ]] ||
  bundle_die "image archive is missing: ${archive_relative}"

case "$runtime" in
  auto)
    if command -v nerdctl >/dev/null 2>&1; then
      runtime="nerdctl"
    elif command -v ctr >/dev/null 2>&1; then
      runtime="ctr"
    elif command -v k3s >/dev/null 2>&1; then
      runtime="k3s"
    elif command -v docker >/dev/null 2>&1; then
      runtime="docker"
    elif command -v podman >/dev/null 2>&1; then
      runtime="podman"
    else
      bundle_die "no supported image import command found"
    fi
    ;;
  ctr | nerdctl | docker | podman | k3s) ;;
  *) bundle_die "unsupported runtime: ${runtime}" ;;
esac

bundle_require_command "$runtime"
bundle_info "importing images with ${runtime}"

case "$runtime" in
  ctr)
    ctr_args=(--namespace "$containerd_namespace")
    if [[ -n "$containerd_address" ]]; then
      ctr_args+=(--address "$containerd_address")
    fi
    ctr "${ctr_args[@]}" images import "$archive_path"
    ;;
  nerdctl)
    nerdctl_args=(--namespace "$containerd_namespace")
    if [[ -n "$containerd_address" ]]; then
      nerdctl_args+=(--address "$containerd_address")
    fi
    nerdctl "${nerdctl_args[@]}" load --input "$archive_path"
    ;;
  k3s)
    k3s ctr --namespace "$containerd_namespace" images import "$archive_path"
    ;;
  docker)
    docker load --input "$archive_path"
    ;;
  podman)
    podman load --input "$archive_path"
    ;;
esac

bundle_info "bundle image references"
while IFS= read -r image_ref || [[ -n "$image_ref" ]]; do
  [[ -z "$image_ref" ]] && continue
  [[ "$image_ref" =~ ^[A-Za-z0-9._:/@-]+$ ]] ||
    bundle_die "unsafe image reference in image-references.txt"
  printf '  %s\n' "$image_ref"
done <"${bundle_root}/image-references.txt"

bundle_info "image import completed"
