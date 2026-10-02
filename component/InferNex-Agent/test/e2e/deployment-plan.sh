#!/usr/bin/env bash
# Runs only in the disposable Kind CI cluster, before any Bridge CRD is installed.
set -euo pipefail
[[ "$(kubectl config current-context)" == "kind-infernex-agent" ]] || {
  echo 'deployment-plan smoke requires the dedicated kind-infernex-agent context' >&2
  exit 1
}
agent_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
task_dir="$(mktemp -d)"
namespace="deployment-plan-smoke"
# Do not replace a namespace that was not created by this test.
if kubectl get namespace "$namespace" >/dev/null 2>&1; then
  echo 'deployment-plan smoke namespace already exists' >&2
  exit 1
fi
kubectl create namespace "$namespace"
cleanup() {
  kubectl delete namespace "$namespace" --wait=false >/dev/null || true
  rm -rf -- "$task_dir"
}
trap cleanup EXIT
if kubectl get crd infernexservices.infernex.infernex.io >/dev/null 2>&1; then
  echo 'planning must be tested before Bridge CRDs are installed' >&2
  exit 1
fi
(cd "$agent_dir" && go build -o "$task_dir/agent" ./cmd/infernex-agent)
cat > "$task_dir/profile.json" <<'JSON'
{"version":"infernex.openfuyao.io/v1alpha1","image":"example.invalid/model@sha256:0000000000000000000000000000000000000000000000000000000000000000","cpu":"100m","memory":"64Mi"}
JSON
"$task_dir/agent" deployment-plan --profile "$task_dir/profile.json" \
  --namespace "$namespace" --replicas 1 --kubeconfig "$HOME/.kube/config" > "$task_dir/plan.json"
python3 - "$task_dir/plan.json" <<'PY'
import json,sys
p=json.load(open(sys.argv[1]))
assert p['placeableReplicas']==1,p
assert p['requestedReplicas']==1,p
assert p['trafficVerified'] is False and p['performanceVerified'] is False
assert p['reservationCreated'] is False
assert p['planHash'] and p['snapshotHash'] and p['profileHash']
print('Bridge-free live resource plan verified')
PY
kubectl -n "$namespace" create quota deny-new-pods --hard=pods=0
"$task_dir/agent" deployment-plan --profile "$task_dir/profile.json" \
  --namespace "$namespace" --replicas 1 --kubeconfig "$HOME/.kube/config" > "$task_dir/quota.json"
python3 - "$task_dir/quota.json" <<'PY'
import json,sys
p=json.load(open(sys.argv[1]))
assert p['status'] in ('blocked','insufficient'),p
assert p.get('reasons'),p
print('Live ResourceQuota prevents an unconditional fit claim')
PY
[[ "$(kubectl -n "$namespace" get pods,deployments,services -o name | wc -l | tr -d ' ')" == 0 ]]
echo 'Deployment planning created no workloads or services'
