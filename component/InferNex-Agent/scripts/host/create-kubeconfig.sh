#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
if [[ -f "${script_dir}/bundle-lib.sh" ]]; then
  # Extracted host bundle.
  # shellcheck source=/dev/null
  source "${script_dir}/bundle-lib.sh"
else
  # Source checkout.
  # shellcheck source=../offline/bundle-lib.sh
  source "${script_dir}/../offline/bundle-lib.sh"
fi

usage() {
  cat <<'EOF'
Create namespace-scoped RBAC and a dedicated kubeconfig for a host Agent.

Usage:
  create-kubeconfig.sh --target-namespace NAMESPACE [options]

Options:
  --target-namespace NAMESPACE     Namespace to observe (repeatable)
  --admin-kubeconfig FILE          Bootstrap kubeconfig; default kubectl rules
  --agent-namespace NAMESPACE      Credential namespace (default: infernex-system)
  --service-account NAME           ServiceAccount name (default: infernex-agent-host)
  --output FILE                    Output kubeconfig (default: ./infernex-agent-host.kubeconfig)
  --enable-deployment              Permit constrained catalog create/delete
  --enable-log-diagnostics         Permit InferNex-owned Pod log reads
  --enable-experiments             Permit candidates and approved profiles
  --experiment-template-namespace N Profile namespace (default: infernex-bridge-system)
  --enable-recovery                Permit recovery-service create and profile get
  --recovery-template-namespace N  Profile namespace (default: infernex-bridge-system)
  --rotate-token                   Replace the long-lived ServiceAccount token
  --force                          Replace an existing output file
  -h, --help                       Show this help

The output contains a long-lived, namespace-scoped token. Store it as a
credential (0600), rotate it under your organization's policy, and do not use
cluster-admin/admin.conf as the Agent runtime identity.
EOF
}

admin_kubeconfig=""
agent_namespace="infernex-system"
service_account="infernex-agent-host"
output_file="${PWD}/infernex-agent-host.kubeconfig"
enable_deployment="false"
enable_log_diagnostics="false"
enable_experiments="false"
experiment_template_namespace="infernex-bridge-system"
enable_recovery="false"
recovery_template_namespace="infernex-bridge-system"
rotate_token="false"
force="false"
declare -a target_namespaces=()

