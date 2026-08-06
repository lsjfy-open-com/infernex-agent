# InferNex Agent

[English](README.md) | 简体中文

InferNex Agent 是运行在既有 InferNex 管理面的 AI Agent。用户用自然语言描述目标，Agent 主动发现环境、形成计划、调用受限领域工具、观察执行结果、诊断故障，并在失败时精确回退本次变更。

它不是 kubectl 参数生成器，也不要求普通用户配置 namespace、`catalogId`、YAML、镜像或容器命令。它复用已有的 `InferNexService`、`InferNexServiceConfig`、InferNex Bridge、infernex-checker、vLLM/vLLM-Ascend、Mooncake、PD Orchestrator、Hermes 和 Eagle-Eye，不重复实现推理服务生命周期。

## 最短使用路径

前提是用户已经构建好 InferNex，且管理节点上的 `kubectl` 能访问集群。

联网安装：

```bash
curl -fsSL https://raw.githubusercontent.com/lsjfy-open-com/infernex-agent/main/component/InferNex-Agent/scripts/install.sh | sudo bash
```

离线安装：把对应架构的宿主机包和 `.sha256` 传入管理节点，然后执行：

```bash
sha256sum --check infernex-agent-host-offline-*-linux-*.tar.gz.sha256
tar -xzf infernex-agent-host-offline-*-linux-*.tar.gz
cd infernex-agent-host-offline-*-linux-*
sudo ./install.sh
```

`aarch64` 使用 `linux-arm64` 包，`x86_64` 使用 `linux-amd64` 包。包内是静态二进制，不需要 Go、Python、Docker 或编译环境，也不会下载新的 vLLM-Ascend/Mooncake 镜像或模型权重。

安装器自动发现：

- 管理 kubeconfig；
- InferNex CRD 和 Bridge 模板命名空间；
- 已有模型实例所在的命名空间；
- 主机 CPU 架构；
- 可复用的稳定服务和已有 engine profile。

它自动创建专用 ServiceAccount、最小化 RBAC、独立 kubeconfig 和 `infernex-agent-workspace`。唯一需要用户输入的是 Agent 背后的 OpenAI 兼容模型接口：真实 Base URL、真实 model ID，以及可选 API Key。

安装完成后开始对话：

```bash
sudo infernex-agent chat
```

也可以先跳过模型配置，验证自动发现后再配置：

```bash
sudo ./install.sh --skip-model-setup
sudo infernex-agent setup
sudo infernex-agent chat
```

完整步骤见[产品使用指南](docs/product-guide-zh.md)。旧文档里的 `model-a`、`models` 和 `ops-model` 都是示例占位符，不是正常安装时需要填写的值。

## Agentic 工作方式

对话模型会使用无参数的 `infernex_list_all_services` 先扫描当前环境。部署请求会先调用 `infernex_list_deployment_sources`，从以下安全来源中选择：

- 当前代已 Ready、没有 Degraded 的既有 `InferNexService`；
- 管理员已经放入 Bridge 模板命名空间、且包含完整 engine 的 `InferNexServiceConfig`。

Agent 内部使用不透明 `sourceId`，用户不需要知道它。新实例只创建为标准 `InferNexService`，实际 Deployment/LeaderWorkerSet/Pod/Service 继续由 InferNex Bridge 管理。

部署前，本机终端展示写操作并要求精确输入 `yes`。部署后，Agent 跟踪持久化 `changeId`，检查 Ready、ObservedGeneration、Degraded、拓扑、Event 和可选日志证据。超时或当前代 Degraded 时，只撤销带有相同 Agent 所有权和 `changeId` 的本次新建实例。

如果集群里没有稳定服务或完整 engine profile，Agent 会说明缺少基线并给出建议，不会偷偷选择演示模型或凭空生成生产 YAML。这符合“稳定基线 + 一次受控变化”的第一版原则。

## 主要能力

- 全环境资产发现与持续扫描；
- InferNex 状态、工作负载、Pod 和 Event 关联；
- vLLM/vLLM-Ascend、NPU/CANN、HCCL/RDMA、Mooncake/KV、OOM、超时、流中断和输出损坏的受限日志诊断；
- 基于既有稳定配置的自然语言部署；
- 持久变更记录、Ready 门禁和失败回退；
- 单特性渐进实验与稳定基线对比；
- systemd 宿主机运行、MCP 服务、只读 Web Dashboard 和 JSON 快照。

模型接口不可用时，自然语言对话暂停，但确定性扫描、Dashboard、快照、变更记录和回退不依赖模型，仍然运行。

## Web Dashboard

宿主机默认监听 `127.0.0.1:8081`。从 XShell/SSH 使用端口转发：

```bash
ssh -L 8081:127.0.0.1:8081 <管理节点>
```

浏览器打开 `http://127.0.0.1:8081/`。Dashboard 没有内置登录认证，不应直接暴露到公网。

## 文档

- [产品使用指南：一键安装、离线安装和自然语言使用](docs/product-guide-zh.md)
- [产品设计与 Agent 边界](docs/product-design-zh.md)
- [变更保护、备份与回退](docs/change-safety-zh.md)
- [安全与能力边界](docs/security-boundaries-zh.md)
- [渐进式特性实验与跨节点诊断](docs/progressive-experiments-zh.md)
- [生产运维手册](docs/operations-runbook-zh.md)
- [高级 openEuler 宿主机安装](docs/host-install-openeuler-zh.md)
- [候选二进制验证](docs/candidate-validation-zh.md)

高级脚本和 Helm 参数用于安全审计、CI、定制 RBAC 与恢复，不是普通用户的首选入口。
