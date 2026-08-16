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
Configure optional OpenAI-compatible analysis and interactive chat.

Usage:
  sudo configure-model.sh [options]

Actions:
  --interactive           Prompt only for the Agent model interface
  --base-url URL          Set or replace the OpenAI-compatible base URL
  --model MODEL           Set or replace the diagnostic model
  --api-key-file FILE     Install or rotate the protected API key
  --clear-api-key         Remove the installed API key
  --timeout DURATION      Per-attempt timeout, for example 3m or 300s
  --context-window-tokens N
                         Model context window (default: 32768)
  --max-output-tokens N  Output reservation and per-call maximum
  --context-compaction-threshold PERCENT
                         Compact at this context usage (default: 80)
  --context-keep-recent-turns N
                         Recent user turns kept verbatim (default: 4)
  --tool-result-max-tokens N
                         Approximate cap for one tool result
  --disable               Disable model analysis and remove its API key
  --test                  Send a small chat-completions request before applying
  --test-tools            Verify harmless auto tool calling used by the terminal
  --show                  Print effective non-secret model configuration

Control:
  --no-restart            Write configuration without restarting the service
  -h, --help              Show this help

The model is optional. Without it, deterministic collection, issue
classification, MCP, the snapshot API, and the dashboard continue to work.
Configuration is stored in /etc/infernex-agent/agent.conf. The API key is
stored separately as /etc/infernex-agent/openai-api-key and is never printed.
Interactive chat additionally requires OpenAI-compatible function/tool calling.
EOF
}

service_name="infernex-agent.service"
config_file="/etc/infernex-agent/agent.conf"
credential_file="/etc/infernex-agent/openai-api-key"
service_user="infernex-agent"

base_url=""
model=""
request_timeout=""
context_window_tokens=""
max_output_tokens=""
context_compaction_threshold=""
context_keep_recent_turns=""
tool_result_max_tokens=""
api_key_source=""
base_url_set="false"
model_set="false"
timeout_set="false"
context_window_set="false"
max_output_set="false"
context_threshold_set="false"
keep_recent_set="false"
tool_result_max_set="false"
api_key_set="false"
clear_api_key="false"
disable_model="false"
test_model="false"
test_tools="false"
show_model="false"
restart_service="true"
interactive="false"
interactive_key_file=""

while (($#)); do
  case "$1" in
    --interactive)
      interactive="true"
      shift
      ;;
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
    --context-window-tokens)
      [[ $# -ge 2 ]] || bundle_die "--context-window-tokens requires a value"
      context_window_tokens="$2"
      context_window_set="true"
      shift 2
      ;;
    --max-output-tokens)
      [[ $# -ge 2 ]] || bundle_die "--max-output-tokens requires a value"
      max_output_tokens="$2"
      max_output_set="true"
      shift 2
      ;;
    --context-compaction-threshold)
      [[ $# -ge 2 ]] || bundle_die "--context-compaction-threshold requires a value"
      context_compaction_threshold="$2"
      context_threshold_set="true"
      shift 2
      ;;
    --context-keep-recent-turns)
      [[ $# -ge 2 ]] || bundle_die "--context-keep-recent-turns requires a value"
      context_keep_recent_turns="$2"
      keep_recent_set="true"
      shift 2
      ;;
    --tool-result-max-tokens)
      [[ $# -ge 2 ]] || bundle_die "--tool-result-max-tokens requires a value"
      tool_result_max_tokens="$2"
      tool_result_max_set="true"
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
    --test-tools)
      test_model="true"
      test_tools="true"
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

cleanup_interactive_key() {
  [[ -z "$interactive_key_file" ]] || rm -f -- "$interactive_key_file"
}
trap cleanup_interactive_key EXIT

if [[ "$interactive" == "true" ]]; then
  [[ "$disable_model" == "false" && "$show_model" == "false" ]] ||
    bundle_die "--interactive cannot be combined with --disable or --show"
  printf 'OpenAI 兼容接口地址（例如以 /v1 结尾；请填写真实地址）: '
  IFS= read -r base_url
  base_url="${base_url%/}"
  [[ -n "$base_url" ]] || bundle_die "model interface URL cannot be empty"
  printf '接口中的真实模型名（不是示例名称）: '
  IFS= read -r model
  [[ -n "$model" ]] || bundle_die "model name cannot be empty"
  printf 'API Key（无鉴权直接回车）: '
  IFS= read -r -s interactive_key
  printf '\n'
  base_url_set="true"
  model_set="true"
  test_model="true"
  test_tools="true"
  interactive_context_default="32768"
  if [[ "$context_window_set" == "true" ]]; then
    interactive_context_default="$context_window_tokens"
  elif [[ -r "$config_file" ]]; then
    while IFS= read -r existing_argument; do
      case "$existing_argument" in
        --context-window-tokens=*) interactive_context_default="${existing_argument#*=}" ;;
      esac
    done <"$config_file"
  fi
  printf '模型上下文窗口 token 数 [%s]: ' "$interactive_context_default"
  IFS= read -r context_window_tokens
  context_window_tokens="${context_window_tokens:-$interactive_context_default}"
  context_window_set="true"
  if [[ -n "$interactive_key" ]]; then
    interactive_key_file="$(mktemp /tmp/infernex-agent-model-key.XXXXXX)"
    chmod 0600 "$interactive_key_file"
    printf '%s\n' "$interactive_key" >"$interactive_key_file"
    unset interactive_key
    api_key_source="$interactive_key_file"
    api_key_set="true"
  fi
fi

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
current_timeout="3m"
current_context_window="32768"
current_max_output=""
current_context_threshold="80"
current_keep_recent="4"
current_tool_result_max=""
for argument in "${current_args[@]}"; do
  [[ -n "$argument" && "$argument" == --* ]] ||
    bundle_die "${config_file} contains an invalid argument"
  case "$argument" in
    --openai-base-url=*) current_base_url="${argument#*=}" ;;
    --openai-model=*) current_model="${argument#*=}" ;;
    --openai-timeout=*) current_timeout="${argument#*=}" ;;
    --context-window-tokens=*) current_context_window="${argument#*=}" ;;
    --max-output-tokens=*) current_max_output="${argument#*=}" ;;
    --context-compaction-threshold=*) current_context_threshold="${argument#*=}" ;;
    --context-keep-recent-turns=*) current_keep_recent="${argument#*=}" ;;
    --tool-result-max-tokens=*) current_tool_result_max="${argument#*=}" ;;
  esac
