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
eval "$(sed -n '/^report_kubeconfig_failure() {/,/^}/p' "$quick_script")"
eval "$(sed -n '/^die_for_kubeconfig_failure() {/,/^}/p' "$quick_script")"
eval "$(sed -n '/^known_admin_kubeconfigs() {/,/^}/p' "$quick_script")"
eval "$(sed -n '/^discover_kubeconfig() {/,/^}/p' "$quick_script")"
bundle_die() { printf 'ERROR: %s\n' "$*" >&2; exit 42; }
bundle_warn() { printf 'WARN: %s\n' "$*" >&2; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
assert_equal() {
  local expected="$1" actual="$2" label="$3"
  [[ "$actual" == "$expected" ]] || fail "$label: expected '$expected', got '$actual'"
}
assert_selected() {
  local expected
  expected="$(readlink -f -- "$1")"
  assert_equal "$expected" "$admin_kubeconfig" 'selected kubeconfig'
}
known_admin_kubeconfigs() {
  printf '%s\n' "${1}/.kube/config"
  [[ -z "${test_known_admin:-}" ]] || printf '%s\n' "$test_known_admin"
}
getent() {
  [[ "$1" == passwd && "$2" == operator ]] || return 1
  printf 'operator:x:1000:1000::%s:/bin/bash\n' "$test_sudo_home"
}
kubectl() {
  printf '%s\n' "$*" >>"$test_dir/kubectl-calls"
  if [[ "$1" == config ]]; then
    [[ "$2" == view && "${merge_mode:-fail}" == success &&
      "$KUBECONFIG" == "$merged_sources" ]] || return 1
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
  if result="$(discover_kubeconfig "$test_home" "$test_root_home" "$test_home_parent" 2>&1)"; then
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
  test_root_home="$test_dir/root-home"
  test_home_parent="$test_dir/home-parent"
  test_known_admin=""
  test_sudo_home="$test_dir/sudo-home"
  merge_mode=fail
  merged_sources=""
  admin_kubeconfig=""
  admin_kubeconfig_explicit=false
  discovery_kubeconfig=""
  kubeconfig_failure=""
  agent_config_path="$test_dir/agent/agent.conf"
  rm -rf -- "$test_dir/agent/kubeconfig"
  rm -f -- "$test_dir/agent/agent.conf" \
    "$test_dir/empty-home/.kube/config" \
    "$test_dir/custom-home/.kube/config" "$test_dir/sudo-home/.kube/config" \
    "$test_dir/root-home/.kube/config"
  : >"$test_dir/kubectl-calls"
}
mkdir -p "$test_dir/empty-home" "$test_dir/agent" "$test_dir/root-home/.kube"

run_discover() {
  if ! discover_kubeconfig "$test_home" "$test_root_home" "$test_home_parent"; then
    fail 'discovery unexpectedly failed'
  fi
}

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
merge_mode=success
run_discover
assert_equal merged-context "$(<"$admin_kubeconfig")" 'merged context content'
assert_selected "$discovery_kubeconfig"
assert_equal 1 "$(grep -c '^config view --raw --flatten --minify$' "$test_dir/kubectl-calls")" 'merge call count'
rm -f -- "$discovery_kubeconfig"

# If a merged KUBECONFIG cannot be built, retain alpha.15 compatibility by
# trying each listed file before conventional locations.
reset_case
printf 'invalid\n' >"$test_dir/one"
printf 'working\n' >"$test_dir/two"
merged_sources="$test_dir/one:$test_dir/two"
export KUBECONFIG="$merged_sources"
run_discover
assert_selected "$test_dir/two"
assert_equal '' "$discovery_kubeconfig" 'failed merge temporary path cleanup'

# Missing or invalid environment inputs warn and continue to a working host
# default instead of making no-argument installation fail.
reset_case
mkdir -p "$test_dir/custom-home/.kube"
printf 'working\n' >"$test_dir/custom-home/.kube/config"
test_home="$test_dir/custom-home"
KUBECONFIG="$test_dir/missing"
if ! run_discover 2>"$test_dir/env-warning"; then fail 'missing env fallback failed'; fi
assert_selected "$test_dir/custom-home/.kube/config"
grep -Fq 'merged KUBECONFIG is unavailable' "$test_dir/env-warning" ||
  fail 'missing KUBECONFIG did not produce a concise warning'
printf 'invalid\n' >"$test_dir/invalid-env"
KUBECONFIG="$test_dir/invalid-env"
if ! run_discover 2>"$test_dir/env-warning"; then fail 'invalid env fallback failed'; fi
assert_selected "$test_dir/custom-home/.kube/config"
grep -Fq 'invalid kubeconfig' "$test_dir/env-warning" ||
  fail 'invalid KUBECONFIG entry was not diagnosed'
unset KUBECONFIG

# The actual HOME works with arbitrary home directories. Under sudo, preserve
# alpha.15/16 behavior by checking the invoking user's passwd home first.
reset_case
mkdir -p "$test_dir/custom-home/.kube" "$test_dir/sudo-home/.kube"
printf 'working\n' >"$test_dir/custom-home/.kube/config"
printf 'working\n' >"$test_dir/sudo-home/.kube/config"
test_sudo_home="$test_dir/sudo-home"
SUDO_USER=operator
test_home="$test_dir/custom-home"
run_discover
assert_selected "$test_dir/sudo-home/.kube/config"
test_home=/root
run_discover
assert_selected "$test_dir/sudo-home/.kube/config"

# If passwd lookup is unavailable or empty, /home/SUDO_USER remains the
# compatibility fallback. The root home candidate is always checked even when
# the actual HOME differs; the second argument is the test-only path seam.
reset_case
SUDO_USER=unknown-operator
printf 'working\n' >"$test_dir/root-home/.kube/config"
test_home="$test_dir/custom-home"
if ! run_discover 2>"$test_dir/sudo-fallback-warning"; then
  fail 'root fallback after an unavailable sudo passwd entry failed'
fi
assert_selected "$test_dir/root-home/.kube/config"

# Assert the /home fallback itself without requiring privileged host writes.
reset_case
SUDO_USER=operator
test_sudo_home=""
test_home="$test_dir/custom-home"
mkdir -p "$test_home_parent/operator/.kube"
printf 'working\n' >"$test_home_parent/operator/.kube/config"
run_discover
assert_selected "$test_home_parent/operator/.kube/config"

# On upgrade the installed Agent path is checked before machine admin paths.
reset_case
printf '%s\n' "--kubeconfig=$test_dir/legacy" >"$agent_config_path"
printf 'working\n' >"$test_dir/legacy"
printf 'working\n' >"$test_dir/custom-home/.kube/config"
test_home="$test_dir/custom-home"
run_discover
assert_selected "$test_dir/legacy"
rm -f -- "$test_dir/legacy"
: >"$agent_config_path"
printf 'working\n' >"$test_dir/agent/kubeconfig"
run_discover
assert_selected "$test_dir/agent/kubeconfig"

# Automatic discovery skips every unusable category and selects the next
# working candidate. This explicitly guards against set -e false positives.
reset_case
mkdir -p "$test_dir/empty-home/.kube" "$test_dir/sudo-home/.kube"
printf '%s\n' "--kubeconfig=$test_dir/stale" >"$agent_config_path"
printf 'invalid\n' >"$test_dir/stale"
mkdir "$test_dir/agent/kubeconfig"
SUDO_USER=operator
test_sudo_home="$test_dir/sudo-home"
printf 'no-context\n' >"$test_dir/sudo-home/.kube/config"
printf 'offline\n' >"$test_dir/empty-home/.kube/config"
printf 'working\n' >"$test_dir/root-home/.kube/config"
if ! run_discover 2>"$test_dir/fallback-warnings"; then
  fail 'default discovery did not fall through unusable candidates'
fi
assert_selected "$test_dir/root-home/.kube/config"
grep -Fq 'invalid kubeconfig' "$test_dir/fallback-warnings" || fail 'invalid candidate warning missing'
grep -Fq 'not a readable file' "$test_dir/fallback-warnings" || fail 'unreadable candidate warning missing'
grep -Fq 'no usable current context' "$test_dir/fallback-warnings" || fail 'no-context candidate warning missing'
grep -Fq 'Kubernetes API is unavailable' "$test_dir/fallback-warnings" || fail 'offline candidate warning missing'

reset_case
assert_error 'no management kubeconfig file was found'

reset_case
mkdir -p "$test_dir/empty-home/.kube"
printf 'offline\n' >"$test_dir/empty-home/.kube/config"
assert_error 'management kubeconfig files were found, but none had a usable context and Kubernetes API'

# The online entrypoint must preserve caller flags when invoking the bundle.
grep -Fq '"${work_dir}/${bundle_name}/install.sh" "$@"' "$online_script" ||
  fail 'online installer does not forward arguments'
printf 'Host kubeconfig discovery and wrapper forwarding pass\n'
