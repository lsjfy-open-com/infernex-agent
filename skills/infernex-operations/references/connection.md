# Connect an external Agent to InferNex

This reference is for an Agent consuming an operator-provisioned InferNex MCP
service. Installing this skill does not install the service, grant Kubernetes
access, configure a model, or authorize changes.

## Version and environment

- The repository's published package baseline is `0.5.0-alpha.22`. Check the
  deployed binary with `infernex-agent version` and inspect the MCP initialize
  response and `tools/list`; a checkout can contain capabilities absent from an
  installed package.
- Supported release packages target Linux `amd64` and `arm64`. The host installer
  uses systemd, an existing Kubernetes identity, `kubectl`, `curl`, and standard
  Linux utilities. Helm and InferNex Bridge are not required for the general
  Kubernetes observation tools. The host does not need an NPU merely to run the
  Agent.
- A published binary or complete host bundle does not require separately
  installing Go, Python, or Node.js. Source builds have separate build
  dependencies; do not prescribe them to a package consumer.
- An external HTTP MCP client needs network access to the approved endpoint and
  the delegated credential. Kubernetes credentials stay with the InferNex
  service; do not request or copy the service's kubeconfig into the caller.
- The general MCP service requires access to its configured Kubernetes API and
  the permissions for the selected tools. Examples include `get/list` for
  observed objects and `get pods/log` for logs. Active Pod probes additionally
  require approved Pod exec access. Scope and RBAC failures are capability
  limits, not reasons to switch identities.
- An OpenAI-compatible model is optional for the MCP server's advisory analysis.
  An external Agent can supply its own reasoning model and call MCP directly.
  The product's interactive chat/TUI needs its own configured model; this is a
  different requirement from serving MCP tools.
- Private inventory is an **unreleased development capability**, separate from
  the alpha.22 package baseline. Its additional restrictions are described below.

## Preferred diagnostic integration: the restricted delegate

Ask the service operator for an endpoint, an approved namespace scope, and a
credential reference available to the client's secret store. For the standard
host installation with diagnostic delegation enabled, the local endpoint is:

```text
Transport: Streamable HTTP MCP
URL: http://127.0.0.1:18082/mcp
Authorization: Bearer <credential resolved by the client runtime>
Default server-side credential file: /etc/infernex-agent/diagnostic-subagent-token
```

That path identifies a protected server credential; it is not a portable client
configuration or a request to print its contents. The operator must provision
the caller's access. Keep the token out of prompts, reports, committed MCP
configuration, command-line arguments, and logs. Configure the client's supported
secret/header mechanism; configuration syntax differs between clients.

Loopback refers to the machine running the client. For a caller on another host,
use an operator-provisioned SSH tunnel, mTLS gateway, or authenticated private
proxy. Token-only HTTP is not a public service boundary. This listener must be
enabled in `diagnose` or a higher server mode, with at least one configured scan
namespace. It may be disabled or configured on a different address.

Use an MCP SDK or the Agent runtime's MCP client, then perform this sequence:

1. Connect using Streamable HTTP and send `initialize` with supported protocol
   versions and client capabilities. Read the negotiated protocol version,
   server identity/version, and instructions.
2. Send `notifications/initialized`.
3. Request `tools/list`, following `nextCursor` until complete. Keep the returned
   input schemas and annotations; do not infer arguments from tool names.
4. Verify that `infernex_get_diagnostic_delegate_contract` is present, then call
   it with `{}`. Read `namespaceScope`, execution channels, action classes,
   Evidence/report rules, collection budgets, and exclusions before diagnosis.
   If absent, do not assume the endpoint is the restricted delegate.
5. Use the current tool schemas to choose environment detection, workload,
   event, and bounded log reads. Prefer `k8s_detect_environment` when advertised;
   use `openfuyao_detect_environment` for versions advertising only that name.

For protocol implementers, these are individual JSON-RPC messages, **not** a
client configuration file or an HTTP batch:

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"partner-agent","version":"1.0.0"}}}
```

```json
{"jsonrpc":"2.0","method":"notifications/initialized"}
```

```json
{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}
```

```json
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"infernex_get_diagnostic_delegate_contract","arguments":{}}}
```

Send each HTTP message by POST to the same `/mcp` URL, with the delegated
Authorization header, `Content-Type: application/json`, and
`Accept: application/json, text/event-stream`. On requests after initialization,
set `MCP-Protocol-Version` to the negotiated version. The example protocol
version is supported by the repository's pinned SDK; negotiate rather than
assuming that every deployed version has the same protocol support.

The current server uses stateless Streamable HTTP with JSON responses. There is
no separate `/sse` endpoint, and no persistent MCP session ID is required. A
standalone SSE GET receives HTTP 405 in this configuration; that is not evidence
that the POST-based MCP service is broken. Let a maintained MCP client handle
transport details and any negotiated session headers.

The delegate excludes deployment, recovery, experiments, generic
`k8s_read_resources`, and semantic-memory writes. It can still expose bounded
active probes and local Evidence/report writes. A token grants endpoint access;
it does not approve a capture, report, or change. The caller must enforce the
operator's approval policy before sending a mutating request or `confirm: true`.

## Trusted local stdio

For a trusted runtime on the management host, launch the binary as a child
process using the runtime's stdio MCP transport. Do not launch an interactive
`chat` or `tui` session as an MCP server. Replace every `/ABSOLUTE/...` placeholder
before use. Provision a dedicated writable state directory owned by the same
unprivileged OS user that will run the client and server, and a least-privilege
kubeconfig readable by that user:

```bash
/opt/infernex-agent/bin/infernex-agent serve \
  --transport=stdio \
  --kubeconfig=/ABSOLUTE/PATH/approved-kubeconfig \
  --state-dir=/ABSOLUTE/PATH/caller-owned-agent-state \
  --execution-mode=detect \
  --enable-deployment=false \
  --enable-auto-recovery=false \
  --enable-experiments=false
