# InferNex Agent

[English](README.md) | [简体中文](README-zh.md)

InferNex Agent is the domain tool boundary between an agent runtime and the
InferNex control plane. It reuses the existing `InferNexService` API and
Bridge-generated status instead of reimplementing serving lifecycle or
readiness logic.

Published `linux-amd64` and `linux-arm64` bundles for both in-cluster and
host/systemd installation are available on the
[0.3.0-rc.6 release](https://github.com/lsjfy-open-com/infernex-agent/releases/tag/infernex-agent-v0.3.0-rc.6).
The repository's `scripts/download-bundle.sh` downloads the matching archive
and checksum, verifies it, and extracts it without changing the cluster.

Host installations also provide an interactive terminal:

```bash
sudo /opt/infernex-agent/bin/configure-model.sh \
  --base-url http://model.internal:8000/v1 \
  --model ops-model --api-key-file /secure/model.key --test-tools
sudo /opt/infernex-agent/bin/chat.sh
```

The model must support OpenAI-compatible function/tool calling. Read-only MCP
tools run automatically; every mutating tool requires an exact local `yes`.
One-shot `--ask` mode always denies writes.

The default installation publishes four typed, read-only tools:

- `infernex_list_services`
- `infernex_inspect_service`
- `infernex_get_topology`
- `infernex_get_events`

When catalog deployment is explicitly enabled, it also publishes:

- `infernex_deploy_model`
- `infernex_delete_model`
- `infernex_get_change` (read-only change/rollback status)

The output is deliberately normalized. It includes InferNex status, managed
Deployment/DaemonSet/LeaderWorkerSet readiness, and compact Pod evidence. It
also correlates recent Kubernetes Events only to the selected service and its
managed objects. It does not return Secret objects, environment variables,
full Pod specs, or a generic Kubernetes command surface.

The deployment tools are deliberately narrower than Kubernetes write access.
They accept only `namespace`, `name`, the fixed `catalogId`, and
`confirm: true`. They do not accept an image, model URL, command, patch, or
arbitrary object. The Agent creates only an `InferNexService`; InferNex Bridge
continues to own workload and Service reconciliation, readiness, and garbage
collection.

## Change safety and rollback

Catalog writes are protected by a durable change journal. Before creating a
service, the Agent persists the pre-change state and assigns an opaque
`changeId`. It then observes InferNex status in the background. A new service
that does not reach `Ready` before `--deployment-readiness-timeout` (10 minutes
by default), or reports `Degraded` for the observed generation, is deleted
only after both Agent ownership and the exact `changeId` are verified. This
restores the pre-deployment state, which was "resource absent".

Use `infernex_get_change` to distinguish `committed`, `rolled-back`,
`rollback-failed`, and `apply-failed`. Pending changes resume after an Agent
restart.

The host installer also creates a checksummed pre-install recovery point under
`/var/lib/infernex-agent/backups/` before replacing any Agent files. If
installation or verification fails, it restores the previous host files,
systemd state, and Agent-managed cluster source resources automatically.

See [change safety, backup, and rollback](docs/change-safety-zh.md) for the
restore CLI, persistence requirements, guarantees, and boundaries.

## Continuous supervisor and dashboard

The HTTP deployment can run a continuous, read-only supervisor in addition to
the MCP server. It:

- scans an explicit namespace allowlist on a configurable interval;
- correlates `InferNexService` status with managed workload and Pod readiness;
- fetches recent related Kubernetes warning Events only for degraded services;
- classifies reconciliation lag, component failure, replica deficit, Pod
  failure/restarts, and warning Events;
- caches analysis for unchanged evidence so a configured model is not called
  every scan; and
- publishes an embedded dashboard plus `/api/v1/snapshot` on a separate port.

The deterministic evidence pipeline works without a model. To add advisory
analysis, configure an internal OpenAI-compatible `/v1/chat/completions`
endpoint. The API key is accepted only through
`INFERNEX_OPENAI_API_KEY`/an existing Kubernetes Secret. The normalized model
input excludes Secret objects, environment variables, Kubernetes credentials,
node names, and Event notes.

The supervisor is advisory and read-only by default. Enabling the existing
deployment catalog does not let model output bypass its fixed catalog,
ownership checks, namespace RBAC, or explicit confirmation contract.

When bounded log diagnostics are explicitly enabled, degraded services also
receive redacted current/previous container-log evidence and a cross-node
incident timeline for NPU/CANN, HCCL/RDMA, Mooncake/KV transport, vLLM worker,
stream interruption, invalid UTF-8/output corruption, OOM, and timeout errors.
The optional MCP tool is `infernex_diagnose_service`; raw unmatched logs are
not retained. Continuous scans collect logs for at most 10 degraded services
per pass by default, so a widespread outage does not cause unbounded log API
traffic.

### Progressive single-feature experiments

An additional opt-in controller turns an approved sequence of sparse
`InferNexServiceConfig` feature profiles into a durable experiment. Each stage
keeps the stable service, prepends exactly one feature profile, and creates a
separate candidate through the existing Bridge API. Ready/current-generation,
baseline health, candidate-vs-baseline diagnostic comparison, and soak time are
mandatory gates. A regression or timeout deletes only the exact owned current
candidate; completed stages become the next stable baseline.

It publishes `infernex_start_experiment`, `infernex_get_experiment`, and
`infernex_list_experiments`, plus `/api/v1/experiments` on the dashboard. It
does not generate profiles, replay inference traffic, switch production
traffic, or delete passed candidates. See the
[progressive experiment and cross-node diagnostics guide](docs/progressive-experiments-zh.md).

### Guarded automatic recovery service

Automatic recovery is a separate, deterministic, double-opt-in path. It is
disabled by default and never depends on model output.

An operator first creates a complete, versioned `InferNexServiceConfig` using
the normal InferNex workflow, then explicitly approves it:

```bash
kubectl --namespace infernex-bridge-system label infernexserviceconfig \
  qwen-pd-recovery-v1 \
  agent.infernex.io/approved-recovery-profile=true
```

The source service must also opt in and select that exact profile:

```bash
kubectl --namespace models annotate infernexservice qwen-pd \
  agent.infernex.io/auto-recovery=true \
  agent.infernex.io/recovery-profile=qwen-pd-recovery-v1 \
  agent.infernex.io/recovery-name=qwen-pd-recovery
```

Finally enable the controller-wide policy:

```yaml
supervisor:
  remediation:
    enabled: true
    templateNamespace: infernex-bridge-system
    minCriticalScans: 3
```

After the configured number of consecutive scans with a stable observed
generation and at least one critical issue, the Agent creates only a new
`InferNexService` whose sole desired-state input is the approved `baseRef`.
Repeating the action is idempotent; an unowned name collision or spec drift is
refused. InferNex Bridge performs reconciliation and publishes status.

The recovery path does not:

- create or modify an `InferNexServiceConfig`;
- update or delete the failing source service;
- create Deployments, Services, Pods, or arbitrary Kubernetes objects;
- switch production traffic; or
- let model analysis select a profile or trigger an action.

The new service and recovery state appear in the dashboard. Traffic promotion
should remain an operator or future approved-plan action after health and SLO
verification.

## CPU test-model catalog

The first entry is `smollm2-135m-q4`:

- model: [SmolLM2-135M-Instruct Q4_K_M GGUF](https://huggingface.co/bartowski/SmolLM2-135M-Instruct-GGUF)
- runtime: [llama.cpp server](https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md)
- requirements: CPU only, approximately 105 MB model download, no Hugging Face token
- API: OpenAI-compatible `/v1/chat/completions` on port 8080

The catalog pins the Hugging Face repository revision, verifies the GGUF
SHA-256 before startup, and pins the multi-platform llama.cpp server image
digest. It is a lightweight integration-test profile, not a replacement for
the production vLLM/Ascend engine profiles.

## Local development

Use a kubeconfig that can read the target namespace:

```bash
go run ./cmd/infernex-agent \
  --transport=streamable-http \
  --listen-address=:8080 \
  --kubeconfig="$HOME/.kube/config"
```

The MCP endpoint is `http://localhost:8080/mcp`. Health endpoints are
`/healthz` and `/readyz`.

To run the supervisor and dashboard from a Linux management node with an
external kubeconfig:

```bash
export INFERNEX_OPENAI_API_KEY='replace-with-internal-provider-key'

go run ./cmd/infernex-agent \
  --transport=streamable-http \
  --listen-address=:8080 \
  --dashboard-listen-address=:8081 \
  --scan-namespaces=models,production-models \
  --scan-interval=60s \
  --openai-base-url=http://internal-model.example:8000/v1 \
  --openai-model=ops-model \
  --kubeconfig="$HOME/.kube/config"
```

Open `http://<management-node>:8081/` for the dashboard or read
`http://<management-node>:8081/api/v1/snapshot` from another monitoring
system. Bind the listener to a private interface or place an authenticated
reverse proxy in front of it.

Stdio is available for local MCP clients:

```bash
go run ./cmd/infernex-agent --transport=stdio
```

## kubectl-ai integration

InferNex does not vendor kubectl-ai. Configure an installed kubectl-ai runtime
as the MCP client in `~/.config/kubectl-ai/mcp.yaml`:

```yaml
servers:
  - name: infernex
    url: http://infernex-agent.infernex-system.svc:8080/mcp
```

Then start kubectl-ai with MCP client mode:

```bash
kubectl-ai --mcp-client
```

For a production deployment, run kubectl-ai and InferNex Agent in the same
restricted management namespace, keep the Agent Service internal, and enable
the chart NetworkPolicy.

## Build and test

```bash
go test ./...
go vet ./...
go build ./cmd/infernex-agent
```

The real-cluster tests require Docker, Kind, Helm, kubectl, curl, and jq:

```bash
kind create cluster --name infernex-agent
# Install the InferNexService and LeaderWorkerSet CRDs, build/load the image,
# then run the read-only contract:
bash ./test/e2e/smoke.sh

# Build/load InferNex Bridge as infernex-bridge:e2e, then run the guarded
# deployment, real inference, observation, idempotency, and deletion flow:
bash ./test/e2e/tiny-model.sh
```

The repository workflow at
`.github/workflows/infernex-agent.yaml` automates this sequence on a GitHub
hosted Ubuntu runner. No paid Kubernetes cluster or GPU is required.

The image build uses `component/` as its context because the Go module imports
the canonical CRD types from `InferNex-Bridge`:

```bash
make docker-build
```

## Offline bundle

For an existing air-gapped InferNex cluster, build a self-contained Agent
bundle on a connected Linux host:

```bash
./scripts/offline/build-bundle.sh \
  --architecture amd64 \
  --agent-image docker.io/library/infernex-agent:0.3.0 \
  --output-dir ./dist
```

The bundle contains only the Agent image, Helm chart, content checksums, and
install/verification commands. It does not reinstall InferNex Bridge, NPU
drivers, gateways, model weights, or inference workloads. Optional images
used by approved recovery profiles can be supplied through `--extra-images`.

On the offline master/control-plane node:

```bash
sha256sum --check infernex-agent-offline-0.3.0-linux-amd64.tar.gz.sha256
tar -xzf infernex-agent-offline-0.3.0-linux-amd64.tar.gz
cd infernex-agent-offline-0.3.0-linux-amd64

./bin/install-agent.sh \
  --target-node master-01 \
  --target-namespace kserve \
  --dashboard-cidr 10.20.0.0/16 \
  --runtime ctr
```

See the complete Chinese guide:
[offline build and existing-cluster installation](docs/offline-install-zh.md).

For an Agent that runs directly on an openEuler master/bootstrap host rather
than in Kubernetes, build the static-binary bundle:

```bash
./scripts/offline/build-host-bundle.sh \
  --version 0.3.0 \
  --architecture arm64 \
  --output-dir ./dist
```

The host mode uses the existing `--kubeconfig` support, a dedicated
namespace-scoped identity, a non-root hardened systemd unit, loopback-only
MCP/dashboard defaults, and an API-key credential file. See the
[openEuler host installation guide](docs/host-install-openeuler-zh.md).

Model analysis is optional in both deployment modes. A host installation can
start without a model and configure one later without reinstalling:

```bash
sudo /opt/infernex-agent/bin/configure-model.sh \
  --base-url http://internal-model.example:8000/v1 \
  --model ops-model \
  --api-key-file /secure/infernex-agent-openai.key \
  --test \
  --show
```

Non-secret effective arguments are stored in
`/etc/infernex-agent/agent.conf`; the optional API key remains a separate
`0600` credential.

Product documentation:

- [Installation and operating-mode guide (Chinese)](docs/install-and-modes-zh.md)
- [Product guide and acceptance](docs/product-guide-zh.md)
- [Product design and failure semantics](docs/product-design-zh.md)
- [Progressive experiments and cross-node diagnostics](docs/progressive-experiments-zh.md)
- [Change safety, backup, and rollback](docs/change-safety-zh.md)
- [Model configuration lifecycle](docs/model-configuration-zh.md)
- [Security and capability boundaries](docs/security-boundaries-zh.md)
- [Operations runbook](docs/operations-runbook-zh.md)

## Deployment

The Helm chart defaults to namespace-scoped, read-only RBAC and does not
register mutation tools. With no `rbac.targetNamespaces`, the release namespace
is the only observable namespace.

```bash
helm upgrade --install infernex-agent ./chart/infernex-agent \
  --namespace infernex-system \
  --create-namespace
```

### Kubernetes master/control-plane node

The management-node profile accepts both the modern
`node-role.kubernetes.io/control-plane` label and the legacy
`node-role.kubernetes.io/master` label, and adds the corresponding
`NoSchedule` tolerations.

Create an API-key Secret only when the internal model requires one:

```bash
kubectl --namespace infernex-system create secret generic infernex-agent-openai \
  --from-literal=api-key='replace-with-internal-provider-key'
```

Install on the master/control-plane node and expose the read-only dashboard:

```bash
helm upgrade --install infernex-agent ./chart/infernex-agent \
  --namespace infernex-system \
  --create-namespace \
  --values ./chart/infernex-agent/values-master-node.yaml \
  --set 'rbac.targetNamespaces[0]=models' \
  --set-string 'supervisor.analysis.openAI.baseURL=http://internal-model.example:8000/v1' \
  --set-string 'supervisor.analysis.openAI.model=ops-model' \
  --set-string 'supervisor.analysis.openAI.existingSecret=infernex-agent-openai'
```

To enable a pre-approved recovery profile, add:

```bash
  --set supervisor.remediation.enabled=true \
  --set-string supervisor.remediation.templateNamespace=infernex-bridge-system
```

To enable bounded log diagnostics and progressive experiments, add:

```bash
  --set supervisor.diagnostics.logs.enabled=true \
  --set experiments.enabled=true \
  --set-string experiments.templateNamespace=infernex-bridge-system \
  --set changeSafety.persistence.enabled=true
```

Experiments require namespace-scoped RBAC and spare capacity for a parallel
candidate. They never switch traffic automatically.

The dashboard is then available at:

```text
http://<master-node-ip>:30081/
```

`values-master-node.yaml` permits `0.0.0.0/0` for initial access. Replace it
with the internal operations CIDR before production use:

```yaml
networkPolicy:
  dashboardAllowedCIDRs:
    - 10.20.0.0/16
```

The NodePort Service selects only dashboard port `8081`; MCP remains on its
separate ClusterIP Service. The dashboard is read-only but is not an
authentication boundary, so do not expose it directly to the public Internet.

To observe services in selected namespaces while keeping the Agent in
`infernex-system`:

```yaml
rbac:
  targetNamespaces:
    - model-a
    - model-b
```

Cluster-wide reads are opt-in through `rbac.clusterWide: true`.

To enable the fixed catalog only in selected namespaces:

```yaml
rbac:
  clusterWide: false
  targetNamespaces:
    - models

tools:
  deployment:
    enabled: true

changeSafety:
  persistence:
    enabled: true
    existingClaim: infernex-agent-state
```

```bash
helm upgrade --install infernex-agent ./chart/infernex-agent \
  --namespace infernex-system \
  --create-namespace \
  --set 'rbac.targetNamespaces[0]=models' \
  --set tools.deployment.enabled=true
```

Cluster-wide RBAC and catalog deployment cannot be enabled together. The
resulting Role adds only `create/delete` for `InferNexService`; it still cannot
create Deployments, read Secrets, create namespaces, or create cluster RBAC.

Example MCP arguments:

```json
{
  "namespace": "models",
  "name": "kind-smollm",
  "catalogId": "smollm2-135m-q4",
  "confirm": true
}
```

Use the returned `changeId` with `infernex_get_change` until its status is
`committed` or `rolled-back`. Use the same deployment arguments with
`infernex_delete_model`; deletion is refused unless the existing service
carries the Agent catalog ownership labels.

## Tested standalone candidates

Successful CI runs publish static `linux-amd64` and `linux-arm64` candidate
binaries with SHA256 files. They are intended for validation on an existing
management host before a formal release is assembled; the target server needs
no Go, Python, Node.js, compiler, or source checkout.

The binary provides `version`, `serve --config`, `doctor`, `chat`, and guarded
`candidate verify|apply|rollback` commands. Candidate activation preserves the
installed configuration, backs up the current binary, restarts systemd, waits
for the health endpoint, and automatically restores the previous binary if the
new process does not become healthy. See the
[Chinese candidate validation guide](docs/candidate-validation-zh.md) for the
operator workflow and acceptance gates.

See [docs/architecture.md](docs/architecture.md) for component boundaries and
the broader mutation roadmap.
