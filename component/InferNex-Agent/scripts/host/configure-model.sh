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
Configure optional OpenAI-compatible analysis for a host InferNex Agent.

Usage:
  sudo configure-model.sh [options]

Actions:
  --base-url URL          Set or replace the OpenAI-compatible base URL
  --model MODEL           Set or replace the diagnostic model
  --api-key-file FILE     Install or rotate the protected API key
  --clear-api-key         Remove the installed API key
  --timeout DURATION      Set request timeout, for example 60s or 2m
  --disable               Disable model analysis and remove its API key
  --test                  Send a small chat-completions request before applying
  --show                  Print effective non-secret model configuration

Control:
  --no-restart            Write configuration without restarting the service
  -h, --help              Show this help

The model is optional. Without it, deterministic collection, issue
classification, MCP, the snapshot API, and the dashboard continue to work.
Configuration is stored in /etc/infernex-agent/agent.conf. The API key is
stored separately as /etc/infernex-agent/openai-api-key and is never printed.
EOF
}

service_name="infernex-agent.service"
config_file="/etc/infernex-agent/agent.conf"
credential_file="/etc/infernex-agent/openai-api-key"
service_user="infernex-agent"

base_url=""
model=""
request_timeout=""
api_key_source=""
base_url_set="false"
model_set="false"
timeout_set="false"
api_key_set="false"
clear_api_key="false"
disable_model="false"
test_model="false"
show_model="false"
restart_service="true"

while (($#)); do
  case "$1" in
    --base-url)
      [[ $# -ge 2 ]] || bundle_die "--base-url requires a value"
      base_url="$2"
      base_url_set="true"
      shift 2
      ;;
    --model)
      [[ $# -ge 2 ]] || bundle_die "--model requires a value"
      model="$2"
      model_set="true"
      shift 2
      ;;
    --api-key-file)
      [[ $# -ge 2 ]] || bundle_die "--api-key-file requires a value"
      api_key_source="$2"
      api_key_set="true"
      shift 2
      ;;
    --clear-api-key)
      clear_api_key="true"
      shift
      ;;
    --timeout)
      [[ $# -ge 2 ]] || bundle_die "--timeout requires a value"
      request_timeout="$2"
      timeout_set="true"
      shift 2
      ;;
    --disable)
      disable_model="true"
      shift
      ;;
    --test)
      test_model="true"
      shift
      ;;
    --show)
      show_model="true"
      shift
      ;;
    --no-restart)
      restart_service="false"
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

[[ ${EUID} -eq 0 ]] ||
  bundle_die "configure-model.sh must run as root"
[[ -r "$config_file" ]] ||
  bundle_die "${config_file} is missing; install the host Agent first"
id "$service_user" >/dev/null 2>&1 ||
  bundle_die "service user ${service_user} does not exist"
service_group="$(id -gn "$service_user")"

[[ "$disable_model" != "true" ||
  ( "$base_url_set" == "false" &&
    "$model_set" == "false" &&
    "$timeout_set" == "false" &&
    "$api_key_set" == "false" &&
    "$clear_api_key" == "false" ) ]] ||
  bundle_die "--disable cannot be combined with model or API-key changes"
[[ "$api_key_set" != "true" || "$clear_api_key" != "true" ]] ||
  bundle_die "--api-key-file and --clear-api-key are mutually exclusive"
[[ "$disable_model" != "true" || "$test_model" != "true" ]] ||
  bundle_die "--disable and --test are mutually exclusive"

declare -a current_args=()
mapfile -t current_args <"$config_file"
((${#current_args[@]} > 0)) ||
  bundle_die "${config_file} contains no Agent arguments"

current_base_url=""
current_model=""
current_timeout="60s"
for argument in "${current_args[@]}"; do
  [[ -n "$argument" && "$argument" == --* ]] ||
    bundle_die "${config_file} contains an invalid argument"
  case "$argument" in
    --openai-base-url=*) current_base_url="${argument#*=}" ;;
    --openai-model=*) current_model="${argument#*=}" ;;
    --openai-timeout=*) current_timeout="${argument#*=}" ;;
  esac
done
[[ -z "$current_base_url" && -z "$current_model" ||
  -n "$current_base_url" && -n "$current_model" ]] ||
  bundle_die "${config_file} contains incomplete model configuration"

candidate_base_url="$current_base_url"
candidate_model="$current_model"
candidate_timeout="$current_timeout"
[[ "$base_url_set" == "false" ]] || candidate_base_url="$base_url"
[[ "$model_set" == "false" ]] || candidate_model="$model"
[[ "$timeout_set" == "false" ]] || candidate_timeout="$request_timeout"

modify_requested="false"
if [[ "$base_url_set" == "true" ||
  "$model_set" == "true" ||
  "$timeout_set" == "true" ||
  "$api_key_set" == "true" ||
  "$clear_api_key" == "true" ||
  "$disable_model" == "true" ]]; then
  modify_requested="true"
fi

if [[ "$disable_model" == "true" ]]; then
  candidate_base_url=""
  candidate_model=""
  candidate_timeout="60s"
elif [[ "$modify_requested" == "true" ]]; then
  [[ -n "$candidate_base_url" && -n "$candidate_model" ]] ||
    bundle_die "an enabled model requires both --base-url and --model"
fi

validate_model_config() {
  local value_base_url="$1"
  local value_model="$2"
  local value_timeout="$3"

  [[ -z "$value_base_url" ||
    "$value_base_url" =~ ^https?://[^[:space:]@]+$ ]] ||
    bundle_die "base URL must be http(s), contain no credentials, and contain no spaces"
  [[ "$value_base_url" != *'?'* && "$value_base_url" != *'#'* ]] ||
    bundle_die "base URL must not contain a query string or fragment"
  [[ -z "$value_model" ||
    ( "$value_model" != *[[:space:]]* &&
      "$value_model" != *$'\r'* &&
      "$value_model" != *$'\n'* ) ]] ||
    bundle_die "model name must not contain whitespace or control characters"
  [[ "$value_timeout" =~ ^[1-9][0-9]*(ms|s|m|h)$ ]] ||
    bundle_die "timeout must be a positive duration such as 60s or 2m"
}
validate_model_config \
  "$candidate_base_url" \
  "$candidate_model" \
  "$candidate_timeout"

validate_api_key_file() {
  local source_file="$1"
  local size
  [[ -f "$source_file" && -r "$source_file" ]] ||
    bundle_die "API key file is not a readable regular file"
  size="$(wc -c <"$source_file")"
  ((size > 0 && size <= 65536)) ||
    bundle_die "API key file must contain between 1 and 65536 bytes"
  if LC_ALL=C grep -q $'\r' "$source_file" ||
    ! awk 'NR > 1 { exit 1 }' "$source_file"; then
    bundle_die "API key file must contain exactly one text line"
  fi
}
if [[ "$api_key_set" == "true" ]]; then
  [[ -n "$candidate_base_url" ]] ||
    bundle_die "an API key requires an enabled model endpoint"
  validate_api_key_file "$api_key_source"
fi

effective_key_file=""
if [[ "$api_key_set" == "true" ]]; then
  effective_key_file="$api_key_source"
elif [[ "$clear_api_key" != "true" &&
  "$disable_model" != "true" &&
  -f "$credential_file" ]]; then
  effective_key_file="$credential_file"
fi

chat_completions_endpoint() {
  local endpoint="${1%/}"
  case "$endpoint" in
    */chat/completions) ;;
    */v1) endpoint="${endpoint}/chat/completions" ;;
    *) endpoint="${endpoint}/v1/chat/completions" ;;
  esac
  printf '%s' "$endpoint"
}

