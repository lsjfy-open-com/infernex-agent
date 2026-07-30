#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=bundle-lib.sh
source "${script_dir}/bundle-lib.sh"

usage() {
  cat <<'EOF'
Install InferNex Agent into an existing, offline InferNex Kubernetes cluster.

Usage:
  install-agent.sh --target-namespace NAMESPACE --dashboard-cidr CIDR [options]

Required for the default NodePort dashboard:
  --target-namespace NAMESPACE    Namespace to observe (repeatable)
  --dashboard-cidr CIDR           Internal source CIDR (repeatable)

Options:
  --bundle-dir DIR                Extracted bundle root
  --namespace NAMESPACE           Agent namespace (default: infernex-system)
  --release NAME                  Helm release (default: infernex-agent)
  --target-node NODE              Pin the Agent to this control-plane node
  --dashboard-node-port PORT      NodePort (default: 30081)
  --dashboard-cluster-ip          Keep the dashboard as ClusterIP
  --agent-image REF               Use a mirrored internal-registry image
  --skip-image-import             Do not import the bundled image locally
  --runtime RUNTIME               Runtime passed to load-images.sh
  --containerd-address SOCKET     Optional containerd socket
  --openai-base-url URL           Internal OpenAI-compatible /v1 endpoint
  --openai-model MODEL            Model name sent to chat/completions
  --openai-api-key-file FILE      Read API key from a local file
  --openai-existing-secret NAME   Use an existing Secret with key "api-key"
  --enable-recovery               Enable guarded, double-opt-in recovery
  --recovery-template-namespace N InferNexServiceConfig namespace
  --values FILE                   Additional Helm values file (repeatable)
  --timeout DURATION              Helm/rollout timeout (default: 5m)
  --skip-checksums                Skip bundle content verification
  -h, --help                      Show this help

This installer never installs or modifies InferNex Bridge, NPU drivers,
firmware, model weights, gateways, or inference workloads.
EOF
}

bundle_root="$(bundle_default_root || true)"
namespace="infernex-system"
release_name="infernex-agent"
target_node=""
dashboard_type="NodePort"
dashboard_node_port="30081"
agent_image_override=""
skip_image_import="false"
runtime="auto"
containerd_address=""
openai_base_url=""
openai_model=""
openai_api_key_file=""
openai_existing_secret=""
enable_recovery="false"
recovery_template_namespace="infernex-bridge-system"
timeout="5m"
verify_checksums="true"
declare -a target_namespaces=()
declare -a dashboard_cidrs=()
declare -a extra_values=()

