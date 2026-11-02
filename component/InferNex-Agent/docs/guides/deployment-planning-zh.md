# 部署规格资源规划

alpha.20 提供原生 Kubernetes 部署规划的第一个只读切片。它读取当前 kubeconfig 可见的 Node 和已调度 Pod，根据调用方提供的单实例 Profile 与实例数估算放置结果。它不会创建、修改或删除 Kubernetes 对象，不会预留资源，不会批准部署，也不依赖 InferNex Bridge。

这项能力回答的是“按当前可见快照和已支持的约束，这组同规格实例是否看起来放得下”。`resource-fit` 不是 Kubernetes 调度承诺，更不是模型已经 Ready、可以服务或达到吞吐与时延 SLO 的证明。

## 准备 Profile

[仓库中的 JSON 示例](https://github.com/lsjfy-open-com/infernex-agent/blob/develop/component/InferNex-Agent/examples/deployment-profile-alpha20.json)会随 host bundle 放入 `./examples/deployment-profile-alpha20.json`，离线解压后可以直接作为 `--profile` 输入：

```json
{
  "version": "infernex.openfuyao.io/v1alpha1",
  "image": "registry.example.com/inference/demo-server@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  "cpu": "8",
  "memory": "32Gi",
  "extendedResources": {
    "huawei.com/Ascend910": 2
  },
  "nodeSelector": {
    "kubernetes.io/os": "linux"
  }
}
```

这个文件只是格式和规划流程示例。`registry.example.com` 是示例域名，镜像摘要也是虚构值；该文件不是可部署制品、管理员批准的 Profile 或硬件性能证明，不能用于实际部署。

`--profile` 只接受最大 1 MiB 的普通 JSON 文件。解析器拒绝未知字段和多个 JSON 值；最小必填字段是 `version`、固定 SHA-256 摘要的 `image`、`cpu` 和 `memory`。

Profile 中的资源都是**每个实例**的需求：

- `cpu` 和 `memory` 使用 Kubernetes Quantity 格式。
- `extendedResources` 的值是每实例设备数；示例表示每个实例需要 2 张 `huawei.com/Ascend910` 卡。
- `nodeSelector` 和 `tolerations` 是第一切片可考虑的放置条件。
- `image` 固定为摘要形式并参与 Profile 身份计算，但规划器不会拉取或运行镜像。

`replicas` 不写进 Profile，而由本次请求单独提供。示例 Profile 配合 `--replicas 3` 表示 3 个实例，每实例 8 CPU、32 GiB 内存和 2 张卡，总需求为 24 CPU、96 GiB 内存和 6 张卡；它不表示一个 6 卡实例。alpha.20 只支持一次请求使用一种 Profile，不支持把不同规格的实例混在同一个计划里。

## 从 CLI 生成计划

在已安装 Agent 的管理节点运行：

```bash
sudo infernex-agent deployment-plan \
  --profile ./examples/deployment-profile-alpha20.json \
  --namespace inference \
  --replicas 3 \
  --kubeconfig /etc/kubernetes/admin.conf
```

命令只读取集群。`--namespace` 指定目标 namespace，规划器读取其中的 ResourceQuota 和 LimitRange；节点占用估算会读取当前身份可见的全局 Pod。`--kubeconfig` 选择快照来源。调用身份必须已有以下权限：get 目标 Namespace、list Nodes、跨 namespace list Pods，并在目标 namespace list ResourceQuotas 和 LimitRanges。Agent 不会自动创建或扩大 RBAC；默认的 namespace-scoped host/Helm 身份通常不足，cluster-wide Helm 身份也可能缺少 get Namespace 或 list ResourceQuotas/LimitRanges，缺少任一项都会报错且不生成 Plan。不要因为命令只读就授予不受限的集群管理员权限，应由现场管理员提供已批准且只包含必要读取项的身份。MCP 调用还受 Agent 已配置的 namespace 范围约束。

CLI 输出 JSON Plan，主要字段包括：

| 字段 | 含义 |
| --- | --- |
| `status` | `resource-fit`、`insufficient` 或 `blocked` |
| `requestedReplicas` / `placeableReplicas` | 请求实例数与当前估算可放置实例数 |
| `resourceEstimate.perReplica` / `totalRequested` | 规范化后的每实例资源需求与所有请求实例的总需求 |
| `placements` | 按节点给出的同规格实例数量及估算剩余资源 |
| `reasons` / `warnings` | 资源缺口、未支持约束或估算限制 |
| `profileHash` | 规范化 Profile 的摘要；Profile 变化时应变化 |
| `snapshotHash` | 本次规划所依据的可见资源快照摘要；资源或占用变化后可能变化。Profile 自身已含不支持的复杂约束、因此在读取集群前即 `blocked` 时省略 |
| `planHash` | 绑定 namespace、replicas、`profileHash` 和已有 `snapshotHash` 的确定性计划摘要；不包含生成时间 |
| `trafficVerified` | alpha.20 恒为 `false`，未验证实际流量分布 |
| `performanceVerified` | alpha.20 恒为 `false`，未量测吞吐、TTFT、TPOT 或其他 SLO |
| `reservationCreated` | alpha.20 恒为 `false`，没有资源预留 |

`planHash` 固定“哪个 namespace、多少实例、用什么规格、依据哪个快照”这次计划的关键输入。执行任何未来写操作前都必须重新读取集群并重新规划；旧 hash 不能锁定资源，也不能当作批准令牌。

## 从 MCP 请求同一类计划

启用 Kubernetes 工具集时，`k8s_plan_deployment` 接受内联 Profile、`namespace` 和 `replicas`，返回同一 Plan 结构。MCP 不接受客户端文件路径或 YAML；调用方应解析 JSON 后传入对应字段。它是 read-only、idempotent、closed-world 工具，工具本身不要求 root 或变更批准，不会生成可直接 `kubectl apply` 的 YAML，也不会授权后续写操作。

## 结果与失败边界

- `resource-fit` 只表示第一切片已建模的资源和条件在当前可见快照中可容纳全部实例。
- `insufficient` 表示已建模资源不足；查看 `placeableReplicas` 和 `reasons`，不要超卖或自动降低 Profile。
- `blocked` 表示 Profile 本身或成功读取的基础快照中存在不能安全计算的条件。PVC、affinity、ResourceClaim、Pod-level resources、LimitRange、作用域配额或节点上的未知资源占用等第一切片尚未可靠建模的条件按 unknown 处理并 fail closed，不能把未知当成空闲。
- kubeconfig 无效、认证失败、缺少读取权限或输入非法时，CLI 返回非零退出码且不生成 Plan；MCP 返回错误。调用方不得把这类错误降级为 `resource-fit`、空集群或零占用。
- Kubernetes scheduler、设备拓扑、DRA、成组调度、动态配额变化和并发工作负载仍可能让一个旧的 `resource-fit` 计划无法落地。最终调度结果只能由后续受控部署和实际调度验证。
- alpha.20 不运行负载，不量测吞吐、并发、成功率、TTFT/TPOT 或质量指标；不要从资源可容纳性推导业务 SLO。
- alpha.20 不支持异构 Profile 组合、自动改写规格、自动创建 Deployment/Service、扩缩容或自动申请/预留资源。

发布范围和未验证项见 [alpha.20 发布说明](https://github.com/lsjfy-open-com/infernex-agent/blob/develop/component/InferNex-Agent/docs/releases/v0.5.0-alpha.20-zh.md)。完整 D1 目标及后续 D2 写路径见 [Kubernetes 分层契约](https://github.com/lsjfy-open-com/infernex-agent/blob/develop/component/InferNex-Agent/docs/architecture/kubernetes-first-zh.md)。