done
[[ -z "$current_base_url" && -z "$current_model" ||
  -n "$current_base_url" && -n "$current_model" ]] ||
  bundle_die "${config_file} contains incomplete model configuration"

candidate_base_url="$current_base_url"
candidate_model="$current_model"
candidate_timeout="$current_timeout"
candidate_context_window="$current_context_window"
candidate_max_output="$current_max_output"
candidate_context_threshold="$current_context_threshold"
candidate_keep_recent="$current_keep_recent"
candidate_tool_result_max="$current_tool_result_max"
[[ "$base_url_set" == "false" ]] || candidate_base_url="$base_url"
[[ "$model_set" == "false" ]] || candidate_model="$model"
[[ "$timeout_set" == "false" ]] || candidate_timeout="$request_timeout"
[[ "$context_window_set" == "false" ]] || candidate_context_window="$context_window_tokens"
[[ "$max_output_set" == "false" ]] || candidate_max_output="$max_output_tokens"
[[ "$context_threshold_set" == "false" ]] || candidate_context_threshold="$context_compaction_threshold"
[[ "$keep_recent_set" == "false" ]] || candidate_keep_recent="$context_keep_recent_turns"
[[ "$tool_result_max_set" == "false" ]] || candidate_tool_result_max="$tool_result_max_tokens"

# Recalculate safe derived defaults when an operator changes only the window.
[[ "$candidate_context_window" =~ ^[0-9]+$ ]] ||
  bundle_die "context window tokens must be a positive integer"
((candidate_context_window >= 2048 && candidate_context_window <= 4000000)) ||
  bundle_die "context window tokens must be between 2048 and 4000000"
if [[ "$context_window_set" == "true" && "$max_output_set" == "false" ]]; then
  candidate_max_output=$((candidate_context_window / 8))
  ((candidate_max_output <= 2048)) || candidate_max_output=2048
fi
if [[ "$context_window_set" == "true" && "$tool_result_max_set" == "false" ]]; then
  candidate_tool_result_max=$((candidate_context_window * 15 / 100))
  ((candidate_tool_result_max <= 4096)) || candidate_tool_result_max=4096
fi
if [[ -z "$candidate_max_output" ]]; then
  candidate_max_output=$((candidate_context_window / 8))
  ((candidate_max_output <= 2048)) || candidate_max_output=2048
fi
if [[ -z "$candidate_tool_result_max" ]]; then
  candidate_tool_result_max=$((candidate_context_window * 15 / 100))
  ((candidate_tool_result_max <= 4096)) || candidate_tool_result_max=4096
fi

modify_requested="false"
if [[ "$base_url_set" == "true" ||
  "$model_set" == "true" ||
  "$timeout_set" == "true" ||
  "$context_window_set" == "true" ||
  "$max_output_set" == "true" ||
  "$context_threshold_set" == "true" ||
  "$keep_recent_set" == "true" ||
  "$tool_result_max_set" == "true" ||
  "$api_key_set" == "true" ||
  "$clear_api_key" == "true" ||
  "$disable_model" == "true" ]]; then
  modify_requested="true"