test_endpoint() (
  local value_base_url="$1"
  local value_model="$2"
  local key_file="$3"
  local endpoint response_file header_file http_code escaped_model payload

  [[ -n "$value_base_url" && -n "$value_model" ]] ||
    bundle_die "model analysis is disabled; there is no endpoint to test"
  bundle_require_command curl

  response_file="$(mktemp "${TMPDIR:-/tmp}/infernex-model-response.XXXXXX")"
  header_file=""
  trap 'rm -f -- "$response_file"; [[ -z "$header_file" ]] || rm -f -- "$header_file"' EXIT

  escaped_model="${value_model//\\/\\\\}"
  escaped_model="${escaped_model//\"/\\\"}"
  payload="$(
    printf '{"model":"%s","messages":[{"role":"user","content":"Reply with OK."}],"temperature":0,"stream":false,"max_tokens":8}' \
      "$escaped_model"
  )"

  declare -a curl_args=(
    --silent
    --show-error
    --connect-timeout 5
    --max-time 60
    --output "$response_file"
    --write-out '%{http_code}'
    --header 'Accept: application/json'
    --header 'Content-Type: application/json'
    --data-binary "$payload"
  )
  if [[ -n "$key_file" ]]; then
    validate_api_key_file "$key_file"
    header_file="$(mktemp "${TMPDIR:-/tmp}/infernex-model-header.XXXXXX")"
    chmod 0600 "$header_file"
    {
      printf 'Authorization: Bearer '
      tr -d '\n' <"$key_file"
      printf '\n'
    } >"$header_file"
    curl_args+=(--header "@${header_file}")
  fi

  endpoint="$(chat_completions_endpoint "$value_base_url")"
  if ! http_code="$(curl "${curl_args[@]}" "$endpoint")"; then
    bundle_die "model endpoint request failed: ${endpoint}"
  fi
  [[ "$http_code" =~ ^2[0-9][0-9]$ ]] ||
    bundle_die "model endpoint returned HTTP ${http_code}"
  grep -Eq '"choices"[[:space:]]*:' "$response_file" ||
    bundle_die "model endpoint response does not contain choices"
  bundle_info "model endpoint test succeeded: ${endpoint}"
)

