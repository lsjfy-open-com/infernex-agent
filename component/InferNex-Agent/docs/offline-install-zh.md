# InferNex Agent 离线构建与既有集群安装

> 在 master/引导节点安装第一版 Agent 时，请优先使用[产品使用指南](product-guide-zh.md)
> 中的宿主机离线包：校验、解压后执行 `sudo ./install.sh`，无需填写 namespace。
> 本文主要保留集群内 Pod/Helm 形态和高级离线维护流程。

本文适用于已经在内网 Kubernetes 中部署 InferNex、InferNex Bridge 和
PD 分离推理服务，仅新增管理节点 Agent 的场景。

本文安装的是 Kubernetes Pod/Helm 形态。若运维习惯是在 openEuler
master 或引导节点直接运行管理进程，请改用
[openEuler 宿主机/systemd 安装](host-install-openeuler-zh.md)；两种形态
共用同一 Agent 核心和 InferNex API，不需要同时安装。

离线包只包含：

- InferNex Agent 容器镜像；
- `infernex-agent` Helm Chart；
- 镜像导入、安装和验证脚本；
- 无密钥的既有集群 values 模板；
- Bundle 内文件校验和与许可证。

它不会重复安装 InferNex Bridge、网关、NPU 驱动或固件，也不默认携带
模型权重和推理镜像。Agent 仍然只创建 `InferNexService`，后续工作负载
编排、状态回写和垃圾回收继续由现有 InferNex Bridge 完成。

## 1. 总体流程

```text
联网 Linux 构建机
  └─ 构建/拉取 Agent 与可选恢复镜像
       └─ 生成 tar.gz + 外层 SHA256
            └─ 通过受控介质传入内网
                 └─ 在目标管理节点校验并导入镜像
                      └─ Helm 安装 Agent
                           └─ 自动验证 RBAC、Pod、MCP、Dashboard
```

一次性安装完成后，Agent 可以在无公网的环境持续扫描指定命名空间，
执行规则分析、调用内网 OpenAI 兼容模型给出诊断建议，并在显式启用且
服务和恢复模板双重授权后创建恢复用 `InferNexService`。

## 2. 联网侧构建 Bundle

### 2.1 前置条件

在联网 Linux 构建机准备：

- Bash、GNU `tar`、`sha256sum`；
- Helm 3 或 Helm 4；
- Docker、Podman 或 nerdctl；
- 能访问 Dockerfile 基础镜像和 Go 依赖。

从项目根目录执行：

```bash
cd component/InferNex-Agent

./scripts/offline/build-bundle.sh \
  --architecture amd64 \
  --agent-image docker.io/library/infernex-agent:0.3.0 \
  --output-dir ./dist
```

默认从当前检出的源码构建 Agent。输出为：

```text
dist/infernex-agent-offline-0.3.0-linux-amd64.tar.gz
dist/infernex-agent-offline-0.3.0-linux-amd64.tar.gz.sha256
```

昇腾管理节点为 `aarch64` 时使用 `--architecture arm64`。跨架构构建要求
容器工具已启用对应的 buildx/QEMU；更稳妥的做法是在同架构构建机上构建。

项目的 `InferNex Agent` PR CI 也会生成可部署的 `linux/amd64` Bundle
Artifact，保留 30 天。代码合入默认分支后，可手动运行
`InferNex Agent offline bundle` 工作流，同时构建 `amd64` 和 `arm64`；
只有显式选择 `publish_prerelease=true` 时才会创建带两种架构及外层
SHA256 的 GitHub Prerelease。

### 2.2 加入恢复配置依赖的业务镜像

基础包仅需要 Agent 镜像。若已批准的 `InferNexServiceConfig` 引用了内网
节点尚未缓存的推理、下载器、路由器或工具镜像，可创建清单：

