# InferNex Agent

[English](README-en.md) | [简体中文](README-zh.md)

[![InferNex Agent CI](https://github.com/lsjfy-open-com/infernex-agent/actions/workflows/infernex-agent.yaml/badge.svg)](https://github.com/lsjfy-open-com/infernex-agent/actions/workflows/infernex-agent.yaml)
[![License](https://img.shields.io/badge/License-Mulan_PSL_v2-blue.svg)](LICENSE)

This repository keeps the InferNex project structure intact and adds
**InferNex Agent** as an openFuyao management-host component. The Agent first
discovers the Kubernetes API selected by the active kubeconfig, then exposes
bounded Kubernetes, Helm, and optional InferNex Bridge tools to an agentic
runtime. `InferNexService` is supported when Bridge is actually installed; it
is not treated as a prerequisite for an openFuyao inference cluster.

## Agent v0.4 candidate

For normal management-node use, InferNex Agent is an agentic runtime rather
than a parameter-driven Kubernetes utility. Install it with one command, let it
discover the existing InferNex environment, configure only its
OpenAI-compatible model interface, then work through natural language:

```bash
curl -fsSL https://raw.githubusercontent.com/lsjfy-open-com/infernex-agent/main/component/InferNex-Agent/scripts/install.sh | sudo bash
sudo infernex-agent chat
```

> The public `0.3.0-rc.6` Release is the legacy candidate and does not yet
> contain this unified package. Use the Draft PR CI Agent Artifact for existing
> cluster acceptance; publish and merge only after that acceptance passes.

See the [Chinese product guide](component/InferNex-Agent/docs/product-guide-zh.md).
Manual namespaces and values such as `model-a` in advanced documents are
examples, not normal installation inputs. The default product is one local
Linux Agent on the management node, using the current kubeconfig. It does not
install an Agent Pod, controller, CRD, ServiceAccount, or RBAC. openFuyao
Helm/BKE clusters without InferNex Bridge CRDs enter a read-only general
Kubernetes/Helm mode instead of failing installation. A kubeconfig selects one
API server, so a co-located bootstrap K3s and business K8s cluster must be
inspected with their respective kubeconfigs.

The Agent can now run continuously on an InferNex management or Kubernetes
control-plane node. Its supervisor scans explicit namespaces, correlates
InferNex status, managed topology, Pod evidence, and recent Events, and serves
a read-only dashboard and JSON snapshot API on a separate port. An optional
OpenAI-compatible endpoint adds cached diagnostic advice without receiving
Kubernetes credentials.

The default MCP boundary is deliberately read-only. It publishes six general
openFuyao/Kubernetes/Helm tools:

- `openfuyao_detect_environment`
- `k8s_cluster_overview`
- `k8s_list_workloads`
- `k8s_get_events`
- `k8s_get_pod_logs`
- `helm_list_releases`

When InferNex Bridge exists, it also publishes its five domain tools:

- `infernex_list_services`
- `infernex_inspect_service`
- `infernex_get_topology`
- `infernex_get_events`

An explicit, namespace-scoped deployment mode adds source discovery plus two
guarded write tools:

- `infernex_list_deployment_sources` (read-only)
- `infernex_deploy_model`
- `infernex_delete_model`

The Agent automatically chooses from existing Ready services or
administrator-created `InferNexServiceConfig` engine profiles and deploys into
its fixed workspace. The normal user does not provide a namespace or catalog
ID. It cannot accept arbitrary images, commands, URLs, YAML, shell, or
`kubectl`. The Agent creates only the canonical `InferNexService`; InferNex
Bridge remains responsible for the Deployment, Service, status, and garbage
collection. A CPU-only SmolLM fixture remains for free Kind CI only.

The Helm chart remains an advanced lifecycle option and is not the default V1
installation or a normal Release asset.

An additional double-opt-in recovery mode can create a new
`InferNexService` after repeated critical scans. The source service must name
an operator-approved `InferNexServiceConfig`; the Agent cannot create the
profile, overwrite the source, switch traffic, or submit arbitrary workload
fields.

An advanced Kubernetes profile can schedule the Agent Pod on a
control-plane/master node and expose only the dashboard as NodePort `30081`;
the MCP Service remains internal. See
[`values-master-node.yaml`](component/InferNex-Agent/chart/infernex-agent/values-master-node.yaml).

The default static Agent runs as a hardened, non-root systemd service on an
openEuler master/bootstrap or other management host. It uses the current
kubeconfig by default, keeps MCP and the dashboard on loopback, and does not
require a container or NPU runtime. A dedicated namespace-scoped identity is
an optional hardened installation policy, not a separate package.

## Documentation

- [产品使用说明、部署选型和验收](component/InferNex-Agent/docs/product-guide-zh.md)
- [产品设计和故障语义](component/InferNex-Agent/docs/product-design-zh.md)
- [工具集、知识库与业界方案取舍](component/InferNex-Agent/docs/toolsets-and-knowledge-zh.md)
- [模型配置、换模、测试和密钥轮换](component/InferNex-Agent/docs/model-configuration-zh.md)
- [安全、数据和写能力边界](component/InferNex-Agent/docs/security-boundaries-zh.md)
- [生产运维手册](component/InferNex-Agent/docs/operations-runbook-zh.md)
- [变更保护、备份与回退](component/InferNex-Agent/docs/change-safety-zh.md)
- [Agent overview, local development, and deployment](component/InferNex-Agent/README.md)
- [Architecture and component boundaries](component/InferNex-Agent/docs/architecture.md)
- [openFuyao v26.06 alignment baseline (Chinese)](component/InferNex-Agent/docs/openfuyao-alignment-zh.md)
- [Agent offline bundle and existing-cluster installation (Chinese)](component/InferNex-Agent/docs/offline-install-zh.md)
- [Agent openEuler host/systemd installation (Chinese)](component/InferNex-Agent/docs/host-install-openeuler-zh.md)
- [InferNex English documentation](README-en.md)
- [InferNex 中文文档](README-zh.md)

## Validation

The repository workflow runs Go race tests and vet, Helm lint/render checks,
builds the Agent and InferNex Bridge images, creates a real Kind cluster, and
exercises both the general and optional Bridge MCP tools. It also asks the Agent to deploy the catalog model,
waits for Bridge reconciliation, sends an OpenAI-compatible chat-completion
request to llama.cpp, observes the resulting topology through the Agent, and
deletes the service through the guarded tool.

No external Kubernetes environment is required for the repository CI.