```

The binary path is the standard host installation location; use the verified
binary location for another installation. Configure the executable and argument
array in the client's native format. Do not pass
`--config=/etc/infernex-agent/agent.conf`: installed service configuration can
enable HTTP listeners, higher execution modes, or write capabilities that this
child process does not need. Omission of `--config` does not automatically load
that file. Preserve stdin/stdout exclusively for MCP and leave diagnostics on
stderr.

This starts a separate MCP server with its own state; it does not connect to the
running host service. `detect` disables the active diagnostic channel, but
report and semantic-memory tools may still be advertised with explicit
confirmation requirements. Enforce approvals according to live tool schemas
and annotations. Do not treat the entire tool surface as read-only merely
because the execution mode is `detect`. Kubernetes scope is controlled by the
provided identity; `--scan-namespaces` is not a universal access-control boundary
for the main MCP server.

## Main HTTP MCP and Dashboard

The standard host installation's main MCP endpoint is
`http://127.0.0.1:8080/mcp`. It has **no built-in authentication** and exposes the
main configured tool surface. Use it only within an operator-controlled trust
boundary, or behind an authenticated gateway with an approved tool policy. The
diagnostic delegate is a distinct restricted service, not an alias for it.

Bare `serve` defaults to `:8080`; explicitly choose a loopback/private bind when
starting it manually. The host installer's Dashboard default is
`0.0.0.0:8081` and may be changed or moved to another port. Dashboard/API access
must be controlled separately. `/healthz` and `/readyz` are liveness/readiness
checks, and Dashboard `/api/v1/snapshot` is a read-only summary. These are not
substitutes for MCP initialization, authentication checks, or `tools/list`, and
do not prove target workload health.

## Unreleased private inventory: local stdio only

Use this path only when the operator has explicitly installed a build containing
private inventory and initialized its protected state. It is not promised by
the published alpha.22 package. Storage requires Linux and a local filesystem,
not NFS. State is bound to a fixed scope and the effective UID that initialized
it; another UID, including root, cannot silently take over that scope.

After the operator registers and verifies the permitted Docker or Kubernetes
environment, launch:

```bash
/ABSOLUTE/PATH/infernex-agent serve \
  --transport=stdio \
  --private-state-directory=/ABSOLUTE/PATH/initialized-private-state \
  --private-inventory-only
```

Keep one child process alive for preview and pagination handles. This mode
publishes exactly the five private inventory tools: discover, list, get,
verify, and record. It bypasses the general Kubernetes initialization, so a
Docker-only environment needs neither kubeconfig, Helm, nor Bridge. A registered
Kubernetes environment loads its own approved kubeconfig only when needed.
Docker access is limited to the pre-registered local Unix socket and expected
daemon identity; the client cannot supply arbitrary endpoints or credentials.

These tools are not registered on either HTTP MCP endpoint. A separate private
inventory Dashboard does not turn them into remote MCP tools. Removing
`--private-inventory-only` enables the mixed local stdio mode, which again
requires the general Kubernetes configuration.

`infernex_record_domain_inventory` is a local write. It accepts only a
server-held `previewHandle` and the exact `digest`, with scope, subject,
Environment revision, and TTL checks. Persist only when the task authorizes
saving; otherwise obtain authorization before recording the preview. Handles
expire after five minutes and are lost when the process
restarts; do not move them between services or retry with reconstructed data.

## Connection failures and retries

- HTTP 401: stop and have the operator/runtime repair the credential reference;
  do not log or repeatedly guess the token.
- HTTP 429: respect `Retry-After` and retry with a bounded budget. The delegate
  defaults to four concurrent requests; the configured limit may differ.
- MCP scope/RBAC denial or a missing tool: report the unavailable capability;
  do not switch namespace, identity, endpoint, or shell to evade the limit.
- A tool result with `isError: true` is a failure even when HTTP succeeded.
  Inspect structured content or text, retain evidence IDs, and narrow the query
  when a result is truncated or exceeds its budget.
- After an ambiguous write timeout, inspect the returned task/change/report
  state before retrying. Starting another capture or mutation can duplicate
  work. A reconnect does not confer approval for additional actions.