if [[ "$test_model" == "true" ]]; then
  test_endpoint "$candidate_base_url" "$candidate_model" "$effective_key_file"
fi

show_configuration() {
  local status="disabled"
  local credential="not configured"
  [[ -z "$candidate_base_url" ]] || status="enabled"
  [[ ! -f "$credential_file" ]] || credential="configured (value hidden)"
  if [[ "$api_key_set" == "true" ]]; then
    credential="configured (pending value hidden)"
  elif [[ "$clear_api_key" == "true" || "$disable_model" == "true" ]]; then
    credential="not configured"
  fi
  printf 'model_analysis=%s\n' "$status"
  printf 'base_url=%s\n' "${candidate_base_url:--}"
  printf 'model=%s\n' "${candidate_model:--}"
  printf 'timeout=%s\n' "$candidate_timeout"
  printf 'api_key=%s\n' "$credential"
}

if [[ "$modify_requested" == "true" ]]; then
  bundle_require_command systemctl
  declare -a updated_args=()
  for argument in "${current_args[@]}"; do
    case "$argument" in
      --openai-base-url=* | --openai-model=* | --openai-api-key-file=* | --openai-timeout=*)
        ;;
      *) updated_args+=("$argument") ;;
    esac
  done
  if [[ -n "$candidate_base_url" ]]; then
    updated_args+=(
      "--openai-base-url=${candidate_base_url}"
      "--openai-model=${candidate_model}"
      "--openai-timeout=${candidate_timeout}"
    )
    if [[ "$clear_api_key" != "true" &&
      ( "$api_key_set" == "true" || -f "$credential_file" ) ]]; then
      updated_args+=("--openai-api-key-file=${credential_file}")
    fi
  fi

  config_backup="$(mktemp /etc/infernex-agent/.agent.conf.backup.XXXXXX)"
  cp --preserve=mode,ownership,timestamps -- "$config_file" "$config_backup"
  credential_backup=""
  credential_existed="false"
  if [[ -f "$credential_file" ]]; then
    credential_existed="true"
    credential_backup="$(
      mktemp /etc/infernex-agent/.openai-api-key.backup.XXXXXX
    )"
    cp --preserve=mode,ownership,timestamps \
      -- "$credential_file" "$credential_backup"
  fi

  temporary_config="$(mktemp /etc/infernex-agent/.agent.conf.XXXXXX)"
  printf '%s\n' "${updated_args[@]}" >"$temporary_config"
  chmod 0640 "$temporary_config"
  chown root:"$service_group" "$temporary_config"

  if [[ "$api_key_set" == "true" ]]; then
    temporary_credential="$(
      mktemp /etc/infernex-agent/.openai-api-key.XXXXXX
    )"
    install -m 0600 -o "$service_user" -g "$service_group" \
      "$api_key_source" "$temporary_credential"
    mv -f -- "$temporary_credential" "$credential_file"
  elif [[ "$clear_api_key" == "true" || "$disable_model" == "true" ]]; then
    rm -f -- "$credential_file"
  fi
  mv -f -- "$temporary_config" "$config_file"

  if [[ "$restart_service" == "true" ]]; then
    bundle_info "restarting ${service_name}"
    systemctl reset-failed "$service_name" >/dev/null 2>&1 || true
    if ! systemctl restart "$service_name" ||
      ! sleep 1 ||
      ! systemctl is-active --quiet "$service_name"; then
      bundle_warn "new model configuration failed; restoring the previous configuration"
      mv -f -- "$config_backup" "$config_file"
      if [[ "$credential_existed" == "true" ]]; then
        mv -f -- "$credential_backup" "$credential_file"
      else
        rm -f -- "$credential_file"
      fi
      systemctl reset-failed "$service_name" >/dev/null 2>&1 || true
      systemctl restart "$service_name" || true
      journalctl -u "$service_name" --no-pager -n 100 >&2 || true
      bundle_die "failed to activate model configuration; previous configuration restored"
    fi
  else
    bundle_warn "configuration written; restart ${service_name} before it takes effect"
  fi

  rm -f -- "$config_backup"
  [[ -z "$credential_backup" ]] || rm -f -- "$credential_backup"
  bundle_info "model configuration updated"
fi

if [[ "$show_model" == "true" || "$modify_requested" == "true" ]]; then
  show_configuration
fi

if [[ "$modify_requested" == "false" &&
  "$test_model" == "false" &&
  "$show_model" == "false" ]]; then
  usage >&2
  bundle_die "no action requested"
fi
