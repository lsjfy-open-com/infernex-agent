# Tool recipes and result handling

Use only tools and argument schemas returned by the connected server's
`tools/list`. The examples below are MCP `tools/call` **params**, not REST routes
or complete JSON-RPC requests. Replace example names with discovered targets.
The calling client's MCP SDK handles initialization, protocol version and transport.

## Kubernetes workload triage

Use for unhealthy inference workloads, startup failures, missing endpoints and
container errors. The published alpha.22 baseline supports generic Kubernetes
reads without Bridge.

1. Call `k8s_detect_environment` if listed, otherwise its compatibility name
   `openfuyao_detect_environment`. Check the reported control-plane role: a
   management/bootstrap cluster is not evidence about an unseen business cluster.
2. Call `k8s_cluster_overview` for visible context, then list the authorized
   workload scope. The schema key is `labelSelector`, not `selector`:

   ```json
   {"name":"k8s_list_workloads","arguments":{"namespace":"models","labelSelector":"app=my-model","limit":20}}
   ```

3. Use returned Pod/container identities for events and bounded logs:

   ```json
   {"name":"k8s_get_events","arguments":{"namespace":"models","kind":"Pod","name":"DISCOVERED_POD","sinceMinutes":15,"limit":30}}
   ```

   ```json
   {"name":"k8s_get_pod_logs","arguments":{"namespace":"models","pod":"DISCOVERED_POD","container":"DISCOVERED_CONTAINER","sinceMinutes":15,"tailLines":200,"previous":false}}
   ```

   Use `previous: true` for the prior terminated container when applicable.
   Record the time range and Pod UID when authorized evidence returns it. The
   ordinary workload/log summaries do not return UID; otherwise mark UID
   unavailable and do not assume continuity across a possible Pod replacement.
4. If investigating Service backends and the tool is listed, call
   `k8s_inspect_service_backends` with the discovered Service identity. EndpointSlice
   membership does not prove real request distribution or successful inference.
5. Only if Bridge is detected and its tools are listed, use
   `infernex_inspect_service`, `infernex_get_topology`, `infernex_get_events`, or
   `infernex_diagnose_service`. Do not substitute a Deployment name for an
   `InferNexService` name.

For an unknown resource type, use `k8s_discover_api_resources` first. The main
trusted endpoint can then use `k8s_read_resources` with the exact discovered
`groupVersion` and plural `resource`. The diagnostic delegate deliberately
excludes generic resource reads; return that limitation instead of switching endpoints.

## CANN / HiXL diagnosis

Use when the symptom involves Ascend runtime, NPU, HCCL, HiXL, LLM DataDist or
PD/KV transfer. CPU-only generic Kubernetes triage does not need these skills.

1. Call `infernex_list_skills`; select an exact available name:
   `cann-runtime-diagnosis` or `hixl-communication-diagnosis`.
2. Load one skill, then only the reference relevant to the symptom:

   ```json
   {"name":"infernex_read_skill","arguments":{"name":"cann-runtime-diagnosis"}}
   ```

   ```json
   {"name":"infernex_read_skill_reference","arguments":{"name":"cann-runtime-diagnosis","reference":"signatures.md"}}
   ```

   Use only reference filenames actually returned by the skill. These are
   server-provided diagnostic skills, separate from this caller-side integration skill.
3. Correlate the first error across ranks/nodes and record the observed hardware,
   driver, CANN and framework versions. Mark missing versions as unknown.
4. If passive evidence cannot answer the question, call
   `infernex_list_diagnostic_probes` and choose an advertised fixed probe/channel.
   Do not invent SSH aliases, host paths or shell arguments.
5. When task authorization covers collecting new evidence, use the available
   PlogCapture/CollectorRun tools with explicit scope and budgets from the live
   delegate contract. Saving a report is also a local write. A diagnosis request
   alone does not authorize changing a workload, restarting it or changing timeouts.

If the server has no diagnostic skills, continue generic triage and state the
knowledge/tool gap. Do not download a replacement or claim a hardware root cause.

## Read-only capacity planning

Use `k8s_plan_deployment` only if listed, with a caller-provided or operator-approved
versioned profile, namespace and replica count matching the live schema. If the
profile is missing, request it; do not invent image, accelerator, resource or
scheduling requirements. Planning tools are not available on every endpoint.

For an authorized local CLI workflow, the equivalent command is:

```bash
infernex-agent deployment-plan \
  --profile /ABSOLUTE/APPROVED-PROFILE.json \
  --namespace REPLACE_WITH_NAMESPACE \
  --replicas 2 \
  --kubeconfig /ABSOLUTE/READ-ONLY-KUBECONFIG
```

These are placeholders. This command reads the current cluster; it does not
deploy or reserve resources. Preserve blocked/unknown reasons and returned
profile, snapshot and plan hashes. A fit estimate is not proof of throughput,
hardware compatibility, quota completeness or future scheduler placement.

## Private Docker / Kubernetes inventory (development only)

Prerequisites and stdio startup are in the skill's `connection.md` reference.
The operator must already have initialized the local scope and registered a
verified environment. This flow does not register credentials or deploy workloads.

1. List available registered environments:

   ```json
   {"name":"infernex_list_domain_records","arguments":{"kind":"Environment","limit":20}}
   ```

2. Read the selected Environment using its returned `kind`, `id`, `revision`
   with `infernex_get_domain_record`; retain that exact revision. If multiple
   environments match and the user did not select one, resolve the target first.
3. Discover a bounded preview:

   ```json
   {"name":"infernex_discover_private_environment","arguments":{"environmentId":"RETURNED_ENVIRONMENT_UUID","revision":1}}
   ```

   Replace `1` with the actual current revision. Kubernetes namespace/resource
   filters can narrow the registration but cannot broaden it.
4. Return the preview unless the task authorizes saving it. To persist, call
   `infernex_record_domain_inventory` with the exact returned `previewHandle`
   and `digest`. Never supply raw snapshot JSON, credentials, a connection path
   or a tenant ID to this tool. Saving writes only local inventory records.
5. Verify a saved reference with `infernex_verify_domain_record`, passing the
   returned `kind`, `id`, `revision`. Listing/getting/verifying existing records
   does not refresh the environment; use observation timestamps in the conclusion.

Preview and pagination handles are process-local and expire after five minutes.
After restart or expiry, start a fresh list/preview; do not reconstruct a handle.
List continuation sends only `cursorHandle`, without first-page `kind` or `limit`.
Discovery continuation stays in the same stdio process and binds the original
environment revision and request filters. If persisting a paged discovery, save
each page in order; later snapshots reference the preceding page. Pages are
observations over different time windows, not a global atomic snapshot.

Preserve `observed`, `declared`, `inferred`, `unknown`, `conflict` and `partial`
semantics. A Docker device request does not prove usable NPU capacity; visible
containers or endpoints do not prove inference health.

## Result to return to the parent Agent

Provide the target identity, server version/endpoint role, observation time,
observed facts, hypotheses with supporting evidence, coverage limitations and
next action. Include resource/Pod identities, snapshot references or evidence
root + relative path + SHA-256 + time/line range when actually returned.

Keep the result usable without raw logs. Do not invent report artifacts or
successful writes. When a capability is unavailable, identify the missing tool,
scope or environment prerequisite instead of claiming the intended action ran.
