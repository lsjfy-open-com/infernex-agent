#!/usr/bin/env bash
set -euo pipefail

repository="lsjfy-open-com/infernex-agent"

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

[[ ${EUID} -eq 0 ]] || die "run with sudo: curl -fsSL <installer-url> | sudo bash"
for command_name in curl sha256sum tar awk grep; do
  command -v "$command_name" >/dev/null 2>&1 ||
    die "required command not found: ${command_name}"
done

case "$(uname -m)" in
  x86_64 | amd64) architecture="amd64" ;;
  aarch64 | arm64) architecture="arm64" ;;
  *) die "unsupported host architecture: $(uname -m)" ;;
esac

version="${INFERNEX_AGENT_VERSION:-}"
if [[ -n "$version" ]]; then
  [[ "$version" =~ ^[0-9A-Za-z][0-9A-Za-z.+-]*$ ]] || die "invalid INFERNEX_AGENT_VERSION"
  asset="infernex-agent-${version}-linux-${architecture}.tar.gz"
  asset_url="https://github.com/${repository}/releases/download/infernex-agent-v${version}/${asset}"
else
  releases="$(curl --fail --location --silent --show-error \
    --retry 3 "https://api.github.com/repos/${repository}/releases?per_page=20")"
  asset_url="$(
    printf '%s' "$releases" |
      grep -o "https://github.com/${repository}/releases/download/[^\"]*/infernex-agent-[0-9][^\"]*-linux-${architecture}\.tar\.gz" |
      awk 'NR == 1 {print; exit}'
  )"
  [[ -n "$asset_url" ]] || die "no published InferNex Agent package for linux/${architecture} was found"
  asset="${asset_url##*/}"
fi

work_dir="$(mktemp -d /tmp/infernex-agent-online-install.XXXXXX)"
cleanup() {
  local resolved
  resolved="$(readlink -f -- "$work_dir" 2>/dev/null || true)"
  case "$resolved" in
    /tmp/infernex-agent-online-install.*) rm -rf -- "$resolved" ;;
  esac
}
trap cleanup EXIT

printf '==> downloading %s\n' "$asset"
curl --fail --location --silent --show-error --retry 3 \
  --output "${work_dir}/${asset}" "$asset_url"
curl --fail --location --silent --show-error --retry 3 \
  --output "${work_dir}/${asset}.sha256" "${asset_url}.sha256"
(
  cd -- "$work_dir"
  sha256sum --check "${asset}.sha256"
)

bundle_name="${asset%.tar.gz}"
while IFS= read -r entry; do
  case "$entry" in
    "$bundle_name" | "$bundle_name"/*) ;;
    *) die "bundle contains an unexpected path: ${entry}" ;;
  esac
  case "/${entry}/" in
    */../*) die "bundle contains a parent-directory path" ;;
  esac
done < <(tar -tzf "${work_dir}/${asset}")
tar -C "$work_dir" --no-same-owner --no-same-permissions -xzf "${work_dir}/${asset}"
[[ -x "${work_dir}/${bundle_name}/install.sh" ]] || die "published bundle lacks the one-command installer"

"${work_dir}/${bundle_name}/install.sh"