```text
# recovery-images.txt
hub.oepkgs.net/openfuyao/mikefarah/yq:4.50.1
cr.openfuyao.cn/openfuyao/hermes-router:latest
quay.io/ascend/vllm-ascend:v0.18.0
```

然后构建：

```bash
./scripts/offline/build-bundle.sh \
  --architecture amd64 \
  --agent-image docker.io/library/infernex-agent:0.3.0 \
  --extra-images ./recovery-images.txt \
  --output-dir ./dist
```

镜像清单必须与现网批准的恢复模板逐项对应。模型权重建议保留在现有
PVC、只读共享存储或节点模型目录中，不要无条件放入通用 Agent 包。

### 2.3 复用已经导出的镜像归档

CI 或镜像供应链已经产生归档时，可跳过容器构建：

```bash
./scripts/offline/build-bundle.sh \
  --architecture amd64 \
  --agent-image registry.intra.example/infernex-agent:0.3.0 \
  --image-archive /secure/export/infernex-agent-images-amd64.tar \
  --output-dir ./dist
```

此模式由制作者保证归档包含 `image-references.txt` 对应的全部镜像。

## 3. 传输与完整性校验

将 `.tar.gz` 和同名 `.sha256` 一起通过组织批准的介质传入内网。先校验
外层归档，再解压：

```bash
sha256sum --check infernex-agent-offline-0.3.0-linux-amd64.tar.gz.sha256
tar -xzf infernex-agent-offline-0.3.0-linux-amd64.tar.gz
cd infernex-agent-offline-0.3.0-linux-amd64

./bin/verify-agent.sh --checksums-only
```

外层 SHA256 应从可信的项目 Release/CI 页面或独立供应链记录中取得。
包内 `SHA256SUMS` 用于检测传输或落盘损坏，不能代替发布者身份签名。

## 4. 安装前确认现有 InferNex 集群

在有目标集群管理员 kubeconfig 的管理节点执行：

```bash
kubectl get crd \
  infernexservices.infernex.infernex.io \
  infernexserviceconfigs.infernex.infernex.io

kubectl get infernexservices.infernex.infernex.io --all-namespaces
kubectl get nodes -L node-role.kubernetes.io/control-plane \
  -L node-role.kubernetes.io/master
```

至少必须存在 `InferNexService` CRD。仅在启用自动恢复时才要求
`InferNexServiceConfig` CRD。记录以下信息：

- Agent 要观察的业务命名空间，例如 `kserve`、`model-a`；
- 要运行 Agent 的 master/control-plane 节点名；
- 运维终端所在内网 CIDR，例如 `10.20.0.0/16`；
- 可选的内网 OpenAI 兼容服务 URL、模型名和 API Key；
- 已批准恢复模板所在命名空间，通常为 `infernex-bridge-system`。

## 5. 在当前 master 节点安装

### 5.1 单个管理节点、本地 containerd

下面的命令会校验包、把镜像导入当前节点的 `k8s.io` containerd
命名空间、安装 Helm Release，并运行部署后验证：

```bash
./bin/install-agent.sh \
  --target-node master-01 \
  --target-namespace kserve \
  --dashboard-cidr 10.20.0.0/16 \
  --runtime ctr
```

若 `ctr` 需要 root 权限，可拆成两步：

```bash
sudo ./bin/load-images.sh --runtime ctr

./bin/install-agent.sh \
  --skip-image-import \
  --target-node master-01 \
  --target-namespace kserve \
  --dashboard-cidr 10.20.0.0/16
```

k3s 使用 `--runtime k3s`；有 nerdctl 的标准 containerd 环境可使用
`--runtime nerdctl`。如果 socket 不是默认位置：

```bash
sudo ./bin/load-images.sh \
  --runtime ctr \
  --containerd-address /run/containerd/containerd.sock
```

### 5.2 同时观察多个推理命名空间

`--target-namespace` 可以重复，安装器只为这些命名空间创建只读 Role：

