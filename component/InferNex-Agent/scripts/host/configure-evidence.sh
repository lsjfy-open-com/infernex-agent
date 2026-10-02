#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
if [[ -f "${script_dir}/bundle-lib.sh" ]]; then
  # shellcheck source=/dev/null
  source "${script_dir}/bundle-lib.sh"
else
  # shellcheck source=../offline/bundle-lib.sh
  source "${script_dir}/../offline/bundle-lib.sh"
fi

usage() {
  cat <<'EOF'
Configure host directories that InferNex Agent may inspect as historical evidence.

Usage:
  sudo configure-evidence.sh --add-root /absolute/log/directory
  sudo configure-evidence.sh --remove-root /absolute/log/directory
  sudo configure-evidence.sh --report-directory /absolute/report/directory
  sudo configure-evidence.sh --show

Evidence roots are read-only, must already exist, and must be readable by the
infernex-agent service user. The Agent never modifies source logs. Markdown
reports are created only below the protected report directory.
EOF
}

config_file="/etc/infernex-agent/agent.conf"
service_name="infernex-agent.service"
service_user="infernex-agent"
add_root=""
remove_root=""
report_directory=""
report_set="false"

while (($#)); do
  case "$1" in
    --add-root)
      [[ $# -ge 2 ]] || bundle_die "--add-root requires a value"
      add_root="$2"
      shift 2
      ;;
    --remove-root)
      [[ $# -ge 2 ]] || bundle_die "--remove-root requires a value"
      remove_root="$2"
      shift 2
      ;;
    --report-directory)
      [[ $# -ge 2 ]] || bundle_die "--report-directory requires a value"
      report_directory="$2"
      report_set="true"
      shift 2
      ;;
    --show)
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *) bundle_die "unknown option: $1" ;;
  esac
done

[[ ${EUID} -eq 0 ]] || bundle_die "configure-evidence.sh must run as root"
[[ -f "$config_file" ]] || bundle_die "Agent configuration not found: ${config_file}"
bundle_require_command systemctl
bundle_require_command readlink
bundle_require_command runuser
[[ -z "$add_root" || -z "$remove_root" ]] ||
  bundle_die "--add-root and --remove-root are mutually exclusive"

declare -a current_args=()
mapfile -t current_args <"$config_file"
declare -a roots=()
current_report="/var/lib/infernex-agent/reports"
for argument in "${current_args[@]}"; do
  case "$argument" in
    --evidence-roots=*) IFS=',' read -r -a roots <<<"${argument#*=}" ;;
    --report-directory=*) current_report="${argument#*=}" ;;
  esac
done

canonical_directory() {
  local value="$1" purpose="$2" resolved
  [[ "$value" == /* && "$value" != *','* ]] ||
    bundle_die "${purpose} must be an absolute path without commas"
  resolved="$(readlink -f -- "$value")"
  [[ -d "$resolved" ]] || bundle_die "${purpose} is not an existing directory: ${value}"
  printf '%s' "$resolved"
}

if [[ -n "$add_root" ]]; then
  add_root="$(canonical_directory "$add_root" "evidence root")"
  runuser -u "$service_user" -- test -r "$add_root" &&
    runuser -u "$service_user" -- test -x "$add_root" ||
    bundle_die "evidence root is not readable/traversable by ${service_user}: ${add_root}"
  found="false"
  for root in "${roots[@]}"; do
    [[ "$root" != "$add_root" ]] || found="true"
  done
  [[ "$found" == "true" ]] || roots+=("$add_root")
fi

if [[ -n "$remove_root" ]]; then
  remove_root="$(readlink -m -- "$remove_root")"
  declare -a retained=()
  for root in "${roots[@]}"; do
    [[ "$root" == "$remove_root" ]] || retained+=("$root")
  done
  roots=("${retained[@]}")
fi

if [[ "$report_set" == "true" ]]; then
  current_report="$(canonical_directory "$report_directory" "report directory")"
  runuser -u "$service_user" -- test -w "$current_report" &&
    runuser -u "$service_user" -- test -x "$current_report" ||
    bundle_die "report directory is not writable/traversable by ${service_user}: ${current_report}"
fi

show_configuration() {
  printf 'evidence_roots:\n'
  if ((${#roots[@]} == 0)); then
    printf '  /var/lib/infernex-agent/imports (default)\n'
  else
    printf '  %s\n' "${roots[@]}"
  fi
  printf 'report_directory:\n  %s\n' "$current_report"
}

modify="false"
[[ -z "$add_root" && -z "$remove_root" && "$report_set" == "false" ]] || modify="true"
if [[ "$modify" == "false" ]]; then
  show_configuration
  exit 0
fi

declare -a updated_args=()
for argument in "${current_args[@]}"; do
  case "$argument" in
    --evidence-roots=* | --report-directory=*) ;;
    *) updated_args+=("$argument") ;;
  esac
done
if ((${#roots[@]} > 0)); then
  roots_csv="$(IFS=,; printf '%s' "${roots[*]}")"
  updated_args+=("--evidence-roots=${roots_csv}")
fi
updated_args+=("--report-directory=${current_report}")

backup="$(mktemp /etc/infernex-agent/.agent.conf.evidence-backup.XXXXXX)"
temporary="$(mktemp /etc/infernex-agent/.agent.conf.evidence.XXXXXX)"
cleanup() { rm -f -- "$backup" "$temporary"; }
trap cleanup EXIT
cp --preserve=mode,ownership,timestamps -- "$config_file" "$backup"
printf '%s\n' "${updated_args[@]}" >"$temporary"
chmod --reference="$config_file" "$temporary"
chown --reference="$config_file" "$temporary"
mv -f -- "$temporary" "$config_file"
if ! systemctl restart "$service_name" || ! systemctl is-active --quiet "$service_name"; then
  cp --preserve=mode,ownership,timestamps -- "$backup" "$config_file"
  systemctl restart "$service_name" || true
  bundle_die "Agent restart failed; evidence configuration was rolled back"
fi
show_configuration
