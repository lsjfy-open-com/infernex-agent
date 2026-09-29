# InferNex Agent

[简体中文](README-zh.md) · [Documentation](component/InferNex-Agent/docs/README.md)

A Kubernetes-first agent for inference deployment and operations, with optional InferNex, Helm and customer-platform adapters. It runs on a Linux management host using the active kubeconfig. InferNex CRDs are not required for native discovery, logs and diagnosis.

**Current boundary:** alpha.21 improves Host Pi TUI command classification for common read-only Kubernetes, Helm, Pod and SSH queries, and adds an explicit session-scoped `root risk` mode that removes per-operation prompts. Linux identity, Kubernetes RBAC, tool validation and background consistency checks still apply. It retains alpha.20's first read-only native deployment-planning slice; native deployment writes and request-level load balancing remain planned. See the [capability matrix and architecture](https://github.com/lsjfy-open-com/infernex-agent/blob/develop/component/InferNex-Agent/docs/architecture/kubernetes-first-zh.md).

alpha.21 keeps no-argument kubeconfig discovery for `sudo ./install.sh`, the Host Dashboard live-YAML view and alpha.20's read-only deployment planning. Its Linux identity and dual-architecture package gates have completed; hardware-site validation remains pending, and planning results remain estimates from the caller's current read permissions. See the [release notes](https://github.com/lsjfy-open-com/infernex-agent/blob/develop/component/InferNex-Agent/docs/releases/v0.5.0-alpha.21-zh.md) and [deployment-planning guide](https://github.com/lsjfy-open-com/infernex-agent/blob/develop/component/InferNex-Agent/docs/guides/deployment-planning-zh.md).

## Install the experimental release

Download the archive and matching SHA256 for your management host's CPU architecture from [v0.5.0-alpha.21](https://github.com/lsjfy-open-com/infernex-agent/releases/tag/infernex-agent-v0.5.0-alpha.21). Extract it and run `sudo ./install.sh`, then `sudo infernex-agent chat`. When already logged in as root, run `./install.sh`. The full package includes Pi, rg and fd; no Node or Go installation is needed.

Use the [installation guide](component/InferNex-Agent/docs/guides/offline-install-zh.md) for exact verification and upgrade steps. A release tag identifies an immutable experimental package, not a main-branch merge or hardware certification.

## Development

`develop` is the active development baseline; `main` retains the historical merged baseline pending explicit promotion. Start short-lived feature branches from develop. See [branch policy](component/InferNex-Agent/docs/development/branches-and-releases-zh.md), [current roadmap](component/InferNex-Agent/docs/development/roadmap-zh.md) and [CONTRIBUTING](CONTRIBUTING.md).

The repository retains upstream InferNex components for compatibility; their [original platform documentation](docs/upstream/README-en.md) is separate from Agent prerequisites. Source code lives in `component/InferNex-Agent/`.

License: [Mulan PSL v2](LICENSE).
