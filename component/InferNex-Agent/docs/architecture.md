# InferNex Agent architecture

## Decision

InferNex Agent is an InferNex control-plane component on a management node. It
is not part of the inference request data path. The component is a typed MCP
server and uses kubectl-ai as a replaceable agent runtime. Observation is the
default; a fixed deployment catalog is an explicit, namespace-scoped opt-in.

```text
Operator
   |
   v
kubectl-ai (conversation, LLM, session, approval UX)
   |
   | MCP
   v
InferNex Agent (typed domain tools, policy/catalog boundary)
   |
   | Kubernetes API, namespace-scoped identity
   v
InferNexService desired state + status
   |
   +-- InferNex-Bridge -> Deployment / Service / LeaderWorkerSet
   +-- Hermes / Cache Indexer / Mooncake
   +-- PD Orchestrator / Eagle-Eye
```

## Reuse boundary

| Capability | Owner | Agent behavior |
| --- | --- | --- |
| Conversation, model providers, session UI | kubectl-ai | Reuse through MCP client mode |
| Desired serving topology | `InferNexService` | Read canonical CRD fields; create only fixed catalog objects when enabled |
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

## Observation tool contract

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

## Guarded deployment catalog

V0.2 adds a deliberately small mutation rather than a generic Kubernetes write
surface:

1. `infernex_deploy_model` requires namespace, name, a compiled-in catalog ID,
   and `confirm=true`.
2. The catalog constructs a complete canonical `InferNexService`. The Agent
   does not create or reconcile a Deployment or Service.
3. InferNex Bridge performs its normal reconciliation and publishes canonical
   status. Existing observation tools verify that result.
4. Repeating deployment is idempotent. An unowned name collision or any spec
   drift is refused rather than overwritten.
5. `infernex_delete_model` is explicitly destructive and only deletes a
   service carrying both Agent ownership and matching catalog labels. Bridge
   and Kubernetes garbage collection remove downstream resources.

The first catalog entry is a CPU-only SmolLM2-135M Q4_K_M profile intended for
Kind integration tests. Model revision, file checksum, and llama.cpp runtime
image are pinned. It explicitly disables optional production components so the
existing Bridge reconciler creates only the aggregate inference engine and its
ClusterIP Service.

## Security model

The default installation corresponds to an observation-only permission level:

1. The domain container uses a short-lived projected ServiceAccount token
   through in-cluster configuration; pod-wide token automount is disabled.
2. The default chart creates Roles only in explicit target namespaces.
3. RBAC grants only `get/list` on `InferNexService` and `list` on the workload
   objects and Events required by the topology and evidence tools.
4. The Service is `ClusterIP`.
5. The default NetworkPolicy permits MCP ingress only from the Agent release
   namespace.
6. The LLM never receives kubeconfig or Kubernetes bearer tokens.

When deployment is enabled:

1. Cluster-wide RBAC is rejected by the Helm chart.
2. The namespace Role adds only `create/delete` on `InferNexService`.
3. The Agent still cannot create Deployments, read Secrets, create namespaces,
   or create cluster-scoped RBAC.
4. Code-level catalog validation and ownership checks narrow the operation
   beyond what Kubernetes RBAC can express.
5. Mutation tools are absent from MCP discovery unless deployment mode is
   explicitly enabled.

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

- L0: default read-only observation.
- L1: recommendation and immutable plans.
- L2: approved low-risk changes. The fixed test-model catalog is the first
  constrained L2 slice; bounded replica updates still require a plan contract.
- L3: privileged break-glass actions with external approval and audit.

The first mutation should target an InferNex-owned API, not raw Pod deletion or
arbitrary patching.
