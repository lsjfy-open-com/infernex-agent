# InferNex Agent 在线、离线安装与运行模式指南

本文提供统一的安装与使用入口，覆盖 Kubernetes 集群内、master/引导节点宿主机、
联网和完全离线四类环境。深入的安全、回退和实验语义通过文末链接继续说明。

## 1. 先选择部署形态

| 选择 | Kubernetes 集群内 | master/引导节点宿主机 |
| --- | --- | --- |
| Agent 进程 | Deployment/Pod | 非 root systemd 服务 |
| 访问 Kubernetes | ServiceAccount | 专用命名空间级 kubeconfig |
| MCP 默认入口 | ClusterIP `:8080/mcp` | `127.0.0.1:8080/mcp` |
| Dashboard 默认入口 | ClusterIP `:8081` | `127.0.0.1:8081` |
| 安装回退 | Helm `--atomic` 与 release 历史 | 安装前恢复点和自动恢复 |
| 推荐场景 | 标准 Kubernetes 生命周期管理 | 运维入口机、不能或不希望把 Agent 放入集群 |

两种形态使用同一个 Agent 二进制和同一组领域能力。宿主机模式不要求容器 Runtime、
NPU Runtime 或驱动，但它访问的 Kubernetes API 必须可达。

## 2. 共同前置条件

1. InferNex 和 InferNex Bridge 已经安装；Agent 不替代 Bridge。
2. Kubernetes API 中存在 `infernexservices.infernex.infernex.io` CRD。
3. 启用自动恢复或实验时，还必须存在
   `infernexserviceconfigs.infernex.infernex.io` CRD。
4. 明确 Agent 可观察的命名空间，例如 `models`、`production-models`。
5. Dashboard 仅开放给内网运维网段；MCP 应保持集群内或本机访问。
6. 启用任何写模式前，为状态目录配置持久存储并完成恢复演练。

检查现有环境：

```bash
kubectl get crd infernexservices.infernex.infernex.io
kubectl get infernexservices.infernex.infernex.io --all-namespaces
kubectl get nodes -o wide
helm version
```

## 3. 集群内在线安装（Helm）

### 3.1 获取与版本固定

生产环境不要直接跟随未固定的分支。以下示例固定到 `0.3.0-rc.5`：

```bash
git clone --branch infernex-agent-v0.3.0-rc.5 --depth 1 \
  https://github.com/lsjfy-open-com/infernex-agent.git
cd infernex-agent/component/InferNex-Agent
```

### 3.2 准备镜像

Chart 默认镜像地址记录在 `chart/infernex-agent/values.yaml`。如果集群可以访问该
仓库，可以直接使用默认值。生产上更推荐构建并推送到组织内镜像仓库：

```bash
export AGENT_IMAGE_REPOSITORY=registry.internal.example/ai/infernex-agent
export AGENT_IMAGE_TAG=0.3.0-rc.5

make \
  IMG="${AGENT_IMAGE_REPOSITORY}:${AGENT_IMAGE_TAG}" \
  VERSION="${AGENT_IMAGE_TAG}" \
  docker-build
make IMG="${AGENT_IMAGE_REPOSITORY}:${AGENT_IMAGE_TAG}" docker-push
```

`make docker-build` 使用上一级 `component/` 作为构建上下文，以便复用
InferNex Bridge 的规范 CRD 类型。

### 3.3 安装默认只读模式

```bash
helm upgrade --install infernex-agent ./chart/infernex-agent \
  --namespace infernex-system \
  --create-namespace \
  --set-string image.repository="${AGENT_IMAGE_REPOSITORY}" \
  --set-string image.tag="${AGENT_IMAGE_TAG}" \
  --set-string 'rbac.targetNamespaces[0]=models' \
  --atomic --wait --timeout 5m --history-max 10
```

如直接使用 Chart 默认镜像，删除两个 `image.*` 参数即可。默认权限只能读取 release
命名空间或 `rbac.targetNamespaces` 明确列出的命名空间，不读取 Secret 或 Pod 日志，
不注册部署工具。

### 3.4 安装到 master/control-plane 节点

```bash
helm upgrade --install infernex-agent ./chart/infernex-agent \
  --namespace infernex-system \
  --create-namespace \
  --values ./chart/infernex-agent/values-master-node.yaml \
  --set-string image.repository="${AGENT_IMAGE_REPOSITORY}" \
  --set-string image.tag="${AGENT_IMAGE_TAG}" \
  --set-string 'rbac.targetNamespaces[0]=models' \
  --set-string 'networkPolicy.dashboardAllowedCIDRs[0]=10.20.0.0/16' \
  --atomic --wait --timeout 5m --history-max 10
```

