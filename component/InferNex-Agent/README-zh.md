# InferNex Agent

[English](README.md) | [简体中文](README-zh.md)

InferNex Agent 是运行在 InferNex 管理面的受控运维 Agent。它复用已有的
`InferNexService`、`InferNexServiceConfig` 与 InferNex Bridge 状态，不重新实现
推理服务编排、PD 分离、vLLM/vLLM-Ascend、Mooncake 或工作负载控制器。

Agent 可以部署在 Kubernetes 集群内，也可以作为 systemd 服务运行在 master、
control-plane 或引导节点。它持续扫描指定命名空间，提供 MCP 工具、只读 Web
Dashboard、确定性诊断，以及可选的 OpenAI 兼容模型分析。

## 主要能力

- 观察 InferNex 服务、组件拓扑、Pod 就绪状态和 Kubernetes Event；
- 对服务所属 Pod 的当前/上一次容器日志进行限量、脱敏采集；
- 关联 vLLM、vLLM-Ascend、NPU/CANN、HCCL/RDMA、Mooncake/NIXL/KV、
  Worker 崩溃、OOM、超时、流中断和输出损坏信号；
- 从稳定基线开始，每次只增加一个管理员批准的特性配置，自动执行就绪、浸泡、
  基线对比和失败候选回退；
- 通过固定模型目录创建或删除受 Agent 所有权保护的 `InferNexService`；
- 在连续严重故障后，使用预先批准的配置创建独立恢复服务；
- 在执行写操作前保存持久变更记录，并在 Agent 重启后继续未完成的检查和回退。

模型不是必需组件。未配置模型时，采集、规则分类、实验门禁、回退、MCP、
Dashboard 和 JSON API 都可以正常工作；模型只提供附加的诊断建议，不能绕过
RBAC、固定目录、所有权校验或确认参数。

## 选择安装方式

| 环境 | 推荐方式 | Agent 运行位置 | 适用场景 |
| --- | --- | --- | --- |
| Kubernetes 可访问镜像仓库 | Helm 在线安装 | 集群内 Pod | 标准生产安装、统一调度和升级 |
| Kubernetes 完全离线 | 集群离线 Bundle | 集群内 Pod | 内网、隔离区、无法拉取外部镜像 |
| master/引导节点可联网 | 源码构建或下载宿主机包 | Linux systemd | 运维人员在集群外管理，MCP 默认仅本机可用 |
| openEuler aarch64 完全离线 | arm64 宿主机 Bundle | Linux systemd | Ascend A2 管理节点，不依赖容器或 NPU Runtime |

完整的前置检查、安装矩阵、模式配置和验收步骤见：
[在线、离线安装与运行模式指南](docs/install-and-modes-zh.md)。

## 下载即用（0.3.0-rc.6）

