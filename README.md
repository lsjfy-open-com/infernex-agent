# InferNex Agent

[简体中文](README-zh.md) · [Documentation](component/InferNex-Agent/docs/README.md)

A Kubernetes-first agent for inference deployment and operations, with optional InferNex, Helm and customer-platform adapters. It runs on a Linux management host using the active kubeconfig. InferNex CRDs are not required for native discovery, logs and diagnosis.

**Current boundary:** alpha.13 provides the full Pi TUI, evidence/diagnostic tools and guarded Bridge deployment/recovery. Native resource-aware deployment and request-level load balancing are planned, not yet implemented. See the [capability matrix and architecture](component/InferNex-Agent/docs/architecture/kubernetes-first-zh.md).

## Install the experimental release

Download the archive and matching SHA256 for your management host's CPU architecture from [v0.5.0-alpha.13](https://github.com/lsjfy-open-com/infernex-agent/releases/tag/infernex-agent-v0.5.0-alpha.13). Extract it and run `sudo ./install.sh`, then `sudo infernex-agent chat`. The full package includes Pi, rg and fd; no Node or Go installation is needed.

Use the [installation guide](component/InferNex-Agent/docs/guides/offline-install-zh.md) for exact verification and upgrade steps. A release tag identifies an immutable experimental package, not a main-branch merge or hardware certification.

## Development

`develop` is the active development baseline; `main` retains the historical merged baseline pending explicit promotion. Start short-lived feature branches from develop. See [branch policy](component/InferNex-Agent/docs/development/branches-and-releases-zh.md), [current roadmap](component/InferNex-Agent/docs/development/roadmap-zh.md) and [CONTRIBUTING](CONTRIBUTING.md).

The repository retains upstream InferNex components for compatibility; their [original platform documentation](docs/upstream/README-en.md) is separate from Agent prerequisites. Source code lives in `component/InferNex-Agent/`.

License: [Mulan PSL v2](LICENSE).
