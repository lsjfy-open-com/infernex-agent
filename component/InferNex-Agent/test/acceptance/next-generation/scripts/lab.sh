#!/usr/bin/env bash
set -euo pipefail

readonly EXPECTED_CONTEXT="kind-infernex-nextgen-acceptance"
readonly FIXTURE_NAMESPACE="infernex-nextgen-acceptance"
readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly LAB_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
readonly MANIFEST="${LAB_DIR}/manifests/topology.yaml"
readonly HELM_CHART="${LAB_DIR}/helm/inventory-fixture"
readonly HELM_RELEASE="acceptance-inventory"

usage() {
  echo "usage: $0 apply|check|cleanup" >&2
}

require_disposable_context() {
  local named_context
  named_context="$(kubectl config get-contexts "${EXPECTED_CONTEXT}" -o name)"
  if [[ "${named_context}" != "${EXPECTED_CONTEXT}" ]]; then
    echo "refusing: required context '${EXPECTED_CONTEXT}' does not exist" >&2
    exit 2
  fi
}

require_owned_namespace_if_present() {
  local namespace_name
  namespace_name="$(kubectl --context "${EXPECTED_CONTEXT}" get namespace "${FIXTURE_NAMESPACE}" -o name --ignore-not-found)"
  if [[ -n "${namespace_name}" ]]; then
    kubectl --context "${EXPECTED_CONTEXT}" get namespace "${FIXTURE_NAMESPACE}" \
      -o jsonpath='{.metadata.labels.infernex\.io/acceptance}' | grep -qx 'test-only' || {
        echo "refusing: existing namespace is not labeled infernex.io/acceptance=test-only" >&2
        exit 3
      }
  fi
}

case "${1:-}" in
  apply)
    require_disposable_context
    require_owned_namespace_if_present
    kubectl --context "${EXPECTED_CONTEXT}" apply -f "${MANIFEST}"
    helm upgrade --install "${HELM_RELEASE}" "${HELM_CHART}" \
      --kube-context "${EXPECTED_CONTEXT}" \
      --namespace "${FIXTURE_NAMESPACE}" \
      --set-string image.repository=registry.k8s.io/pause \
      --set-string image.tag=3.10 \
      --wait --timeout 90s
    kubectl --context "${EXPECTED_CONTEXT}" -n "${FIXTURE_NAMESPACE}" wait \
      --for=condition=Available deployment/native-inventory deployment/helm-metadata-mock deployment/helm-release-inventory \
      --timeout=90s
    ;;
  check)
    require_disposable_context
    require_owned_namespace_if_present
    kubectl --context "${EXPECTED_CONTEXT}" get namespace "${FIXTURE_NAMESPACE}" \
      -o jsonpath='{.metadata.labels.infernex\.io/acceptance}' | grep -qx 'test-only'
    kubectl --context "${EXPECTED_CONTEXT}" -n "${FIXTURE_NAMESPACE}" get deployment,service \
      -l infernex.io/acceptance=test-only
    helm status "${HELM_RELEASE}" --kube-context "${EXPECTED_CONTEXT}" --namespace "${FIXTURE_NAMESPACE}"
    ;;
  cleanup)
    require_disposable_context
    namespace_name="$(kubectl --context "${EXPECTED_CONTEXT}" get namespace "${FIXTURE_NAMESPACE}" -o name --ignore-not-found)"
    if [[ -n "${namespace_name}" ]]; then
      kubectl --context "${EXPECTED_CONTEXT}" get namespace "${FIXTURE_NAMESPACE}" \
        -o jsonpath='{.metadata.labels.infernex\.io/acceptance}' | grep -qx 'test-only' || {
          echo "refusing: namespace is missing infernex.io/acceptance=test-only" >&2
          exit 3
        }
      if helm status "${HELM_RELEASE}" --kube-context "${EXPECTED_CONTEXT}" --namespace "${FIXTURE_NAMESPACE}" >/dev/null 2>&1; then
        helm uninstall "${HELM_RELEASE}" --kube-context "${EXPECTED_CONTEXT}" --namespace "${FIXTURE_NAMESPACE}" --wait
      fi
      kubectl --context "${EXPECTED_CONTEXT}" delete namespace "${FIXTURE_NAMESPACE}" --wait=true
    else
      echo "namespace ${FIXTURE_NAMESPACE} is already absent"
    fi
    ;;
  *)
    usage
    exit 64
    ;;
esac
