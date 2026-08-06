# InferNex Agent 产品使用指南

[English README](../README.md) | 简体中文

## 先说明：它是 Agent，不是参数生成器

InferNex Agent 运行在 InferNex 的 master、引导节点或独立管理节点上。用户通过自然语言说明目标，Agent 自己完成环境发现、计划、工具调用、状态观察、故障诊断和失败回退。

正常使用时，用户不需要填写 Kubernetes namespace、`catalogId`、工作负载 YAML 或镜像参数。旧文档中的 `model-a`、`models`、`ops-model` 都只是示例占位符，不应原样复制，也不属于正常安装流程。

Agent 的工作闭环是：

```text
自然语言目标
  → 自动发现 InferNex 环境和现有实例
  → 选择稳定实例或已有 InferNexServiceConfig 作为基线
  → 必要时只询问无法发现的业务信息
  → 展示受限变更并由本机用户批准
  → 交给 InferNex Bridge 拉起服务
  → 持续检查 Ready、拓扑、事件和日志
  → 成功提交；失败自动撤销本次新建
  → 用自然语言给出结论和证据
```

Agent 不重复实现 InferNex Bridge、vLLM/vLLM-Ascend、Mooncake、PD Orchestrator、Hermes、Eagle-Eye 或 infernex-checker。它调用并关联这些已有能力。

## 使用前提

用户先完成 InferNex 集群建设，并在管理节点上确认：

```bash
kubectl get crd infernexservices.infernex.infernex.io
kubectl get infernexservices -A
```

管理节点需要 Linux/systemd、`kubectl`、`curl`、`tar` 和 `sha256sum`。安装 Agent 不需要 Go、Python、Docker、编译环境、NPU 驱动或新的推理镜像。

Agent 的对话模型需要提供 OpenAI 兼容接口，并支持 function/tool calling。用户安装时只需要知道三项模型接口信息：

- 真实 Base URL，例如内网服务的 `http://10.20.0.30:8000/v1`；
- 接口实际接受的模型名；
- API Key；接口不鉴权时可直接回车。

这里的“模型名”是 OpenAI 接口的真实 model ID，不是待部署推理实例的名称。

## 联网安装：一条命令

正式发布包含一键安装器的版本后，在 InferNex 管理节点执行：

```bash
curl -fsSL https://raw.githubusercontent.com/lsjfy-open-com/infernex-agent/main/component/InferNex-Agent/scripts/install.sh | sudo bash
```

安装器自动完成：

1. 根据 `uname -m` 选择 `linux-amd64` 或 `linux-arm64` 静态包；
2. 下载发布包和 SHA256，校验后解压；
3. 从当前用户、`/etc/kubernetes/admin.conf` 或 k3s 路径寻找可用 kubeconfig；
4. 检查 InferNex CRD，并发现 Bridge 模板命名空间；
5. 发现所有已有 `InferNexService` 所在命名空间；
6. 创建 `infernex-agent-workspace` 作为 Agent 新部署的固定工作区；
7. 创建专用 ServiceAccount、最小化 RBAC 和独立 kubeconfig，不让 Agent 长期持有 admin.conf；
8. 安装静态二进制和 systemd 服务；
9. 只提示填写 Agent 模型接口，并测试 chat completions 与 tool calling；
10. 在变更前保存集群源资源和原主机安装的校验备份。

安装过程中不需要用户选择 namespace，也不会下载 vLLM-Ascend、Mooncake 或模型权重镜像。推理实例继续复用既有 InferNex 配置中引用的镜像。

## 离线安装：拷入一个包，一条命令

在联网机器下载与服务器架构匹配的两个文件：

```text
infernex-agent-host-offline-<版本>-linux-arm64.tar.gz
infernex-agent-host-offline-<版本>-linux-arm64.tar.gz.sha256
```

`aarch64` 使用 `arm64` 包，`x86_64` 使用 `amd64` 包。把两个文件传到内网管理节点后执行：

```bash
sha256sum --check infernex-agent-host-offline-*-linux-*.tar.gz.sha256
tar -xzf infernex-agent-host-offline-*-linux-*.tar.gz
cd infernex-agent-host-offline-*-linux-*
sudo ./install.sh
```

离线包内含静态 Agent 二进制和安装脚本，不依赖编译环境。它不包含任何推理框架镜像或模型权重；已有 InferNex 集群的镜像不会被替换。

如果先只验证安装和自动发现，不立即配置模型：

