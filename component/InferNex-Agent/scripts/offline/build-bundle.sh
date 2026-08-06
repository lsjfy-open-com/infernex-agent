#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
agent_dir="$(cd -- "${script_dir}/../.." && pwd)"
component_dir="$(cd -- "${agent_dir}/.." && pwd)"
repo_root="$(cd -- "${agent_dir}/../.." && pwd)"

# shellcheck source=bundle-lib.sh
source "${script_dir}/bundle-lib.sh"

usage() {
  cat <<'EOF'
Build a transferable InferNex Agent offline bundle on a connected Linux host.

Usage:
  build-bundle.sh [options]

Options:
  --version VERSION          Agent/chart version (default: Chart.yaml version)
  --architecture ARCH       amd64 or arm64 (default: current host)
  --agent-image REF         Exact image reference stored in the bundle
  --image-source MODE       build, pull, or local (default: build)
  --image-archive FILE      Reuse an existing docker/OCI image archive
  --extra-images FILE       Additional image references, one per line
  --container-tool TOOL     docker, podman, or nerdctl (default: docker)
  --output-dir DIR          Destination directory (default: ./dist)
  --force                   Replace an existing bundle with the same name
  -h, --help                Show this help

The default "build" mode builds InferNex Agent from this checkout. Use
--extra-images for images referenced by approved recovery profiles. Model
weights and credentials are never added automatically.
EOF
}