fi

if [[ "$disable_model" == "true" ]]; then
  candidate_base_url=""
  candidate_model=""
  candidate_timeout="3m"
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

validate_context_config() {
  local window="$1" output="$2" threshold="$3" keep_recent="$4" tool_max="$5"
  for value in "$window" "$output" "$threshold" "$keep_recent" "$tool_max"; do
    [[ "$value" =~ ^[0-9]+$ ]] || bundle_die "context values must be positive integers"
  done
  ((window >= 2048 && window <= 4000000)) ||
    bundle_die "context window tokens must be between 2048 and 4000000"
  ((output >= 128 && output < window)) ||
    bundle_die "max output tokens must be at least 128 and smaller than the context window"
  ((threshold >= 50 && threshold <= 95)) ||
    bundle_die "context compaction threshold must be between 50 and 95 percent"
  ((keep_recent >= 1 && keep_recent <= 32)) ||
    bundle_die "recent turns to keep must be between 1 and 32"
  ((tool_max >= 128 && tool_max < window)) ||
    bundle_die "tool result token limit must be at least 128 and smaller than the context window"
  ((output < window * threshold / 100)) ||
    bundle_die "max output tokens must be smaller than the compaction threshold budget"
}
validate_context_config \
  "$candidate_context_window" "$candidate_max_output" \
  "$candidate_context_threshold" "$candidate_keep_recent" \
  "$candidate_tool_result_max"

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
  local test_tools="$4"
  local value_timeout="$5"
  local value_max_output_tokens="$6"
  local endpoint response_file header_file http_code escaped_model payload
  local request_timeout_seconds retry_max_seconds probe_attempt probe_attempts

  [[ -n "$value_base_url" && -n "$value_model" ]] ||
    bundle_die "model analysis is disabled; there is no endpoint to test"
  bundle_require_command curl

  response_file="$(mktemp "${TMPDIR:-/tmp}/infernex-model-response.XXXXXX")"
  header_file=""
  trap 'rm -f -- "$response_file"; [[ -z "$header_file" ]] || rm -f -- "$header_file"' EXIT

  escaped_model="${value_model//\\/\\\\}"
  escaped_model="${escaped_model//\"/\\\"}"
  if [[ "$test_tools" == "true" ]]; then
    payload="$(
      printf '{"model":"%s","messages":[{"role":"system","content":"You are testing OpenAI-compatible automatic tool calling. Follow the user request by calling the supplied tool and return no prose."},{"role":"user","content":"Use infernex_test_tool to inspect scope cluster now."}],"tools":[{"type":"function","function":{"name":"infernex_test_tool","description":"Harmless compatibility test that reports the requested scope","parameters":{"type":"object","properties":{"scope":{"type":"string","enum":["cluster"]}},"required":["scope"],"additionalProperties":false}}}],"tool_choice":"auto","temperature":0,"stream":false,"max_tokens":%s}' \
        "$escaped_model" "$value_max_output_tokens"
    )"
  else
    payload="$(
      printf '{"model":"%s","messages":[{"role":"user","content":"Reply with OK."}],"temperature":0,"stream":false,"max_tokens":8}' \
        "$escaped_model"
    )"
  fi

  declare -a curl_args=(
    --silent
    --show-error
    --fail
    --connect-timeout 15
    --retry 3
    --retry-delay 2
    --retry-connrefused
    --output "$response_file"
    --write-out '%{http_code}'
    --header 'Accept: application/json'
    --header 'Content-Type: application/json'
    --data-binary "$payload"
  )
  case "$value_timeout" in
    *ms)
      request_timeout_seconds=$((
        (${value_timeout%ms} + 999) / 1000
      ))
      ;;
    *s) request_timeout_seconds="${value_timeout%s}" ;;
    *m) request_timeout_seconds=$((${value_timeout%m} * 60)) ;;
    *h) request_timeout_seconds=$((${value_timeout%h} * 3600)) ;;
    *) bundle_die "unsupported model timeout: ${value_timeout}" ;;
  esac
  ((request_timeout_seconds >= 1)) || request_timeout_seconds=1
  retry_max_seconds=$((request_timeout_seconds * 4 + 10))
  curl_args+=(
    --max-time "$request_timeout_seconds"
    --retry-max-time "$retry_max_seconds"
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
  probe_attempts=1
  [[ "$test_tools" != "true" ]] || probe_attempts=3
  for ((probe_attempt = 1; probe_attempt <= probe_attempts; probe_attempt++)); do
    : >"$response_file"
    if ! http_code="$(curl "${curl_args[@]}" "$endpoint")"; then
      bundle_die "model endpoint request failed: ${endpoint}"
    fi
    [[ "$http_code" =~ ^2[0-9][0-9]$ ]] ||
      bundle_die "model endpoint returned HTTP ${http_code}"
    grep -Eq '"choices"[[:space:]]*:' "$response_file" ||
      bundle_die "model endpoint response does not contain choices"
    if [[ "$test_tools" != "true" ]] || {
      grep -Eq '"tool_calls"[[:space:]]*:[[:space:]]*\[' "$response_file" &&
        grep -Eq '"name"[[:space:]]*:[[:space:]]*"infernex_test_tool"' "$response_file"
    }; then
      break
    fi
    if ((probe_attempt < probe_attempts)); then
      bundle_warn "auto tool-call probe ${probe_attempt}/${probe_attempts} returned no message.tool_calls; retrying because model generation and parser output may be non-deterministic"
    fi
  done
  if [[ "$test_tools" == "true" ]]; then
    if ! grep -Eq '"tool_calls"[[:space:]]*:[[:space:]]*\[' "$response_file"; then
      bundle_warn "the endpoint accepted an auto-tools request but returned no message.tool_calls"
      if grep -Eq '"finish_reason"[[:space:]]*:[[:space:]]*"length"' "$response_file"; then
        bundle_warn "the compatibility response ended with finish_reason=length even with max_tokens=${value_max_output_tokens}"
      fi
      if grep -Eq '(<tool_call>|&lt;tool_call&gt;)' "$response_file"; then
        bundle_warn "raw tool-call markup was left in content/reasoning; the serving parser did not convert it to message.tool_calls"
      fi
      bundle_warn "bounded response follows (credentials and request headers are not included)"
      head -c 4096 "$response_file" >&2 || true
      printf '\n' >&2
      bundle_die "model endpoint did not return OpenAI-compatible message.tool_calls in ${probe_attempts} attempts using the same tool_choice=auto mode as infernex-agent; verify the live model ID, gateway passthrough, reasoning/chat template, --enable-auto-tool-choice, and the model-specific --tool-call-parser"
    fi
    grep -Eq '"name"[[:space:]]*:[[:space:]]*"infernex_test_tool"' "$response_file" ||
      bundle_die "model endpoint returned tool_calls in ${probe_attempts} attempts, but never called infernex_test_tool"
    grep -Eq 'scope.{0,32}cluster' "$response_file" ||
      bundle_warn "tool call was parsed, but its arguments did not contain the expected scope=cluster; runtime validation may reject malformed arguments"
  fi
  bundle_info "model endpoint test succeeded: ${endpoint}"
)