while (($#)); do
  case "$1" in
    --bundle-dir)
      [[ $# -ge 2 ]] || bundle_die "--bundle-dir requires a value"
      bundle_root="$2"
      shift 2
      ;;
    --namespace)
      [[ $# -ge 2 ]] || bundle_die "--namespace requires a value"
      namespace="$2"
      shift 2
      ;;
    --release)
      [[ $# -ge 2 ]] || bundle_die "--release requires a value"
      release_name="$2"
      shift 2
      ;;
    --target-namespace)
      [[ $# -ge 2 ]] || bundle_die "--target-namespace requires a value"
      target_namespaces+=("$2")
      shift 2
      ;;
    --target-node)
      [[ $# -ge 2 ]] || bundle_die "--target-node requires a value"
      target_node="$2"
      shift 2
      ;;
    --dashboard-cidr)
      [[ $# -ge 2 ]] || bundle_die "--dashboard-cidr requires a value"
      dashboard_cidrs+=("$2")
      shift 2
      ;;
    --dashboard-node-port)
      [[ $# -ge 2 ]] || bundle_die "--dashboard-node-port requires a value"
      dashboard_node_port="$2"
      shift 2
      ;;
    --dashboard-cluster-ip)
      dashboard_type="ClusterIP"
      shift
      ;;
    --agent-image)
      [[ $# -ge 2 ]] || bundle_die "--agent-image requires a value"
      agent_image_override="$2"
      shift 2
      ;;
    --skip-image-import)
      skip_image_import="true"
      shift
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
    --openai-base-url)
      [[ $# -ge 2 ]] || bundle_die "--openai-base-url requires a value"
      openai_base_url="$2"
      shift 2
      ;;
    --openai-model)
      [[ $# -ge 2 ]] || bundle_die "--openai-model requires a value"
      openai_model="$2"
      shift 2
      ;;
    --openai-api-key-file)
      [[ $# -ge 2 ]] || bundle_die "--openai-api-key-file requires a value"
      openai_api_key_file="$2"
      shift 2
      ;;
    --openai-existing-secret)
      [[ $# -ge 2 ]] || bundle_die "--openai-existing-secret requires a value"
      openai_existing_secret="$2"
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
    --values)
      [[ $# -ge 2 ]] || bundle_die "--values requires a value"
      extra_values+=("$2")
      shift 2
      ;;
    --timeout)
      [[ $# -ge 2 ]] || bundle_die "--timeout requires a value"
      timeout="$2"
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

bundle_require_command kubectl
bundle_require_command helm
bundle_require_command curl
if [[ "$verify_checksums" == "true" ]]; then
  bundle_verify_checksums "$bundle_root"
fi

[[ "$(bundle_property "$bundle_root" format)" == "infernex-agent-offline-v1" ]] ||
  bundle_die "unsupported bundle format"

[[ "$namespace" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] ||
  bundle_die "invalid Agent namespace: ${namespace}"
[[ "$release_name" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] ||
  bundle_die "invalid release name: ${release_name}"
[[ "$dashboard_node_port" =~ ^[0-9]+$ ]] &&
  ((dashboard_node_port >= 30000 && dashboard_node_port <= 32767)) ||
  bundle_die "dashboard NodePort must be between 30000 and 32767"

if ((${#target_namespaces[@]} == 0)); then
  mapfile -t target_namespaces < <(
    kubectl get infernexservices.infernex.infernex.io --all-namespaces \
      -o custom-columns=NAMESPACE:.metadata.namespace --no-headers 2>/dev/null |
      awk 'NF {print $1}' | LC_ALL=C sort -u
  )
fi
((${#target_namespaces[@]} > 0)) ||
  bundle_die "no InferNexService namespace was discovered; pass --target-namespace"
for target_namespace in "${target_namespaces[@]}"; do
  [[ "$target_namespace" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] ||
    bundle_die "invalid target namespace: ${target_namespace}"
  kubectl get namespace "$target_namespace" >/dev/null ||
    bundle_die "target namespace does not exist: ${target_namespace}"
done

if [[ "$dashboard_type" == "NodePort" && ${#dashboard_cidrs[@]} -eq 0 ]]; then
  bundle_die "NodePort exposure requires at least one --dashboard-cidr"
fi
for cidr in "${dashboard_cidrs[@]}"; do
  [[ "$cidr" =~ ^[0-9A-Fa-f:.]+/[0-9]{1,3}$ ]] ||
    bundle_die "invalid dashboard CIDR: ${cidr}"
done

if [[ -n "$openai_base_url" || -n "$openai_model" ]]; then
  [[ -n "$openai_base_url" && -n "$openai_model" ]] ||
    bundle_die "--openai-base-url and --openai-model must be provided together"
  [[ "$openai_base_url" =~ ^https?://[^[:space:]@]+(:[0-9]+)?(/[^[:space:]]*)?$ ]] ||
    bundle_die "OpenAI base URL must be http(s), contain no credentials, and contain no spaces"
fi
if [[ -n "$openai_api_key_file" && -n "$openai_existing_secret" ]]; then
  bundle_die "choose either --openai-api-key-file or --openai-existing-secret"
fi
if [[ -n "$openai_api_key_file" ]]; then
  [[ -f "$openai_api_key_file" && -r "$openai_api_key_file" ]] ||
    bundle_die "API key file is not readable: ${openai_api_key_file}"
  [[ -n "$openai_base_url" ]] ||
    bundle_die "--openai-api-key-file requires OpenAI endpoint configuration"
  openai_existing_secret="infernex-agent-openai"
fi
if [[ -n "$openai_existing_secret" && -z "$openai_base_url" ]]; then
  bundle_die "--openai-existing-secret requires OpenAI endpoint configuration"
fi

kubectl get crd infernexservices.infernex.infernex.io >/dev/null ||
  bundle_die "InferNexService CRD is missing; install InferNex/Bridge first"
if [[ "$enable_recovery" == "true" ]]; then
  kubectl get crd infernexserviceconfigs.infernex.infernex.io >/dev/null ||
    bundle_die "InferNexServiceConfig CRD is required for recovery"
  kubectl get namespace "$recovery_template_namespace" >/dev/null ||
    bundle_die "recovery template namespace does not exist: ${recovery_template_namespace}"
fi

if [[ "$skip_image_import" != "true" ]]; then
  load_args=(--bundle-dir "$bundle_root" --runtime "$runtime")
  [[ "$verify_checksums" == "true" ]] || load_args+=(--skip-checksums)
  if [[ -n "$containerd_address" ]]; then
    load_args+=(--containerd-address "$containerd_address")
  fi
  "${script_dir}/load-images.sh" "${load_args[@]}"

  if [[ -z "$target_node" ]]; then
    host_short="$(hostname -s)"
    if kubectl get node "$host_short" >/dev/null 2>&1; then
      target_node="$host_short"
    else
      mapfile -t control_plane_nodes < <(
        {
          kubectl get nodes \
            -l node-role.kubernetes.io/control-plane \
            -o custom-columns=NAME:.metadata.name --no-headers
          kubectl get nodes \
            -l node-role.kubernetes.io/master \
            -o custom-columns=NAME:.metadata.name --no-headers
        } | awk 'NF {print $1}' | LC_ALL=C sort -u
      )
      if ((${#control_plane_nodes[@]} == 1)); then
        target_node="${control_plane_nodes[0]}"
      else
        bundle_die "cannot identify the locally imported control-plane node; pass --target-node"
      fi
    fi
  fi
fi

if [[ -n "$target_node" ]]; then
  [[ "$target_node" =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$ ]] ||
    bundle_die "invalid target node name: ${target_node}"
  kubectl get node "$target_node" >/dev/null ||
    bundle_die "target node does not exist: ${target_node}"
fi

chart_relative="$(bundle_property "$bundle_root" chart)"
bundle_safe_relative_path "$chart_relative" ||
  bundle_die "unsafe chart path"
chart_path="${bundle_root}/${chart_relative}"
[[ -f "$chart_path" ]] || bundle_die "chart is missing: ${chart_relative}"

agent_image="${agent_image_override:-$(bundle_property "$bundle_root" agent_image)}"
[[ "$agent_image" != *@* ]] ||
  bundle_die "digest-only chart references are not supported; use repository:tag"
[[ "$agent_image" =~ ^[A-Za-z0-9._:/-]+:[A-Za-z0-9._-]+$ ]] ||
  bundle_die "invalid Agent image reference: ${agent_image}"
image_repository="${agent_image%:*}"
image_tag="${agent_image##*:}"

kubectl create namespace "$namespace" --dry-run=client -o yaml |
  kubectl apply -f - >/dev/null

if [[ -n "$openai_api_key_file" ]]; then
  bundle_info "creating/updating API key Secret from local file"
  kubectl --namespace "$namespace" create secret generic "$openai_existing_secret" \
    --from-file="api-key=${openai_api_key_file}" \
    --dry-run=client -o yaml |
    kubectl apply -f - >/dev/null
elif [[ -n "$openai_existing_secret" ]]; then
  kubectl --namespace "$namespace" get secret "$openai_existing_secret" >/dev/null ||
    bundle_die "OpenAI Secret does not exist: ${namespace}/${openai_existing_secret}"
fi

helm_args=(
  upgrade --install "$release_name" "$chart_path"
  --namespace "$namespace"
  --values "${bundle_root}/values/values-existing-cluster.yaml"
  --atomic
  --wait
  --timeout "$timeout"
  --history-max 10
  --set-string "image.repository=${image_repository}"
  --set-string "image.tag=${image_tag}"
  --set "image.pullPolicy=IfNotPresent"
  --set "dashboard.service.type=${dashboard_type}"
)

for values_file in "${extra_values[@]}"; do
  [[ -f "$values_file" ]] || bundle_die "values file does not exist: ${values_file}"
  helm_args+=(--values "$values_file")
done
for index in "${!target_namespaces[@]}"; do
  helm_args+=(--set-string "rbac.targetNamespaces[${index}]=${target_namespaces[$index]}")
done
if [[ "$dashboard_type" == "NodePort" ]]; then
  helm_args+=(--set "dashboard.service.nodePort=${dashboard_node_port}")
  for index in "${!dashboard_cidrs[@]}"; do
    helm_args+=(--set-string "networkPolicy.dashboardAllowedCIDRs[${index}]=${dashboard_cidrs[$index]}")
  done
fi
if [[ -n "$target_node" ]]; then
  helm_args+=(--set-string "nodeSelector.kubernetes\\.io/hostname=${target_node}")
fi
if [[ -n "$openai_base_url" ]]; then
  helm_args+=(
    --set-string "supervisor.analysis.openAI.baseURL=${openai_base_url}"
    --set-string "supervisor.analysis.openAI.model=${openai_model}"
  )
  if [[ -n "$openai_existing_secret" ]]; then
    helm_args+=(--set-string "supervisor.analysis.openAI.existingSecret=${openai_existing_secret}")
  fi
fi
if [[ "$enable_recovery" == "true" ]]; then
  helm_args+=(
    --set "supervisor.remediation.enabled=true"
    --set-string "supervisor.remediation.templateNamespace=${recovery_template_namespace}"
  )
fi

bundle_info "installing InferNex Agent"
helm "${helm_args[@]}"

verify_args=(
  --bundle-dir "$bundle_root"
  --namespace "$namespace"
  --release "$release_name"
  --timeout "$timeout"
  --skip-checksums
)
for target_namespace in "${target_namespaces[@]}"; do
  verify_args+=(--target-namespace "$target_namespace")
done
"${script_dir}/verify-agent.sh" "${verify_args[@]}"

if [[ "$dashboard_type" == "NodePort" ]]; then
  bundle_info "dashboard: http://<${target_node:-control-plane-node}-internal-ip>:${dashboard_node_port}/"
else
  bundle_info "dashboard remains ClusterIP; use kubectl port-forward when needed"
fi