```bash
sudo ./install.sh --skip-model-setup
sudo infernex-agent setup
```

`setup` 会再次只询问模型接口，测试失败时恢复原模型配置，不影响规则扫描和 Dashboard。

## 开始与 Agent 对话

安装完成后，在 XShell/SSH 中执行：

```bash
sudo infernex-agent chat
```

可以直接说：

```text
扫描整个 InferNex 环境，告诉我有哪些实例异常，先不要修改。

我想基于当前稳定的 Qwen PD 实例再部署一个测试实例，拉起后持续观察，失败就回退。

比较这个候选实例和稳定实例的跨节点日志，重点看 HCCL、Mooncake 和 vLLM worker 中断。
```

Agent 会先调用无参数的全环境发现工具。部署前，它会自动查找：

- 当前代已 Ready、无 Degraded 状态的现有 `InferNexService`；
- Bridge 中管理员已经创建、且包含完整 engine 的 `InferNexServiceConfig`。

它不会要求用户提供 namespace 或 `sourceId`。这些是 Agent 内部工具参数。用户只需在本机看到变更摘要后输入精确的 `yes`。单次非交互 `--ask` 模式始终禁止写操作。

### 为什么第一版从稳定基线部署

InferNex 的真实 vLLM-Ascend/PD 配置包含镜像、容器命令、并行度、NPU、Mooncake 和网络等强关联设置。仅凭一个模型路径让大模型凭空生成生产 YAML 不安全，也会重复实现 InferNex。

因此第一版的智能部署遵循“上一份稳定配置 + 一次受控变化”：优先克隆一个实际 Ready 的服务配置，或引用集群管理员已有的 Bridge profile。若环境中没有任何稳定服务或完整 engine profile，Agent 会明确说明缺少部署基线并给出建档建议，不会偷偷使用演示模型或猜测生产参数。

## Web 展示

Dashboard 默认只监听管理节点的 `127.0.0.1:8081`。从 XShell/SSH 建立本地转发：

```bash
ssh -L 8081:127.0.0.1:8081 <管理节点>
```

然后在本机打开 `http://127.0.0.1:8081/`。这样无需把无内置登录认证的 Dashboard 暴露到整个网络。

确需绑定管理网地址时，可使用高级安装选项：

```bash
sudo ./install.sh --dashboard-listen-address 10.20.0.10:8081
```

必须同时配置主机防火墙、管理网 ACL 或带认证的反向代理。

## 自动发现的范围

首次安装会发现当时已有 InferNex 服务命名空间，并将它们写入专用 RBAC 和持续扫描范围；Agent 自己的新实例固定进入 `infernex-agent-workspace`。这样既接近 k9s 的开箱发现体验，也不把集群管理员权限长期交给模型。

如果集群后来新增了全新的业务命名空间，重新运行同一个 `sudo ./install.sh --skip-model-setup` 即可重新发现并扩充范围；已有模型接口配置会保留。后续版本会把这一动作纳入 Agent 的受控 RBAC 扩展审批流。

## 安全、回退与边界

- 安装前备份 Agent 管理范围内的 `InferNexService`/`InferNexServiceConfig` 源资源和现有主机文件；安装失败自动恢复。
- 每次 Agent 部署先写入持久变更记录和 `changeId`。
- 新服务在超时前未 Ready，或当前代报告 Degraded 时，只删除带有相同 Agent 所有权和 `changeId` 的本次新建资源。
- Agent 不覆盖用户已有服务，不删除无法证明所有权的对象，不直接创建 Deployment/Pod/Service。
- 模型不能提交任意 YAML、shell、镜像或 Kubernetes 对象；真正的工作负载仍由 InferNex Bridge 编排。
- 模型接口不可用时，自然语言对话暂停，但确定性扫描、Dashboard、快照、变更记录和回退仍工作。

详细边界见[安全与能力边界](security-boundaries-zh.md)，回退机制见[变更保护、备份与回退](change-safety-zh.md)。

## 高级维护接口

`create-kubeconfig.sh`、`install-host.sh` 以及 Helm 参数保留给安全审计、定制 RBAC、CI 和故障恢复使用，不是普通用户的安装步骤。需要精细控制时再阅读：

- [openEuler 宿主机高级安装](host-install-openeuler-zh.md)
- [集群内与离线高级安装](offline-install-zh.md)
- [模型接口配置与轮换](model-configuration-zh.md)
- [生产运维手册](operations-runbook-zh.md)
- [渐进式特性实验](progressive-experiments-zh.md)