```bash
./bin/install-agent.sh \
  --target-node master-01 \
  --target-namespace model-a \
  --target-namespace model-b \
  --dashboard-cidr 10.20.0.0/16 \
  --runtime ctr
```

这不会授予集群级读权限，也不会读取 Secret。

### 5.3 配置内网 OpenAI 兼容模型

URL 应从 Agent Pod 网络可达，并以 OpenAI 兼容 `/v1` 为基地址：

```bash
./bin/install-agent.sh \
  --target-node master-01 \
  --target-namespace kserve \
  --dashboard-cidr 10.20.0.0/16 \
  --runtime ctr \
  --openai-base-url http://ops-model.ai-system.svc:8000/v1 \
  --openai-model ops-diagnostic-model \
  --openai-api-key-file /secure/infernex-agent-openai.key
```

安装器在本地读取 Key 并创建 Kubernetes Secret，Key 不会写入 Bundle、
Helm values、命令行参数或 Dashboard。若 Secret 已由内部密钥系统创建：

```bash
./bin/install-agent.sh \
  --skip-image-import \
  --target-node master-01 \
  --target-namespace kserve \
  --dashboard-cidr 10.20.0.0/16 \
  --openai-base-url http://ops-model.ai-system.svc:8000/v1 \
  --openai-model ops-diagnostic-model \
  --openai-existing-secret infernex-agent-openai
```

不配置模型时，持续扫描、规则分析、Dashboard 和受控恢复仍可离线运行；
只是不生成模型补充建议。

### 5.4 启用带持久回退的目录部署

写能力默认关闭。启用后，离线安装器会同时创建或挂载状态 PVC；如果集群没有
默认 StorageClass，应显式指定：

```bash
./bin/install-agent.sh \
  --target-node master-01 \
  --target-namespace kserve \
  --dashboard-cidr 10.20.0.0/16 \
  --runtime ctr \
  --enable-deployment \
  --deployment-readiness-timeout 10m \
  --state-storage-class local-path
```

如组织已经准备 PVC：

```bash
--state-existing-claim infernex-agent-state
```

每次新建会返回 `changeId`。未在时限内 Ready 或当前 generation 报告
Degraded 时，Agent 自动删除且只删除本次新建的 `InferNexService`。详细流程、
恢复命令和边界见[变更保护、备份与回退](change-safety-zh.md)。

### 5.5 启用跨节点诊断和渐进实验

以下命令会启用有界日志读取、持久实验计划和并行候选。必须预留候选所需 NPU：

```bash
./bin/install-agent.sh \
  --target-node master-01 \
  --target-namespace kserve \
  --dashboard-cidr 10.20.0.0/16 \
  --runtime ctr \
  --enable-experiments \
  --experiment-template-namespace infernex-bridge-system \
  --state-storage-class local-path
```

profile 由现有 InferNex 配置流程创建并带批准标签；Agent 不生成 vLLM、
vLLM Ascend 或 Mooncake 参数。完整设计、门禁和边界见
[渐进式特性实验与跨节点故障关联](progressive-experiments-zh.md)。

### 5.6 多 master 或使用内网镜像仓库

如果 Agent 可能被调度到多个 control-plane 节点，选择一种方式：

1. 在每个候选节点解压同一 Bundle，并运行 `sudo ./bin/load-images.sh`；
2. 把 Agent 镜像同步到内网仓库，并在安装时覆盖镜像：

```bash
./bin/install-agent.sh \
  --skip-image-import \
  --agent-image registry.intra.example/ops/infernex-agent:0.3.0 \
  --target-namespace kserve \
  --dashboard-cidr 10.20.0.0/16
```

本地只导入一个节点时必须传 `--target-node`，避免 Pod 被调度到没有镜像的
其他 master。内网仓库模式可不固定节点。

## 6. Web 与 MCP 访问

默认只对外暴露只读 Dashboard：

```text
http://<master-node-internal-ip>:30081/
```