[Release 页面](https://github.com/lsjfy-open-com/infernex-agent/releases/tag/infernex-agent-v0.3.0-rc.6)
提供四个可安装包；每个包旁边都有同名 `.sha256`：

| 目标 | x86_64 / amd64 | aarch64 / arm64 |
| --- | --- | --- |
| 集群内 Pod | [集群 amd64 Bundle](https://github.com/lsjfy-open-com/infernex-agent/releases/download/infernex-agent-v0.3.0-rc.6/infernex-agent-offline-0.3.0-rc.6-linux-amd64.tar.gz) | [集群 arm64 Bundle](https://github.com/lsjfy-open-com/infernex-agent/releases/download/infernex-agent-v0.3.0-rc.6/infernex-agent-offline-0.3.0-rc.6-linux-arm64.tar.gz) |
| master/引导节点 systemd | [宿主机 amd64 Bundle](https://github.com/lsjfy-open-com/infernex-agent/releases/download/infernex-agent-v0.3.0-rc.6/infernex-agent-host-offline-0.3.0-rc.6-linux-amd64.tar.gz) | [宿主机 arm64 Bundle](https://github.com/lsjfy-open-com/infernex-agent/releases/download/infernex-agent-v0.3.0-rc.6/infernex-agent-host-offline-0.3.0-rc.6-linux-arm64.tar.gz) |

联网 Linux 可以让下载器自动识别 CPU 架构、下载 `.sha256`、校验并解压：

```bash
curl --fail --location --remote-name \
  https://raw.githubusercontent.com/lsjfy-open-com/infernex-agent/main/component/InferNex-Agent/scripts/download-bundle.sh
chmod +x download-bundle.sh

# 在 master/引导节点安装 systemd 服务
./download-bundle.sh --mode host

# 或准备集群内安装包
./download-bundle.sh --mode cluster
```

下载器不会自行安装或修改集群。完全离线时，在联网机执行以上下载，将归档和
`.sha256` 一起传入内网，再按下文安装。

## 集群内在线安装

前提是 InferNex 与 Bridge 已经部署，并且集群存在
`infernexservices.infernex.infernex.io` CRD。下面的默认模式只观察 `models`
命名空间，不读取 Secret、不读取 Pod 日志，也不创建或删除业务资源：

```bash
git clone --branch infernex-agent-v0.3.0-rc.6 --depth 1 \
  https://github.com/lsjfy-open-com/infernex-agent.git
cd infernex-agent/component/InferNex-Agent

helm upgrade --install infernex-agent ./chart/infernex-agent \
  --namespace infernex-system \
  --create-namespace \
  --set-string 'rbac.targetNamespaces[0]=models' \
  --atomic --wait
```

Chart 默认使用 `values.yaml` 中声明的镜像仓库。生产环境建议先将镜像构建并推送
到组织镜像仓库，再通过 `image.repository` 和 `image.tag` 显式指定。完整命令见
[在线安装](docs/install-and-modes-zh.md#3-集群内在线安装helm)。

访问本地 Dashboard：

```bash
kubectl --namespace infernex-system port-forward \
  service/infernex-agent-dashboard 8081:8081
```

浏览器打开 `http://127.0.0.1:8081/`。MCP 地址为集群内
`http://infernex-agent.infernex-system.svc:8080/mcp`。

## 集群内离线安装

从 [GitHub Releases](https://github.com/lsjfy-open-com/infernex-agent/releases)
在联网环境下载与目标架构匹配的集群 Bundle 和 `.sha256` 文件，传入内网后执行：

```bash
sha256sum --check \
  infernex-agent-offline-0.3.0-rc.6-linux-arm64.tar.gz.sha256
tar -xzf infernex-agent-offline-0.3.0-rc.6-linux-arm64.tar.gz
cd infernex-agent-offline-0.3.0-rc.6-linux-arm64

./bin/install-agent.sh \
  --target-node master-01 \
  --target-namespace models \
  --dashboard-cidr 10.20.0.0/16 \
  --runtime ctr
```

该安装器只安装 Agent，不会重装或修改 InferNex Bridge、NPU 驱动、固件、网关、
模型权重或现有推理工作负载。详见
[离线构建与既有集群安装](docs/offline-install-zh.md)。

## 宿主机安装

宿主机模式使用专用、命名空间级 kubeconfig，不应直接使用 `admin.conf` 或
cluster-admin。以 openEuler aarch64 为例：

```bash
sha256sum --check \
  infernex-agent-host-offline-0.3.0-rc.6-linux-arm64.tar.gz.sha256
tar -xzf infernex-agent-host-offline-0.3.0-rc.6-linux-arm64.tar.gz
cd infernex-agent-host-offline-0.3.0-rc.6-linux-arm64

./bin/create-kubeconfig.sh \
  --target-namespace models \
  --output /root/infernex-agent-host.kubeconfig

sudo ./bin/install-host.sh \
  --kubeconfig /root/infernex-agent-host.kubeconfig \
  --scan-namespace models
```

安装器会在 `/var/lib/infernex-agent/backups/` 创建带校验和的安装前恢复点，并在
安装或启动验证失败时恢复原有文件、systemd 状态和 Agent 管理的集群源资源。
MCP 与 Dashboard 默认只监听 `127.0.0.1`。完整说明见
[openEuler 管理/引导节点宿主机部署](docs/host-install-openeuler-zh.md)。

## 运行模式

所有写能力默认关闭，建议从只读模式开始逐项启用：

| 模式 | 主要配置 | 是否写集群 | 是否需要模型 |
| --- | --- | --- | --- |
| 只读观察 | 默认配置、`rbac.targetNamespaces` | 否 | 否 |
| 日志关联诊断 | `supervisor.diagnostics.logs.enabled=true` | 否 | 否 |
| 模型辅助分析 | OpenAI 兼容 `baseURL` 与 `model` | 否 | 是 |
| 固定目录部署 | `tools.deployment.enabled=true` | 仅受控创建/删除服务 | 否 |
| 受控自动恢复 | `supervisor.remediation.enabled=true` | 仅创建批准的恢复服务 | 否 |
| 渐进式特性实验 | `experiments.enabled=true` | 创建候选；失败时精确删除候选 | 否 |

写模式必须使用命名空间级 RBAC，并为 `/var/lib/infernex-agent` 配置持久卷。
实验模式还要求启用日志诊断、`replicaCount=1`，并预留同时运行基线和候选实例的
资源。各模式的 Helm、离线安装器和宿主机参数见
[运行模式配置](docs/install-and-modes-zh.md#6-运行模式与配置)。

## 接入内部模型

模型端点需要兼容 `POST /v1/chat/completions`。集群内安装使用已有 Secret，
API Key 不写入 Helm values：

```bash
kubectl --namespace infernex-system create secret generic infernex-agent-openai \
  --from-file=api-key=/secure/infernex-agent-openai.key

helm upgrade infernex-agent ./chart/infernex-agent \
  --namespace infernex-system \
  --reuse-values \
  --set-string supervisor.analysis.openAI.baseURL=http://llm.internal:8000/v1 \
  --set-string supervisor.analysis.openAI.model=ops-model \
  --set-string supervisor.analysis.openAI.existingSecret=infernex-agent-openai \
  --atomic --wait
```

宿主机安装后可以独立配置、测试、轮换或禁用模型，无需重新安装 Agent：

```bash
sudo /opt/infernex-agent/bin/configure-model.sh \
  --base-url http://llm.internal:8000/v1 \
  --model ops-model \
  --api-key-file /secure/infernex-agent-openai.key \
  --test-tools --show
```

然后直接在 XShell/SSH 会话中进入自然语言终端：

```bash
sudo /opt/infernex-agent/bin/chat.sh
```

例如输入“扫描 models 命名空间并解释异常”“分析 qwen-pd 最近一次中断”。只读
工具自动执行；部署、删除、恢复和实验等写工具会显示工具名与参数，只有本地运维
人员精确输入 `yes` 才执行。`--ask '问题'` 可用于一次性查询，但该模式固定拒绝
所有写操作。

交互模型除兼容 Chat Completions 外，还必须支持 OpenAI function/tool calling。
当前自然语言部署仍遵守固定目录和批准 profile 边界，不能输入任意 YAML、镜像或
Shell。现有 openEuler vLLM-Ascend 0.23.0 镜像无需重新下载：已运行的实例可直接被
扫描和诊断，新增候选可通过引用该镜像的既有、管理员批准
`InferNexServiceConfig` 交给 InferNex Bridge 拉起。

详见[模型配置手册](docs/model-configuration-zh.md)。

## MCP 工具与 Web 接口

默认只读 MCP 工具：

- `infernex_list_services`
- `infernex_inspect_service`
- `infernex_get_topology`
- `infernex_get_events`

按模式启用后还可提供：

- `infernex_diagnose_service`
- `infernex_deploy_model`、`infernex_delete_model`、`infernex_get_change`
- `infernex_start_experiment`、`infernex_get_experiment`、
  `infernex_list_experiments`

主要 HTTP 端点：

- MCP：`:8080/mcp`
- 健康检查：`:8080/healthz`、`:8080/readyz`
- Dashboard：`:8081/`
- 快照：`:8081/api/v1/snapshot`
- 实验状态：`:8081/api/v1/experiments`

## 验证

```bash
kubectl --namespace infernex-system rollout status \
  deployment/infernex-agent --timeout=5m
kubectl --namespace infernex-system get pods,service
curl --fail http://127.0.0.1:8081/readyz
curl --fail http://127.0.0.1:8081/api/v1/snapshot
```

源码测试：

```bash
go test ./...
go vet ./...
go build ./cmd/infernex-agent
```

## 文档

- [在线、离线安装与运行模式指南](docs/install-and-modes-zh.md)
- [产品使用、部署选型与验收](docs/product-guide-zh.md)
- [产品设计与故障语义](docs/product-design-zh.md)
- [渐进式特性实验与跨节点故障关联](docs/progressive-experiments-zh.md)
- [变更保护、备份与回退](docs/change-safety-zh.md)
- [模型配置、换模、测试和密钥轮换](docs/model-configuration-zh.md)
- [离线构建与既有集群安装](docs/offline-install-zh.md)
- [openEuler 管理/引导节点宿主机部署](docs/host-install-openeuler-zh.md)
- [安全、数据与写能力边界](docs/security-boundaries-zh.md)
- [生产运维手册](docs/operations-runbook-zh.md)
- [架构与组件边界（英文）](docs/architecture.md)

## 安全边界

InferNex Agent 不是通用 Kubernetes 自动执行器。当前版本不会接受任意 YAML、镜像、
命令、URL 或 Shell，不会修改 InferNex Bridge，不会自动切换生产流量，也不会根据
大模型输出决定是否回退。日志诊断只读取选定服务所属 Pod；主动推理回放、业务语义
正确性、节点级 CANN/device-plugin 日志和 Eagle-Eye 适配仍属于后续能力。
