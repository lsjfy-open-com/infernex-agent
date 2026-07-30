# InferNex Agent architecture

## Decision

InferNex Agent is an InferNex control-plane component on a management node. It
is not part of the inference request data path. The component starts as a
typed, read-only MCP server and uses kubectl-ai as a replaceable agent runtime.

```text
Operator
   |
   v
kubectl-ai (conversation, LLM, session, approval UX)
   |
   | MCP
   v
InferNex Agent (typed domain tools, policy boundary)
   |
   | Kubernetes API, namespace-scoped read-only identity
   v
InferNexService status + managed workloads
   |
   +-- InferNex-Bridge / LeaderWorkerSet
   +-- Hermes / Cache Indexer / Mooncake
   +-- PD Orchestrator / Eagle-Eye
```

## Reuse boundary

| Capability | Owner | Agent behavior |
| --- | --- | --- |
| Conversation, model providers, session UI | kubectl-ai | Reuse through MCP client mode |
| Desired serving topology | `InferNexService` | Read canonical CRD fields |
| Lifecycle and readiness | InferNex-Bridge | Reuse `.status`; do not recompute control-plane readiness |
| Actual group topology | LeaderWorkerSet / Kubernetes | Return a compact evidence view |
| Routing and cache state | Hermes / Cache Indexer | Add domain adapters when their stable APIs are available |
| Hardware and network diagnosis | Eagle-Eye / infernex-checker | Invoke or translate existing reports; do not duplicate checks |
| Scaling and PD orchestration | PD Orchestrator | Add bounded plans against its stable API |

The Agent does not expose `kubectl`, shell execution, Secrets, full Pod specs,
or raw Kubernetes object traversal. This prevents the LLM tool contract from
quietly becoming a second cluster administration interface.

## Recommended management-node composition

For an in-cluster management node, the preferred production composition is one
Pod with two separately built containers:

```text
Pod network namespace
  kubectl-ai runtime
    - web/terminal session and LLM provider
    - MCP client -> http://127.0.0.1:8080/mcp
    - no Kubernetes ServiceAccount token mount

  infernex-agent domain sidecar
    - typed MCP tools
    - projected, short-lived, read-only ServiceAccount token
```

Pod-wide ServiceAccount token automount must remain disabled. The chart
projects the token and cluster CA only into the InferNex Agent container,
which prepares this isolation before kubectl-ai is optionally co-scheduled.
This allows kubectl-ai's conversation and provider implementation to be reused
without granting its generic kubectl or shell tools a Kubernetes identity.

## V0.1 tool contract

All tools require an explicit namespace and are marked read-only, idempotent,
and closed-world in MCP metadata.

### `infernex_list_services`

Returns compact service readiness summaries for one namespace.

### `infernex_inspect_service`

Returns the canonical `InferNexService` status, model identity, source
reference, base templates, component status, and conditions. Model URI user
information, query parameters, and fragments are removed before returning the
value to the model.

### `infernex_get_topology`

Returns workloads labeled with `infernex.io/owner=<service-name>`:

- Deployment desired and ready replicas
- DaemonSet desired and ready pods
- LeaderWorkerSet desired and ready groups plus group size
- compact Pod placement, readiness, restart count, and reason

It deliberately does not reinterpret these objects as the service's canonical
readiness. The Bridge-owned service status remains authoritative.

Tool output is bounded for model-context safety. Service lists and Pod
topologies return at most 200 records and include total/truncation fields.

### `infernex_get_events`

Returns recent Kubernetes Events only when the involved object is the selected
`InferNexService` or a managed Deployment, DaemonSet, LeaderWorkerSet, or Pod.
The lookback window defaults to 60 minutes and is capped at 24 hours. Event
output defaults to 50 records and is capped at 200; notes are normalized and
bounded to 512 Unicode code points.

## Security model

V0.1 corresponds to an observation-only permission level:

1. The domain container uses a short-lived projected ServiceAccount token
   through in-cluster configuration; pod-wide token automount is disabled.
2. The default chart creates Roles only in explicit target namespaces.
3. RBAC grants only `get/list` on `InferNexService` and `list` on the workload
   objects and Events required by the topology and evidence tools.
4. The Service is `ClusterIP`.
5. The default NetworkPolicy permits MCP ingress only from the Agent release
   namespace.
6. The LLM never receives kubeconfig or Kubernetes bearer tokens.

The HTTP MCP transport itself is not an authentication boundary. Cross-network
or multi-tenant exposure requires an authenticated gateway or service mesh in
front of `/mcp`.

## Mutation roadmap

Do not add generic write tools. Mutations should use a two-step contract:

1. `plan_*` returns the target, preconditions, patch, risk level, impact,
   timeout, verification, and rollback.
2. `apply_plan` accepts a short-lived approved plan ID, checks generation and
   resource-version preconditions, performs one bounded change, and records
   evidence.

Suggested permission levels:

- L0: current read-only observation.
- L1: recommendation and immutable plans.
- L2: approved low-risk changes, such as bounded replica updates.
- L3: privileged break-glass actions with external approval and audit.

The first mutation should target an InferNex-owned API, not raw Pod deletion or
arbitrary patching.