`--dashboard-cidr` 会同时写入 NetworkPolicy。不要使用公网地址或
`0.0.0.0/0`。如不需要 NodePort：

```bash
./bin/install-agent.sh \
  --dashboard-cluster-ip \
  --target-namespace kserve \
  --target-node master-01 \
  --runtime ctr
```

随后可临时访问：

```bash
kubectl -n infernex-system port-forward \
  service/infernex-agent-dashboard 8081:8081
```

MCP 始终使用独立的 ClusterIP Service：

```text
http://infernex-agent.infernex-system.svc:8080/mcp
```

建议让内部的 kubectl-ai/Codex 类运行时从受限管理命名空间访问 MCP，
不要把 MCP 端口通过 NodePort 或公网入口暴露。

## 7. 启用受控自动恢复

安装时增加：

```bash
--enable-recovery \
--recovery-template-namespace infernex-bridge-system
```

恢复仍需要两个资源侧授权：

1. `InferNexServiceConfig` 带有
   `agent.infernex.io/approved-recovery-profile=true` 标签；
2. 源 `InferNexService` 带有以下精确注解：

```yaml
metadata:
  annotations:
    agent.infernex.io/auto-recovery: "true"
    agent.infernex.io/recovery-profile: qwen-pd-recovery-v1
    agent.infernex.io/recovery-name: qwen-pd-recovery
```

连续达到临界扫描阈值后，Agent 只会创建一个新的
`InferNexService` 并通过 `baseRef` 引用批准模板。它不会覆盖或删除源
服务、生成任意 YAML、自动切换流量，也不会执行模型输出的命令。

恢复模板引用的镜像和模型必须已经在内网可用。缺失的公网依赖不会由
Agent 绕过网络策略下载。

## 8. 验证、升级和回退

安装器会自动执行同等验证，也可以独立重跑：

```bash
./bin/verify-agent.sh \
  --namespace infernex-system \
  --release infernex-agent \
  --target-namespace kserve
```

验证内容包括：

- InferNex CRD 和 Helm Release；
- Agent Deployment rollout；
- 每个目标命名空间的 ServiceAccount 只读权限；
- MCP 与 Dashboard 的健康端点；
- Dashboard JSON 快照；
- Agent 实际运行节点。

新版本使用同一条 `install-agent.sh` 命令即可幂等升级。Helm 保留最近
10 个版本，必要时可回退：

```bash
helm -n infernex-system history infernex-agent
helm -n infernex-system rollback infernex-agent <REVISION> --wait
```

卸载只移除 Agent 自身资源：

```bash
helm -n infernex-system uninstall infernex-agent
```

它不会删除现有 InferNexService、Bridge、模型 PVC 或推理工作负载。

## 9. 常见问题

### `ImagePullBackOff`

确认镜像导入到了 Kubernetes 实际使用的运行时命名空间。containerd
通常为：

```bash
sudo ctr -n k8s.io images list | grep infernex-agent
```

还要确认 `--target-node` 与执行镜像导入的节点一致。

### Dashboard NodePort 无法访问

依次检查：

```bash
kubectl -n infernex-system get pod,service,networkpolicy -o wide
kubectl -n infernex-system logs deployment/infernex-agent
```

确认访问源地址属于安装时的 `--dashboard-cidr`，并确认节点防火墙只在
内网放行选定 NodePort。

### 有规则诊断但没有模型建议

只有发现问题的服务才会调用模型，且同一证据指纹会缓存。检查 Agent Pod
到内网模型 Service 的网络连通性、模型名、Secret key `api-key`，以及
模型服务是否实现 `/v1/chat/completions`。

### 自动恢复没有触发

检查全局 `--enable-recovery`、源服务两个必需注解、批准模板标签、模板
命名空间和连续临界扫描次数。`observedGeneration` 未追上当前 generation
时，Agent 会等待 Bridge 完成调和，不会抢先恢复。
