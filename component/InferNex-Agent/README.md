# InferNex Agent

InferNex Agent is the domain tool boundary between an agent runtime and the
InferNex control plane. The first release is a read-only MCP server. It reuses
the existing `InferNexService` API and Bridge-generated status instead of
reimplementing serving lifecycle or readiness logic.

It currently publishes four typed tools:

- `infernex_list_services`
- `infernex_inspect_service`
- `infernex_get_topology`
- `infernex_get_events`

The output is deliberately normalized. It includes InferNex status, managed
Deployment/DaemonSet/LeaderWorkerSet readiness, and compact Pod evidence. It
also correlates recent Kubernetes Events only to the selected service and its
managed objects. It does not return Secret objects, environment variables,
full Pod specs, or a generic Kubernetes command surface.

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

The real-cluster smoke test requires Docker, Kind, Helm, kubectl, curl, and jq:

```bash
kind create cluster --name infernex-agent
# Install the InferNexService and LeaderWorkerSet CRDs, build/load the image,
# then run:
bash ./test/e2e/smoke.sh
```

The repository workflow at
`.github/workflows/infernex-agent.yaml` automates this sequence on a GitHub
hosted Ubuntu runner, including RBAC negative tests and all four MCP calls.

The image build uses `component/` as its context because the Go module imports
the canonical CRD types from `InferNex-Bridge`:

```bash
make docker-build
```

## Deployment

The Helm chart defaults to namespace-scoped, read-only RBAC. With no
`rbac.targetNamespaces`, the release namespace is the only observable
namespace.

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

See [docs/architecture.md](docs/architecture.md) for component boundaries and
the mutation roadmap.
