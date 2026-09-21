#!/usr/bin/env bash
set -euo pipefail

agent_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
quick_script="${agent_dir}/scripts/host/quick-install.sh"
online_script="${agent_dir}/scripts/install.sh"
test_dir="$(mktemp -d /tmp/infernex-kubeconfig-test.XXXXXX)"
trap 'rm -rf -- "$test_dir"' EXIT

# Exercise the production discovery functions, with only kubectl and passwd
# lookups stubbed. No API server or privileged host paths are needed.
eval "$(sed -n '/^check_kubeconfig() {/,/^}/p' "$quick_script")"
eval "$(sed -n '/^known_admin_kubeconfigs() {/,/^}/p' "$quick_script")"
eval "$(sed -n '/^discover_kubeconfig() {/,/^}/p' "$quick_script")"
bundle_die() { printf 'ERROR: %s\n' "$*" >&2; exit 42; }
known_admin_kubeconfigs() { :; }
getent() {
  [[ "$1" == passwd && "$2" == operator ]] || return 1
  printf 'operator:x:1000:1000::%s:/bin/bash\n' "$test_sudo_home"
}
kubectl() {
  printf '%s\n' "$*" >>"$test_dir/kubectl-calls"
  if [[ "$1" == config ]]; then
    [[ "$2" == view && "$KUBECONFIG" == "$merged_sources" ]] || return 1
    printf 'merged-context\n'
    return
  fi
  [[ "$1" == --kubeconfig ]] || return 1
  local source="$2"
  shift 2
  [[ -f "$source" ]] || return 1
  if [[ "$1" == config ]]; then
    case "$2" in
      view) [[ "$(<"$source")" != invalid ]] ;;
      current-context)
        [[ "$(<"$source")" != no-context ]] || return 1
        printf 'test-context\n'
        ;;
      *) return 1 ;;
    esac
  else
    [[ "$1" == --request-timeout=10s && "$2" == get && "$3" == --raw=/version ]] || return 1
    [[ "$(<"$source")" != offline ]]
  fi
}
assert_error() {
  local expected="$1" result
  if result="$(discover_kubeconfig "$test_home" 2>&1)"; then
    printf 'discovery unexpectedly succeeded: %s\n' "$result" >&2
    exit 1
  fi
  [[ "$result" == *"$expected"* ]] || {
    printf 'expected %s, got: %s\n' "$expected" "$result" >&2
    exit 1
  }
}
reset_case() {
  unset KUBECONFIG SUDO_USER
  test_home="$test_dir/empty-home"
  admin_kubeconfig=""
  admin_kubeconfig_explicit=false
  discovery_kubeconfig=""
  agent_config_path="$test_dir/agent/agent.conf"
  : >"$test_dir/kubectl-calls"
}
mkdir -p "$test_dir/empty-home" "$test_dir/agent"

# Explicit input must never silently select a valid fallback.
reset_case
printf 'working\n' >"$test_dir/agent/kubeconfig"
admin_kubeconfig_explicit=true
admin_kubeconfig="$test_dir/does-not-exist"
assert_error '--admin-kubeconfig does not exist'
admin_kubeconfig="$test_dir/directory"
mkdir "$admin_kubeconfig"
assert_error '--admin-kubeconfig is not a readable file'
printf 'invalid\n' >"$test_dir/invalid"
admin_kubeconfig="$test_dir/invalid"
assert_error '--admin-kubeconfig has an invalid kubeconfig'
printf 'no-context\n' >"$test_dir/no-context"
admin_kubeconfig="$test_dir/no-context"
assert_error '--admin-kubeconfig has no usable current context'
printf 'offline\n' >"$test_dir/offline"
admin_kubeconfig="$test_dir/offline"
assert_error '--admin-kubeconfig is present but cannot reach the Kubernetes API'

# kubectl combines KUBECONFIG inputs before selecting the current context.
reset_case
printf 'part-one\n' >"$test_dir/one"
printf 'part-two\n' >"$test_dir/two"
merged_sources="$test_dir/one:$test_dir/two"
export KUBECONFIG="$merged_sources"
discover_kubeconfig "$test_home"
[[ "$(<"$admin_kubeconfig")" == merged-context ]]
[[ "$discovery_kubeconfig" == "$admin_kubeconfig" ]]
[[ "$(grep -c '^config view --raw --flatten --minify$' "$test_dir/kubectl-calls")" == 1 ]]
rm -f -- "$discovery_kubeconfig"
KUBECONFIG="$test_dir/missing"
assert_error 'KUBECONFIG has no readable kubeconfig files'
unset KUBECONFIG

# The actual HOME works with arbitrary home directories and takes precedence
# when it is not root's HOME. sudo's passwd home precedes /root under sudo.
reset_case
mkdir -p "$test_dir/custom-home/.kube" "$test_dir/sudo-home/.kube"
printf 'working\n' >"$test_dir/custom-home/.kube/config"
printf 'working\n' >"$test_dir/sudo-home/.kube/config"
test_sudo_home="$test_dir/sudo-home"
SUDO_USER=operator
test_home="$test_dir/custom-home"
discover_kubeconfig "$test_home"
[[ "$admin_kubeconfig" == "$test_dir/custom-home/.kube/config" ]]
test_home=/root
discover_kubeconfig "$test_home"
[[ "$admin_kubeconfig" == "$test_dir/sudo-home/.kube/config" ]]

# On upgrade the installed Agent path is checked before machine admin paths.
reset_case
printf '%s\n' "--kubeconfig=$test_dir/legacy" >"$agent_config_path"
printf 'working\n' >"$test_dir/legacy"
test_home="$test_dir/custom-home"
discover_kubeconfig "$test_home"
[[ "$admin_kubeconfig" == "$test_dir/legacy" ]]
rm -f -- "$test_dir/legacy"
: >"$agent_config_path"
discover_kubeconfig "$test_home"
[[ "$admin_kubeconfig" == "$test_dir/agent/kubeconfig" ]]

# A present kubeconfig with bad API access is a different error from absence.
rm -f -- "$test_dir/agent/kubeconfig"
reset_case
mkdir -p "$test_dir/empty-home/.kube"
printf 'offline\n' >"$test_dir/empty-home/.kube/config"
assert_error 'discovered kubeconfig is present but cannot reach the Kubernetes API'
rm -f -- "$test_dir/empty-home/.kube/config" "$test_dir/agent/kubeconfig"
assert_error 'no management kubeconfig file was found'

# The online entrypoint must preserve caller flags when invoking the bundle.
grep -Fq '"${work_dir}/${bundle_name}/install.sh" "$@"' "$online_script"
printf 'Host kubeconfig discovery and wrapper forwarding pass\n'