while (($#)); do
  case "$1" in
    --target-namespace)
      [[ $# -ge 2 ]] || bundle_die "--target-namespace requires a value"
      target_namespaces+=("$2")
      shift 2
      ;;
    --admin-kubeconfig)
      [[ $# -ge 2 ]] || bundle_die "--admin-kubeconfig requires a value"
      admin_kubeconfig="$2"
      shift 2
      ;;
    --agent-namespace)
      [[ $# -ge 2 ]] || bundle_die "--agent-namespace requires a value"
      agent_namespace="$2"
      shift 2
      ;;
    --service-account)
      [[ $# -ge 2 ]] || bundle_die "--service-account requires a value"
      service_account="$2"
      shift 2
      ;;
    --output)
      [[ $# -ge 2 ]] || bundle_die "--output requires a value"
      output_file="$2"
      shift 2
      ;;
    --enable-deployment)
      enable_deployment="true"
      shift
      ;;
    --enable-log-diagnostics)
      enable_log_diagnostics="true"
      shift
      ;;
    --enable-experiments)
      enable_experiments="true"
      enable_log_diagnostics="true"
      shift
      ;;
    --experiment-template-namespace)
      [[ $# -ge 2 ]] || bundle_die "--experiment-template-namespace requires a value"
      experiment_template_namespace="$2"
      shift 2
      ;;
    --enable-recovery)
      enable_recovery="true"
      shift
      ;;
    --recovery-template-namespace)
      [[ $# -ge 2 ]] || bundle_die "--recovery-template-namespace requires a value"
      recovery_template_namespace="$2"
      shift 2
      ;;
    --rotate-token)
      rotate_token="true"
      shift
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

bundle_require_command kubectl
bundle_require_command base64

validate_dns_label() {
  local label="$1"
  [[ ${#label} -le 63 && "$label" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]]
}

validate_dns_label "$agent_namespace" ||
  bundle_die "invalid Agent namespace: ${agent_namespace}"
validate_dns_label "$service_account" ||
  bundle_die "invalid ServiceAccount name: ${service_account}"
validate_dns_label "$recovery_template_namespace" ||
  bundle_die "invalid recovery template namespace: ${recovery_template_namespace}"
validate_dns_label "$experiment_template_namespace" ||
  bundle_die "invalid experiment template namespace: ${experiment_template_namespace}"
((${#target_namespaces[@]} > 0)) ||
  bundle_die "at least one --target-namespace is required"
for target_namespace in "${target_namespaces[@]}"; do
  validate_dns_label "$target_namespace" ||
    bundle_die "invalid target namespace: ${target_namespace}"
done
if [[ -e "$output_file" && "$force" != "true" ]]; then
  bundle_die "output already exists; pass --force to replace it: ${output_file}"
fi

kubectl_args=()
if [[ -n "$admin_kubeconfig" ]]; then
  [[ -r "$admin_kubeconfig" ]] ||
    bundle_die "admin kubeconfig is not readable: ${admin_kubeconfig}"
  kubectl_args+=(--kubeconfig "$admin_kubeconfig")
fi

kubectl "${kubectl_args[@]}" get crd \
  infernexservices.infernex.infernex.io >/dev/null ||
  bundle_die "InferNexService CRD is missing"
if [[ "$enable_recovery" == "true" || "$enable_experiments" == "true" ]]; then
  kubectl "${kubectl_args[@]}" get crd \
    infernexserviceconfigs.infernex.infernex.io >/dev/null ||
    bundle_die "InferNexServiceConfig CRD is required for recovery or experiments"
fi

bundle_info "creating dedicated ServiceAccount"
kubectl "${kubectl_args[@]}" create namespace "$agent_namespace" \
  --dry-run=client -o yaml | kubectl "${kubectl_args[@]}" apply -f - >/dev/null
kubectl "${kubectl_args[@]}" --namespace "$agent_namespace" \
  create serviceaccount "$service_account" --dry-run=client -o yaml |
  kubectl "${kubectl_args[@]}" apply -f - >/dev/null

for target_namespace in "${target_namespaces[@]}"; do
  kubectl "${kubectl_args[@]}" get namespace "$target_namespace" >/dev/null ||
    bundle_die "target namespace does not exist: ${target_namespace}"
  bundle_info "applying read-only RBAC in ${target_namespace}"
  cat <<EOF | kubectl "${kubectl_args[@]}" apply -f - >/dev/null
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: infernex-agent-host
  namespace: ${target_namespace}
  labels:
    app.kubernetes.io/name: infernex-agent
    app.kubernetes.io/managed-by: infernex-agent-host-bootstrap
rules:
  - apiGroups: ["infernex.infernex.io"]
    resources: ["infernexservices"]
    verbs: ["get", "list"]
  - apiGroups: ["apps"]
    resources: ["deployments", "daemonsets"]
    verbs: ["list"]
  - apiGroups: ["leaderworkerset.x-k8s.io"]
    resources: ["leaderworkersets"]
    verbs: ["list"]
  - apiGroups: [""]
    resources: ["pods", "events"]
    verbs: ["list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: infernex-agent-host
  namespace: ${target_namespace}
  labels:
    app.kubernetes.io/name: infernex-agent
    app.kubernetes.io/managed-by: infernex-agent-host-bootstrap
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: infernex-agent-host
subjects:
  - kind: ServiceAccount
    name: ${service_account}
    namespace: ${agent_namespace}
EOF

  if [[ "$enable_log_diagnostics" == "true" ]]; then
    bundle_info "applying bounded log-read RBAC in ${target_namespace}"
    cat <<EOF | kubectl "${kubectl_args[@]}" apply -f - >/dev/null
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: infernex-agent-host-logs
  namespace: ${target_namespace}
  labels:
    app.kubernetes.io/name: infernex-agent
    app.kubernetes.io/managed-by: infernex-agent-host-bootstrap
rules:
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: infernex-agent-host-logs
  namespace: ${target_namespace}
  labels:
    app.kubernetes.io/name: infernex-agent
    app.kubernetes.io/managed-by: infernex-agent-host-bootstrap
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: infernex-agent-host-logs
subjects:
  - kind: ServiceAccount
    name: ${service_account}
    namespace: ${agent_namespace}
EOF
  else
    kubectl "${kubectl_args[@]}" --namespace "$target_namespace" delete \
      role/infernex-agent-host-logs \
      rolebinding/infernex-agent-host-logs \
      --ignore-not-found >/dev/null
  fi

  if [[ "$enable_deployment" == "true" || "$enable_recovery" == "true" || "$enable_experiments" == "true" ]]; then
    mutation_verbs='["create"]'
    if [[ "$enable_deployment" == "true" || "$enable_experiments" == "true" ]]; then
      mutation_verbs='["create", "delete"]'
    fi
    bundle_info "applying constrained mutation RBAC in ${target_namespace}"
    cat <<EOF | kubectl "${kubectl_args[@]}" apply -f - >/dev/null
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: infernex-agent-host-mutation
  namespace: ${target_namespace}
  labels:
    app.kubernetes.io/name: infernex-agent
    app.kubernetes.io/managed-by: infernex-agent-host-bootstrap
rules:
  - apiGroups: ["infernex.infernex.io"]
    resources: ["infernexservices"]
    verbs: ${mutation_verbs}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: infernex-agent-host-mutation
  namespace: ${target_namespace}
  labels:
    app.kubernetes.io/name: infernex-agent
    app.kubernetes.io/managed-by: infernex-agent-host-bootstrap
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: infernex-agent-host-mutation
subjects:
  - kind: ServiceAccount
    name: ${service_account}
    namespace: ${agent_namespace}
EOF
  else
    kubectl "${kubectl_args[@]}" --namespace "$target_namespace" delete \
      role/infernex-agent-host-mutation \
      rolebinding/infernex-agent-host-mutation \
      --ignore-not-found >/dev/null
  fi
done

if [[ "$enable_recovery" == "true" ]]; then
  kubectl "${kubectl_args[@]}" get namespace "$recovery_template_namespace" >/dev/null ||
    bundle_die "recovery template namespace does not exist: ${recovery_template_namespace}"
  bundle_info "applying recovery-profile read permission"
  cat <<EOF | kubectl "${kubectl_args[@]}" apply -f - >/dev/null
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: infernex-agent-host-recovery-profiles
  namespace: ${recovery_template_namespace}
  labels:
    app.kubernetes.io/name: infernex-agent
    app.kubernetes.io/managed-by: infernex-agent-host-bootstrap
rules:
  - apiGroups: ["infernex.infernex.io"]
    resources: ["infernexserviceconfigs"]
    verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: infernex-agent-host-recovery-profiles
  namespace: ${recovery_template_namespace}
  labels:
    app.kubernetes.io/name: infernex-agent
    app.kubernetes.io/managed-by: infernex-agent-host-bootstrap
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: infernex-agent-host-recovery-profiles
subjects:
  - kind: ServiceAccount
    name: ${service_account}
    namespace: ${agent_namespace}
EOF
else
  kubectl "${kubectl_args[@]}" --namespace "$recovery_template_namespace" delete \
    role/infernex-agent-host-recovery-profiles \
    rolebinding/infernex-agent-host-recovery-profiles \
    --ignore-not-found >/dev/null 2>&1 || true
fi

if [[ "$enable_experiments" == "true" ]]; then
  kubectl "${kubectl_args[@]}" get namespace "$experiment_template_namespace" >/dev/null ||
    bundle_die "experiment template namespace does not exist: ${experiment_template_namespace}"
  bundle_info "applying experiment-profile read permission"
  cat <<EOF | kubectl "${kubectl_args[@]}" apply -f - >/dev/null
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: infernex-agent-host-experiment-profiles
  namespace: ${experiment_template_namespace}
  labels:
    app.kubernetes.io/name: infernex-agent
    app.kubernetes.io/managed-by: infernex-agent-host-bootstrap
rules:
  - apiGroups: ["infernex.infernex.io"]
    resources: ["infernexserviceconfigs"]
    verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: infernex-agent-host-experiment-profiles
  namespace: ${experiment_template_namespace}
  labels:
    app.kubernetes.io/name: infernex-agent
    app.kubernetes.io/managed-by: infernex-agent-host-bootstrap
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: infernex-agent-host-experiment-profiles
subjects:
  - kind: ServiceAccount
    name: ${service_account}
    namespace: ${agent_namespace}
EOF
else
  kubectl "${kubectl_args[@]}" --namespace "$experiment_template_namespace" delete \
    role/infernex-agent-host-experiment-profiles \
    rolebinding/infernex-agent-host-experiment-profiles \
    --ignore-not-found >/dev/null 2>&1 || true
fi

token_secret="${service_account}-token"
if [[ "$rotate_token" == "true" ]]; then
  kubectl "${kubectl_args[@]}" --namespace "$agent_namespace" \
    delete secret "$token_secret" --ignore-not-found >/dev/null
fi
cat <<EOF | kubectl "${kubectl_args[@]}" apply -f - >/dev/null
apiVersion: v1
kind: Secret
metadata:
  name: ${token_secret}
  namespace: ${agent_namespace}
  annotations:
    kubernetes.io/service-account.name: ${service_account}
  labels:
    app.kubernetes.io/name: infernex-agent
    app.kubernetes.io/managed-by: infernex-agent-host-bootstrap
type: kubernetes.io/service-account-token
EOF

token_data=""
for _ in $(seq 1 30); do
  token_data="$(
    kubectl "${kubectl_args[@]}" --namespace "$agent_namespace" \
      get secret "$token_secret" -o jsonpath='{.data.token}' 2>/dev/null || true
  )"
  [[ -n "$token_data" ]] && break
  sleep 1
done
[[ -n "$token_data" ]] ||
  bundle_die "ServiceAccount token controller did not populate ${token_secret}"

server="$(
  kubectl "${kubectl_args[@]}" config view --raw --minify --flatten \
    -o jsonpath='{.clusters[0].cluster.server}'
)"
ca_data="$(
  kubectl "${kubectl_args[@]}" config view --raw --minify --flatten \
    -o jsonpath='{.clusters[0].cluster.certificate-authority-data}'
)"
[[ "$server" =~ ^https://[^[:space:]]+$ ]] ||
  bundle_die "source kubeconfig must use a valid HTTPS Kubernetes API server"
[[ "$ca_data" =~ ^[A-Za-z0-9+/=]+$ ]] ||
  bundle_die "source kubeconfig must contain embedded certificate authority data"
token="$(printf '%s' "$token_data" | base64 --decode)"
[[ -n "$token" && "$token" != *[[:space:]]* ]] ||
  bundle_die "generated ServiceAccount token is invalid"

output_parent="$(dirname -- "$output_file")"
mkdir -p -- "$output_parent"
output_parent="$(cd -- "$output_parent" && pwd)"
output_file="${output_parent}/$(basename -- "$output_file")"
temporary_output="$(mktemp "${output_parent}/.infernex-agent-kubeconfig.XXXXXX")"
cleanup() {
  rm -f -- "$temporary_output"
}
trap cleanup EXIT
umask 077
cat >"$temporary_output" <<EOF
apiVersion: v1
kind: Config
clusters:
  - name: infernex-cluster
    cluster:
      certificate-authority-data: ${ca_data}
      server: ${server}
contexts:
  - name: infernex-agent
    context:
      cluster: infernex-cluster
      namespace: ${agent_namespace}
      user: infernex-agent
current-context: infernex-agent
users:
  - name: infernex-agent
    user:
      token: ${token}
EOF
chmod 0600 "$temporary_output"
mv -f -- "$temporary_output" "$output_file"
trap - EXIT

bundle_info "dedicated kubeconfig written to ${output_file}"
bundle_warn "this file contains a long-lived token; transfer and store it as a credential"
