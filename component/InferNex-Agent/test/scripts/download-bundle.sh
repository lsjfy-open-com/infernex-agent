#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
agent_dir="$(cd -- "${script_dir}/../.." && pwd)"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/infernex-download-test.XXXXXX")"
cleanup() {
  case "$work_dir" in
    "${TMPDIR:-/tmp}"/infernex-download-test.*) rm -rf -- "$work_dir" ;;
  esac
}
trap cleanup EXIT

version="0.3.0-rc.6"
bundle_name="infernex-agent-${version}-linux-amd64"
archive_name="${bundle_name}.tar.gz"
mkdir -p \
  "${work_dir}/fixtures/${bundle_name}" \
  "${work_dir}/bin" \
  "${work_dir}/download"
printf 'verified fixture\n' >"${work_dir}/fixtures/${bundle_name}/marker"
tar -C "${work_dir}/fixtures" -czf \
  "${work_dir}/fixtures/${archive_name}" "$bundle_name"
(
  cd -- "${work_dir}/fixtures"
  sha256sum "$archive_name"
) >"${work_dir}/fixtures/${archive_name}.sha256"

cat >"${work_dir}/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
target=""
url=""
while (($#)); do
  case "$1" in
    --output)
      target="$2"
      shift 2
      ;;
    http://* | https://*)
      url="$1"
      shift
      ;;
    *) shift ;;
  esac
done
[[ -n "$target" && -n "$url" ]]
cp -- "${FIXTURE_DIR}/${url##*/}" "$target"
EOF
chmod 0755 "${work_dir}/bin/curl"

PATH="${work_dir}/bin:${PATH}" FIXTURE_DIR="${work_dir}/fixtures" \
  bash "${agent_dir}/scripts/download-bundle.sh" \
  --version "$version" \
  --architecture amd64 \
  --output-dir "${work_dir}/download"

[[ -f "${work_dir}/download/${bundle_name}/marker" ]]
grep -q '^verified fixture$' \
  "${work_dir}/download/${bundle_name}/marker"

if bash "${agent_dir}/scripts/download-bundle.sh" \
  --output-dir "${work_dir}/download" >/dev/null 2>&1; then
  printf 'missing version unexpectedly succeeded\n' >&2
  exit 1
fi

printf 'download-bundle tests passed\n'