该 profile 同时兼容 `node-role.kubernetes.io/control-plane` 和旧的
`node-role.kubernetes.io/master`，Dashboard 使用 NodePort `30081`。不要保留示例
profile 中的 `0.0.0.0/0`，生产安装必须覆盖为真实内网运维 CIDR。

### 3.5 验收

```bash
kubectl --namespace infernex-system rollout status \
  deployment/infernex-agent --timeout=5m
kubectl --namespace infernex-system get deployment,pod,service,networkpolicy

kubectl --namespace infernex-system port-forward \
  service/infernex-agent-dashboard 8081:8081
```

另一个终端执行：

```bash
curl --fail http://127.0.0.1:8081/readyz
curl --fail http://127.0.0.1:8081/api/v1/snapshot
```

## 4. 集群内离线安装

### 4.1 下载已发布 Bundle

在联网机器打开
[InferNex Agent Releases](https://github.com/lsjfy-open-com/infernex-agent/releases)，
下载与目标节点架构一致的两个文件：

- `infernex-agent-offline-<版本>-linux-amd64.tar.gz` 与 `.sha256`；或
- `infernex-agent-offline-<版本>-linux-arm64.tar.gz` 与 `.sha256`。

`x86_64` 对应 `amd64`，`aarch64` 对应 `arm64`。将两个文件一起传入内网，先验证：

```bash
sha256sum --check \
  infernex-agent-offline-0.3.0-rc.5-linux-arm64.tar.gz.sha256
tar -xzf infernex-agent-offline-0.3.0-rc.5-linux-arm64.tar.gz
cd infernex-agent-offline-0.3.0-rc.5-linux-arm64
```

### 4.2 安装到本地 containerd 所在 master

```bash
./bin/install-agent.sh \
  --target-node master-01 \
  --target-namespace models \
  --dashboard-cidr 10.20.0.0/16 \
  --runtime ctr
```

安装器会验证 Bundle 内部清单、导入 Agent 镜像、创建命名空间、使用
`helm upgrade --install --atomic --wait` 安装，并执行 RBAC、Deployment、健康接口和
Dashboard 快照验收。

如果节点使用 Docker：

```bash
./bin/install-agent.sh \
  --target-node master-01 \
  --target-namespace models \
  --dashboard-cidr 10.20.0.0/16 \
  --runtime docker
```

如果已经把镜像推入内网仓库：

```bash
./bin/install-agent.sh \
  --target-namespace models \
  --dashboard-cidr 10.20.0.0/16 \
  --skip-image-import \
  --agent-image registry.internal.example/ai/infernex-agent:0.3.0-rc.5
```

完整的多 master、额外镜像、containerd socket、更新和常见问题见
[离线构建与既有集群安装](offline-install-zh.md)。

## 5. master/引导节点宿主机安装

### 5.1 在线源码构建

要求 Go 1.24.5 或兼容版本。可以在目标 Linux 主机直接构建，也可以在联网构建机
交叉编译后把静态二进制和 `scripts/` 目录传到目标主机：

```bash
git clone --branch infernex-agent-v0.3.0-rc.5 --depth 1 \
  https://github.com/lsjfy-open-com/infernex-agent.git
cd infernex-agent/component/InferNex-Agent

CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath \
  -ldflags='-s -w -X main.version=0.3.0-rc.5' \
  -o ./bin/infernex-agent ./cmd/infernex-agent
```

先用当前管理员身份创建专用、命名空间级 kubeconfig：

```bash
./scripts/host/create-kubeconfig.sh \
  --target-namespace models \
  --output /root/infernex-agent-host.kubeconfig
```

然后安装 systemd 服务：

```bash
sudo ./scripts/host/install-host.sh \
  --binary ./bin/infernex-agent \
  --kubeconfig /root/infernex-agent-host.kubeconfig \
  --scan-namespace models
```

### 5.2 离线宿主机 Bundle

下载 `infernex-agent-host-offline-<版本>-linux-<架构>.tar.gz` 及其 `.sha256`，
传入目标主机：

```bash
sha256sum --check \
  infernex-agent-host-offline-0.3.0-rc.5-linux-arm64.tar.gz.sha256
tar -xzf infernex-agent-host-offline-0.3.0-rc.5-linux-arm64.tar.gz
cd infernex-agent-host-offline-0.3.0-rc.5-linux-arm64

./bin/create-kubeconfig.sh \
  --target-namespace models \
  --output /root/infernex-agent-host.kubeconfig

sudo ./bin/install-host.sh \
  --kubeconfig /root/infernex-agent-host.kubeconfig \
  --scan-namespace models
```

宿主机安装会：

- 创建不可登录的 `infernex-agent` 用户；
- 安装 hardened systemd unit；
- 默认仅监听 `127.0.0.1:8080` 和 `127.0.0.1:8081`；
- 在 `/var/lib/infernex-agent/backups/install-*` 保存安装前集群与宿主机恢复点；
- 在安装失败时自动尝试恢复；
- 将 API Key 独立保存为 `0600` 凭据，不写入普通配置文件。

验证：

```bash
sudo /opt/infernex-agent/bin/verify-host.sh \
  --kubeconfig /etc/infernex-agent/kubeconfig \
  --target-namespace models
systemctl status infernex-agent --no-pager
journalctl -u infernex-agent -n 100 --no-pager
```

在内网开放 Dashboard 时，必须同时使用主机防火墙限制来源：

```bash
sudo ./bin/install-host.sh \
  --kubeconfig /root/infernex-agent-host.kubeconfig \
  --scan-namespace models \
  --dashboard-listen-address 10.20.0.10:8081
```

不要把 MCP 监听地址改为公网地址。openEuler 的完整安装、升级、回退与卸载步骤见
[openEuler 管理/引导节点宿主机部署](host-install-openeuler-zh.md)。

## 6. 运行模式与配置

### 6.1 模式 A：只读观察（默认）

适合首次接入和生产基线观察。提供服务列表、状态、拓扑、Event、Dashboard 与快照，
不读取 Pod 日志、不调用模型、不写集群。

集群内关键配置：

```yaml
rbac:
  clusterWide: false
  targetNamespaces:
    - models
supervisor:
  enabled: true
tools:
  deployment:
    enabled: false
```

宿主机安装参数：

```bash
sudo ./bin/install-host.sh \
  --kubeconfig /root/infernex-agent-host.kubeconfig \
  --scan-namespace models
```

### 6.2 模式 B：日志关联诊断

该模式读取选定服务所属 Pod 的限量当前/上一次容器日志，进行脱敏、分类与时间关联。
由于日志可能包含 Prompt 或业务数据，必须单独授权。

集群内：

```bash
helm upgrade infernex-agent ./chart/infernex-agent \
  --namespace infernex-system --reuse-values \
  --set supervisor.diagnostics.logs.enabled=true \
  --set supervisor.maxDiagnosticsPerScan=10 \
  --atomic --wait
```

宿主机必须在创建 kubeconfig 和安装时同时启用对应权限：

```bash
./bin/create-kubeconfig.sh \
  --target-namespace models \
  --enable-log-diagnostics \
  --output /root/infernex-agent-host.kubeconfig

sudo ./bin/install-host.sh \
  --kubeconfig /root/infernex-agent-host.kubeconfig \
  --scan-namespace models \
  --enable-log-diagnostics \
  --max-diagnostics-per-scan 10
```

使用 `infernex_diagnose_service` 主动读取单个服务诊断；连续扫描默认每轮最多诊断
10 个退化服务，避免大面积故障时无限放大 Kubernetes 日志 API 压力。

### 6.3 模式 C：OpenAI 兼容模型辅助分析

模型只接收归一化问题摘要，不接收 Kubernetes 凭据、Secret、环境变量、节点名或原始
Event note。模型不可触发部署、回退或实验。

集群内：

```bash
kubectl --namespace infernex-system create secret generic infernex-agent-openai \
  --from-file=api-key=/secure/infernex-agent-openai.key

helm upgrade infernex-agent ./chart/infernex-agent \
  --namespace infernex-system --reuse-values \
  --set-string supervisor.analysis.openAI.baseURL=http://llm.internal:8000/v1 \
  --set-string supervisor.analysis.openAI.model=ops-model \
  --set-string supervisor.analysis.openAI.existingSecret=infernex-agent-openai \
  --atomic --wait
```

宿主机安装后配置：

```bash
sudo /opt/infernex-agent/bin/configure-model.sh \
  --base-url http://llm.internal:8000/v1 \
  --model ops-model \
  --api-key-file /secure/infernex-agent-openai.key \
  --timeout 60s \
  --test --show
```

模型不可用时，确定性诊断和其他 Agent 功能继续运行。详见
[模型配置手册](model-configuration-zh.md)。

### 6.4 模式 D：固定目录部署

该模式只接受 `namespace`、`name`、固定 `catalogId` 和 `confirm: true`，不能接收任意
镜像、URL、命令、Patch 或 YAML。新服务未在超时内 Ready 或当前 generation 报告
Degraded 时，Agent 校验所有权与 `changeId` 后删除本次新建服务。

生产 Helm 配置：

```yaml
rbac:
  clusterWide: false
  targetNamespaces:
    - models
tools:
  deployment:
    enabled: true
changeSafety:
  persistence:
    enabled: true
    existingClaim: infernex-agent-state
```

离线安装器：

```bash
./bin/install-agent.sh \
  --target-namespace models \
  --dashboard-cidr 10.20.0.0/16 \
  --enable-deployment \
  --deployment-readiness-timeout 10m \
  --state-existing-claim infernex-agent-state
```

宿主机 kubeconfig 与安装命令均需加 `--enable-deployment`。通过
`infernex_get_change` 查询 `committed`、`rolled-back`、`rollback-failed` 或
`apply-failed`。详见[变更保护、备份与回退](change-safety-zh.md)。

### 6.5 模式 E：受控自动恢复

这是确定性的双重 opt-in 能力：管理员先批准完整、版本化的
`InferNexServiceConfig`，再对源服务明确标注恢复意图，最后开启全局策略。Agent 只会
创建新的独立恢复服务，不修改或删除故障源服务，也不切换流量。

全局 Helm 配置：

```yaml
supervisor:
  remediation:
    enabled: true
    templateNamespace: infernex-bridge-system
    minCriticalScans: 3
changeSafety:
  persistence:
    enabled: true
    existingClaim: infernex-agent-state
```

批准 profile：

```bash
kubectl --namespace infernex-bridge-system label infernexserviceconfig \
  qwen-pd-recovery-v1 \
  agent.infernex.io/approved-recovery-profile=true
```

源服务 opt-in：

```bash
kubectl --namespace models annotate infernexservice qwen-pd \
  agent.infernex.io/auto-recovery=true \
  agent.infernex.io/recovery-profile=qwen-pd-recovery-v1 \
  agent.infernex.io/recovery-name=qwen-pd-recovery
```

宿主机模式同样要求在 `create-kubeconfig.sh` 和 `install-host.sh` 中加入
`--enable-recovery`。

### 6.6 模式 F：渐进式单特性实验

每个阶段在当前稳定服务基础上只前置一个管理员批准的稀疏
`InferNexServiceConfig`。候选必须通过 current-generation Ready、基线健康、诊断对比
和浸泡时间；失败时只删除所有权完全匹配的当前候选，原稳定基线不变。

前置约束：

- `rbac.clusterWide=false`；
- `replicaCount=1`，保证单写者；
- 日志诊断已启用；
- 状态 PVC 已启用；
- profile 位于 `experiments.templateNamespace` 且带批准标签；
- 集群有足够资源同时运行稳定基线与候选。

Helm 配置：

```yaml
replicaCount: 1
rbac:
  clusterWide: false
  targetNamespaces:
    - models
supervisor:
  diagnostics:
    logs:
      enabled: true
experiments:
  enabled: true
  templateNamespace: infernex-bridge-system
  readinessTimeout: 20m
  soakDuration: 5m
  diagnosticInterval: 30s
changeSafety:
  persistence:
    enabled: true
    existingClaim: infernex-agent-state
```

批准一个只表达单一变化的 profile：

```bash
kubectl --namespace infernex-bridge-system label infernexserviceconfig \
  feature-mooncake-kv-transfer-v1 \
  agent.infernex.io/approved-experiment-feature=true
```

通过 MCP 启动：

```json
{
  "namespace": "models",
  "baselineName": "qwen-pd-stable",
  "candidatePrefix": "qwen-pd-exp-202608",
  "featureProfiles": [
    "feature-vllm-ascend-prefix-cache-v1",
    "feature-mooncake-kv-transfer-v1"
  ],
  "confirm": true
}
```

数组顺序就是实验顺序。使用 `infernex_get_experiment`、
`infernex_list_experiments` 或 Dashboard `/api/v1/experiments` 查看进度。完整配置合并、
诊断分类、失败语义和上线建议见
[渐进式特性实验与跨节点故障关联](progressive-experiments-zh.md)。

## 7. MCP 客户端和 Web 使用

### 7.1 kubectl-ai

在 `~/.config/kubectl-ai/mcp.yaml` 配置：

```yaml
servers:
  - name: infernex
    url: http://infernex-agent.infernex-system.svc:8080/mcp
```

然后执行：

```bash
kubectl-ai --mcp-client
```

生产上建议 kubectl-ai 与 Agent 位于受限管理命名空间，并保持 MCP Service 为
ClusterIP。宿主机模式可让本机 MCP 客户端直接连接
`http://127.0.0.1:8080/mcp`。

### 7.2 Dashboard 与监控 API

- `/`：服务、问题、恢复和实验概览；
- `/api/v1/snapshot`：外部监控可读取的归一化快照；
- `/api/v1/experiments`：实验计划、阶段和回归分类；
- `/healthz`、`/readyz`：进程和依赖就绪检查。

Dashboard 是只读页面，但不是身份认证边界。通过 NodePort 或宿主机 IP 暴露时，必须
使用 NetworkPolicy、防火墙或带认证的反向代理限制来源。

## 8. 更新、回退与数据保留

### 8.1 Kubernetes

- 在线和离线安装统一使用 `helm upgrade --install --atomic --wait`；
- 更新失败时 Helm 恢复上一 release 或清理失败的首次安装；
- 用 `helm history infernex-agent -n infernex-system` 查看历史；
- 写模式的 `/var/lib/infernex-agent` 必须使用 PVC，不能依赖生产 Pod 的 `emptyDir`；
- Agent 只回退自己创建且所有权、实验 ID、阶段和 `changeId` 完全匹配的候选。

手动回退 Agent release：

```bash
helm history infernex-agent --namespace infernex-system
helm rollback infernex-agent <REVISION> \
  --namespace infernex-system --wait --timeout 5m
```

### 8.2 宿主机

每次安装前会生成：

```text
/var/lib/infernex-agent/backups/install-<UTC时间>-<PID>/
```

恢复时必须指定精确目录并明确确认：

```bash
sudo /opt/infernex-agent/bin/restore-host-install.sh \
  --backup-dir /var/lib/infernex-agent/backups/install-20260806T010203Z-1234 \
  --confirm
```

恢复点包含 Agent 管理范围内的 `InferNexService`/`InferNexServiceConfig` 源资源快照和
宿主机安装文件，不是 etcd、Secret、PVC、模型权重或整个集群的灾备。

## 9. 常见配置错误

### 实验 Helm 渲染失败

确认日志诊断已开启、`replicaCount=1`、template namespace 非空、使用命名空间级
RBAC，并为写状态启用 PVC。

### 宿主机启动后没有日志诊断权限

`install-host.sh --enable-log-diagnostics` 只启用进程能力；专用 kubeconfig 也必须由
带 `--enable-log-diagnostics` 的 `create-kubeconfig.sh` 创建。

### Dashboard NodePort 无法访问

检查 `networkPolicy.dashboardAllowedCIDRs`、主机防火墙、NodePort `30081` 和 Agent
是否确实调度到目标 master/control-plane 节点。

### 模型不可用

先检查 `/v1/chat/completions`、模型名、API Key 和 Agent 到内网端点的网络。模型失败
不会停止确定性诊断；可先禁用模型继续运行。

### 候选失败但基线不受影响

这是预期行为。实验永远创建独立候选，不修改原稳定服务，也不会自动切换生产流量。

## 10. 延伸文档

- [产品使用、部署选型与验收](product-guide-zh.md)
- [产品设计与故障语义](product-design-zh.md)
- [变更保护、备份与回退](change-safety-zh.md)
- [离线构建与既有集群安装](offline-install-zh.md)
- [openEuler 管理/引导节点宿主机部署](host-install-openeuler-zh.md)
- [模型配置手册](model-configuration-zh.md)
- [渐进式特性实验与跨节点故障关联](progressive-experiments-zh.md)
- [安全、数据与写能力边界](security-boundaries-zh.md)
- [生产运维手册](operations-runbook-zh.md)