if [[ "$test_model" == "true" ]]; then
  test_endpoint \
    "$candidate_base_url" "$candidate_model" "$effective_key_file" "$test_tools" \
    "$candidate_timeout" "$candidate_max_output"
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
  printf 'context_window_tokens=%s\n' "$candidate_context_window"
  printf 'max_output_tokens=%s\n' "$candidate_max_output"
  printf 'context_compaction_threshold_percent=%s\n' "$candidate_context_threshold"
  printf 'context_keep_recent_turns=%s\n' "$candidate_keep_recent"
  printf 'tool_result_max_tokens=%s\n' "$candidate_tool_result_max"
  printf 'api_key=%s\n' "$credential"
}

if [[ "$modify_requested" == "true" ]]; then
  bundle_require_command systemctl
  declare -a updated_args=()
  for argument in "${current_args[@]}"; do
    case "$argument" in
      --openai-base-url=* | --openai-model=* | --openai-api-key-file=* | --openai-timeout=* | \
        --context-window-tokens=* | --max-output-tokens=* | \
        --context-compaction-threshold=* | --context-keep-recent-turns=* | \
        --tool-result-max-tokens=*)
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
  updated_args+=(
    "--context-window-tokens=${candidate_context_window}"
    "--max-output-tokens=${candidate_max_output}"
    "--context-compaction-threshold=${candidate_context_threshold}"
    "--context-keep-recent-turns=${candidate_keep_recent}"
    "--tool-result-max-tokens=${candidate_tool_result_max}"
  )

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

if [[ -n "$candidate_base_url" && -x /opt/infernex-agent/bin/chat.sh ]]; then
  if [[ -x /opt/infernex-agent/pi-runtime/pi && -x /opt/infernex-agent/bin/tui.sh ]]; then
    bundle_info "Agentic TUI: sudo infernex-agent chat"
    bundle_info "legacy line terminal: sudo infernex-agent chat --classic"
  else
    bundle_info "interactive terminal: sudo /opt/infernex-agent/bin/chat.sh"
  fi
fi

if [[ "$modify_requested" == "false" &&
  "$test_model" == "false" &&
  "$show_model" == "false" ]]; then
  usage >&2
  bundle_die "no action requested"
fi
