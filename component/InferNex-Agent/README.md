# InferNex Agent

InferNex Agent is the domain tool boundary between an agent runtime and the
InferNex control plane. It reuses the existing `InferNexService` API and
Bridge-generated status instead of reimplementing serving lifecycle or
readiness logic.

The default installation publishes four typed, read-only tools:

- `infernex_list_services`
- `infernex_inspect_service`
- `infernex_get_topology`
- `infernex_get_events`

When catalog deployment is explicitly enabled, it also publishes:

- `infernex_deploy_model`
- `infernex_delete_model`

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

## Deployment

The Helm chart defaults to namespace-scoped, read-only RBAC and does not
register mutation tools. With no `rbac.targetNamespaces`, the release namespace
is the only observable namespace.

```bash
helm upgrade --install infernex-agent ./chart/infernex-agent \
  --namespace infernex-system \
  --create-namespace
```

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

Use the same arguments with `infernex_delete_model`; deletion is refused unless
the existing service carries the Agent catalog ownership labels.

See [docs/architecture.md](docs/architecture.md) for component boundaries and
the broader mutation roadmap.