chart_version="$(
  awk '$1 == "version:" {print $2; exit}' \
    "${agent_dir}/chart/infernex-agent/Chart.yaml"
)"
version="$chart_version"
architecture="$(bundle_host_architecture)"
agent_image=""
image_source="build"
image_archive=""
extra_images_file=""
container_tool="docker"
output_dir="${PWD}/dist"
force="false"
helm_bin="${HELM_BIN:-helm}"

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
    --agent-image)
      [[ $# -ge 2 ]] || bundle_die "--agent-image requires a value"
      agent_image="$2"
      shift 2
      ;;
    --image-source)
      [[ $# -ge 2 ]] || bundle_die "--image-source requires a value"
      image_source="$2"
      shift 2
      ;;
    --image-archive)
      [[ $# -ge 2 ]] || bundle_die "--image-archive requires a value"
      image_archive="$2"
      shift 2
      ;;
    --extra-images)
      [[ $# -ge 2 ]] || bundle_die "--extra-images requires a value"
      extra_images_file="$2"
      shift 2
      ;;
    --container-tool)
      [[ $# -ge 2 ]] || bundle_die "--container-tool requires a value"
      container_tool="$2"
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
case "$image_source" in
  build | pull | local) ;;
  *) bundle_die "--image-source must be build, pull, or local" ;;
esac
case "$container_tool" in
  docker | podman | nerdctl) ;;
  *) bundle_die "--container-tool must be docker, podman, or nerdctl" ;;
esac

if [[ -z "$agent_image" ]]; then
  agent_image="docker.io/library/infernex-agent:${version}"
fi
[[ "$agent_image" =~ ^[A-Za-z0-9._:/-]+:[A-Za-z0-9._-]+$ ]] ||
  bundle_die "--agent-image must be a tag-based image reference"

if [[ -n "$image_archive" ]]; then
  [[ -f "$image_archive" ]] ||
    bundle_die "image archive does not exist: ${image_archive}"
fi
if [[ -n "$extra_images_file" ]]; then
  [[ -f "$extra_images_file" ]] ||
    bundle_die "extra image list does not exist: ${extra_images_file}"
fi

bundle_require_command "$helm_bin"
bundle_require_command sha256sum
bundle_require_command tar
bundle_require_command awk
if [[ -z "$image_archive" ]]; then
  bundle_require_command "$container_tool"
fi

mkdir -p -- "$output_dir"
output_dir="$(cd -- "$output_dir" && pwd)"
bundle_name="infernex-agent-offline-${version}-linux-${architecture}"
archive_name="${bundle_name}.tar.gz"
archive_output="${output_dir}/${archive_name}"
archive_checksum="${archive_output}.sha256"

if [[ "$force" != "true" && ( -e "$archive_output" || -e "$archive_checksum" ) ]]; then
  bundle_die "${archive_name} already exists; pass --force to replace it"
fi

work_dir="$(mktemp -d "${TMPDIR:-/tmp}/infernex-agent-bundle.XXXXXX")"
cleanup() {
  local resolved
  resolved="$(cd -- "$work_dir" 2>/dev/null && pwd || true)"
  if [[ -n "$resolved" && "$resolved" == "${TMPDIR:-/tmp}"/infernex-agent-bundle.* ]]; then
    rm -rf -- "$resolved"
  fi
}
trap cleanup EXIT

bundle_root="${work_dir}/${bundle_name}"
mkdir -p \
  "${bundle_root}/bin" \
  "${bundle_root}/charts" \
  "${bundle_root}/docs" \
  "${bundle_root}/images" \
  "${bundle_root}/values"

declare -a images=("$agent_image")
if [[ -n "$extra_images_file" ]]; then
  while IFS= read -r image_ref || [[ -n "$image_ref" ]]; do
    image_ref="${image_ref%%#*}"
    image_ref="${image_ref#"${image_ref%%[![:space:]]*}"}"
    image_ref="${image_ref%"${image_ref##*[![:space:]]}"}"
    [[ -z "$image_ref" ]] && continue
    [[ "$image_ref" =~ ^[A-Za-z0-9._:/@-]+$ ]] ||
      bundle_die "invalid image reference in ${extra_images_file}: ${image_ref}"
    if [[ " ${images[*]} " != *" ${image_ref} "* ]]; then
      images+=("$image_ref")
    fi
  done <"$extra_images_file"
fi

image_archive_relative="images/infernex-agent-images-${architecture}.tar"
image_archive_target="${bundle_root}/${image_archive_relative}"

if [[ -n "$image_archive" ]]; then
  bundle_info "copying supplied image archive"
  cp -- "$image_archive" "$image_archive_target"
else
  if [[ "$image_source" == "build" ]]; then
    bundle_info "building ${agent_image} for linux/${architecture}"
    "$container_tool" build \
      --platform "linux/${architecture}" \
      --build-arg "VERSION=${version}" \
      --file "${agent_dir}/Dockerfile" \
      --tag "$agent_image" \
      "$component_dir"
  elif [[ "$image_source" == "pull" ]]; then
    bundle_info "pulling ${agent_image}"
    "$container_tool" pull "$agent_image"
  else
    bundle_info "using local image ${agent_image}"
  fi

  if ((${#images[@]} > 1)); then
    for image_ref in "${images[@]:1}"; do
      bundle_info "pulling additional image ${image_ref}"
      "$container_tool" pull "$image_ref"
    done
  fi

  bundle_info "exporting ${#images[@]} image(s)"
  "$container_tool" save --output "$image_archive_target" "${images[@]}"
fi

printf '%s\n' "${images[@]}" >"${bundle_root}/image-references.txt"

bundle_info "packaging Helm chart"
"$helm_bin" package "${agent_dir}/chart/infernex-agent" \
  --destination "${bundle_root}/charts" \
  --version "$version" \
  --app-version "$version" \
  >/dev/null

chart_relative="charts/infernex-agent-${version}.tgz"
[[ -f "${bundle_root}/${chart_relative}" ]] ||
  bundle_die "Helm did not produce ${chart_relative}"

install -m 0755 \
  "${script_dir}/bundle-lib.sh" \
  "${script_dir}/load-images.sh" \
  "${script_dir}/install-agent.sh" \
  "${script_dir}/verify-agent.sh" \
  "${bundle_root}/bin/"
install -m 0644 \
  "${agent_dir}/offline/values-existing-cluster.yaml" \
  "${bundle_root}/values/"
install -m 0644 \
  "${agent_dir}/docs/offline-install-zh.md" \
  "${bundle_root}/README.md"
install -m 0644 \
  "${agent_dir}/docs/product-guide-zh.md" \
  "${agent_dir}/docs/product-design-zh.md" \
  "${agent_dir}/docs/model-configuration-zh.md" \
  "${agent_dir}/docs/security-boundaries-zh.md" \
  "${agent_dir}/docs/operations-runbook-zh.md" \
  "${agent_dir}/docs/change-safety-zh.md" \
  "${agent_dir}/docs/progressive-experiments-zh.md" \
  "${bundle_root}/docs/"
install -m 0644 "${repo_root}/LICENSE" "${bundle_root}/LICENSE"

created_utc="$(date -u +'%Y-%m-%dT%H:%M:%SZ')"
cat >"${bundle_root}/bundle.properties" <<EOF
format=infernex-agent-offline-v1
agent_version=${version}
architecture=${architecture}
agent_image=${agent_image}
chart=${chart_relative}
image_archive=${image_archive_relative}
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

bundle_info "bundle created: ${archive_output}"
bundle_info "outer checksum: ${archive_checksum}"
