# InferNex Agent 产品使用指南

[English README](../README.md) | 简体中文

## 一句话定位

InferNex Agent 是运行在 InferNex 管理节点、master 节点或引导节点上的本地 AI
Agent。用户用自然语言提出目标，Agent 通过当前 `kubectl` 身份和 InferNex 已有 API
完成环境探索、部署计划、证据采集、故障诊断、变更确认、持续观察和失败回退。

它不是另一个推理框架、Kubernetes Controller 或必须常驻集群的 Pod，也不替代
InferNex Bridge、vLLM/vLLM-Ascend、Mooncake、PD Orchestrator、Hermes、Eagle-Eye
或 infernex-checker。

## 安装前提

- 已经建好的 InferNex 集群；
- 能在管理节点通过当前 `kubectl` context 访问目标业务集群；
- Linux amd64 或 arm64、systemd、`curl`、`tar` 和 `sha256sum`；
- 一个支持 function/tool calling 的 OpenAI 兼容模型接口。

不需要 Go、Python、Docker、编译环境、新推理镜像或模型权重。

## 在线安装：一条命令

在管理节点执行：

```bash
curl -fsSL https://raw.githubusercontent.com/lsjfy-open-com/infernex-agent/main/component/InferNex-Agent/scripts/install.sh | sudo bash
```

脚本会自动识别 CPU 架构和可用 kubeconfig，探测当前是 Bridge CRD 形态还是
openFuyao Helm/BKE 形态，安装静态二进制与 systemd 服务，然后只询问 Agent 自身使用的
模型接口。默认复用当前 `kubectl` 身份，不创建 Agent Pod、Controller、CRD、
ServiceAccount 或 RBAC。

只有检测到 Bridge CRD 时，安装器才会创建空的 `infernex-agent-workspace`
Namespace，用于后续经批准的新实例。没有 Bridge 的 Helm/BKE 集群进入
`generic-kubernetes` 基础兼容模式，安装阶段不修改任何 Kubernetes 资源。

基础兼容模式当前先打通安装、systemd、模型配置、对话入口、健康检查和 Web 端口；
Bridge 专属部署工具会关闭。面向 Helm release、Deployment/LWS、Pod、Service 及
底层组件日志的资产适配器属于紧随其后的功能，不应通过安装 Bridge CRD 来伪装兼容。

> 当前公开的 `0.3.0-rc.6` 仍是旧版候选包，不具备本页所述的新统一包名和默认流程。
> 在新候选完成 A2 既有集群验收并发布前，请从 Draft PR 的 CI Artifact 验证，不能把
> 上述在线命令当作已经可用的正式交付。

## Release 到底下载哪个

正式 Release 只提供一种产品包，区别仅是服务器 CPU 架构：

| `uname -m` | 下载文件 |
|---|---|
| `x86_64` | `infernex-agent-<版本>-linux-amd64.tar.gz` |
| `aarch64` | `infernex-agent-<版本>-linux-arm64.tar.gz` |

同时下载同名 `.sha256`。不存在需要普通用户选择的“宿主机包”和“集群包”。管理节点
是否同时是 Kubernetes master 不影响选择。

## 离线安装

把归档和校验文件传入内网管理节点：

```bash
sha256sum --check infernex-agent-*-linux-*.tar.gz.sha256
tar -xzf infernex-agent-*-linux-*.tar.gz
cd infernex-agent-*-linux-*
sudo ./install.sh
```

离线包自带静态二进制和脚本，不会联网拉镜像。详细说明见
[离线安装](offline-install-zh.md)。

## 只需要配置一次的模型接口

安装器会询问：

- Base URL，例如 `http://10.20.0.30:8000/v1`；
- 接口真实接受的 model ID；
- API Key，无鉴权可留空。

这里的 model ID 是 Agent 背后的对话/规划模型，不是要部署的推理实例名。也可先执行
`sudo ./install.sh --skip-model-setup`，稍后运行 `sudo infernex-agent setup`。

## 自然语言使用

在 XShell/SSH 中运行：

```bash
sudo infernex-agent chat
```

示例：

```text
扫描当前 InferNex 环境，关联实例、Pod、事件和近期异常日志，先不要修改。
基于当前稳定的 Qwen PD 实例创建一个测试实例，拉起失败就回退。
比较候选实例与稳定实例，重点检查 HCCL、Mooncake 和 vLLM worker 中断。
```

在已安装 Bridge 的集群中，Agent 会先探索当前环境，再选择已 Ready 的稳定服务或管理员
已有的完整 Bridge profile 作为部署来源。用户无需填写 namespace、`sourceId`、镜像、
容器命令或 YAML。只读工具自动执行；任何写操作都必须在本机展示摘要，并由用户输入
精确的 `yes` 批准。

在当前 Helm/BKE 基础兼容模式中，`chat`、模型接口和 Web 入口可以使用，但尚不会声称
已经发现或能够部署现有 Helm 模型实例；这需要后续 Helm release/LWS/Pod 资产适配器。

## Web 展示

Dashboard 默认监听 `127.0.0.1:8081`。通过 SSH 隧道访问：

```bash
ssh -L 8081:127.0.0.1:8081 <管理节点>
```

浏览器打开 `http://127.0.0.1:8081/`。如需绑定管理网地址，重新安装时传入
`--dashboard-listen-address <IP>:8081`，并自行配置防火墙、ACL 或认证代理。

## 自动发现、工具集与知识库

V1 采用业界常见的“本地 CLI Agent + 当前 kubeconfig + 受限工具集”结构：

- 自动识别 Bridge CRD 与 openFuyao Helm/BKE 两类部署入口；
- Bridge 模式发现 InferNexService、profiles、工作负载、Pod、事件和拓扑；
- Helm/BKE 模式后续从 release、Deployment/LWS、Pod、Service 和组件标签建立资产图；
- 通过 typed tools 调用 Kubernetes/InferNex API，不给模型任意 shell 或任意 YAML；
- 把 vLLM-Ascend、PD、Mooncake、HCCL/RDMA 等排障知识作为可迭代知识库和 runbook；
- 模型依据实时证据选择工具、关联多节点日志，并输出结论和建议。

参见[工具集与知识库设计](toolsets-and-knowledge-zh.md)。

## 回退和边界

- 安装覆盖前保存 Agent 管理范围内的集群源资源和本机文件恢复点；
- 每次部署先写持久变更记录和 `changeId`；
- 新实例未按时 Ready 或当前代报告 Degraded 时，只撤销本次 Agent 所有的新资源；
- Agent 不覆盖既有服务，不直接创建 Deployment/Pod/Service，不读取 Secret；
- 模型接口不可用时，对话暂停，但确定性扫描、Dashboard、变更记录和回退仍可工作。

更完整的保证见[变更保护与回退](change-safety-zh.md)和
[安全与能力边界](security-boundaries-zh.md)。

## 高级选项

Bridge 模式下，若默认身份不适合长期服务账户策略，可让同一个安装脚本创建专用的、
namespace-scoped 身份：

```bash
sudo ./install.sh --hardened-identity
```

当前 Helm/BKE 基础兼容模式还没有对应的最小权限规则集，因此暂不接受这个选项；请使用
当前 kubeconfig 完成首轮安装验证，后续资产适配器会同时给出所需 RBAC 清单。

Helm/Pod 形态仅保留给必须由 Kubernetes 管理 Agent 生命周期的高级场景，不是 V1
默认交付，也不出现在普通 Release 下载列表。高级维护说明见
[安装模式与高级选项](install-and-modes-zh.md)。
