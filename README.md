# InferNex Agent

[简体中文](README-zh.md) · [Documentation](component/InferNex-Agent/docs/README.md) · [External Agent integration](component/InferNex-Agent/docs/guides/external-agent-integration-zh.md) · [Agent Skill](skills/infernex-operations/SKILL.md)

A Kubernetes-first agent for inference deployment and operations, with optional InferNex, Helm and customer-platform adapters. It runs on a Linux management host using the active kubeconfig. InferNex CRDs are not required for native discovery, logs and diagnosis.

**Current boundary:** alpha.22 fixes Pi TUI context compaction during continuous tool calls and resumes the task once from the compacted session. It retains alpha.21's Host command classification and explicit session-scoped `root risk` mode, plus alpha.20's first read-only native deployment-planning slice. Linux identity, Kubernetes RBAC, tool validation and background consistency checks still apply; native deployment writes and request-level load balancing remain planned. See the [capability matrix and architecture](component/InferNex-Agent/docs/architecture/kubernetes-first-zh.md).

alpha.22 keeps no-argument kubeconfig discovery for `sudo ./install.sh`, the Host Dashboard live-YAML view and alpha.20's read-only deployment planning. Its Linux tests and dual-architecture package gates have completed; hardware-site validation remains pending, and planning results remain estimates from the caller's current read permissions. See the [release notes](component/InferNex-Agent/docs/releases/v0.5.0-alpha.22-zh.md) and [context-management guide](component/InferNex-Agent/docs/guides/context-management-zh.md).

## Install the experimental release

Download the archive and matching SHA256 for your management host's CPU architecture from [v0.5.0-alpha.22](https://github.com/lsjfy-open-com/infernex-agent/releases/tag/infernex-agent-v0.5.0-alpha.22). Extract it and run `sudo ./install.sh`, then `sudo infernex-agent chat`. When already logged in as root, run `./install.sh`. The full package includes Pi, rg and fd; no Node or Go installation is needed. After an upgrade, exit any old TUI and start it again so the launcher writes the new compaction settings.

Use the [installation guide](component/InferNex-Agent/docs/guides/offline-install-zh.md) for exact verification and upgrade steps. A release tag identifies an immutable experimental package, not a main-branch merge or hardware certification.

## Integrate another Agent

Give the calling Agent the self-contained [infernex-operations Skill](skills/infernex-operations/SKILL.md) and configure an operator-provisioned MCP connection. The skill covers when to call, environment requirements, tool selection, examples and failure handling. Start with the [partner integration guide](component/InferNex-Agent/docs/guides/external-agent-integration-zh.md); use the authenticated, namespace-scoped diagnostic delegate for diagnostic integrations. The main HTTP MCP endpoint has no built-in authentication.

The existing CANN/HiXL skills are server-side diagnostic knowledge. The new caller-side skill is distributed separately from the alpha.22 runtime package.

**Development status, checked 2026-11-02:** `codex/private-deployment-evolution` at `6bf0c0e` adds read-only Docker/Kubernetes discovery, persistent private inventory, local CLI/stdio tools, and an authenticated inventory Dashboard with snapshot diffs. These changes are not in alpha.22. Typed Docker/native Kubernetes deployment, PD orchestration and capability self-evolution remain planned. See the [current roadmap](component/InferNex-Agent/docs/development/roadmap-zh.md).

## Development

`develop` is the active development baseline; `main` retains the historical merged baseline pending explicit promotion. Start short-lived feature branches from develop. See [branch policy](component/InferNex-Agent/docs/development/branches-and-releases-zh.md), [current roadmap](component/InferNex-Agent/docs/development/roadmap-zh.md) and [CONTRIBUTING](CONTRIBUTING.md).

The repository retains upstream InferNex components for compatibility; their [original platform documentation](docs/upstream/README-en.md) is separate from Agent prerequisites. Source code lives in `component/InferNex-Agent/`.

License: [Mulan PSL v2](LICENSE).
