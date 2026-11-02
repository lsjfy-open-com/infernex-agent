# 运行模式、Policy、配置版本与 Dashboard 路由设计

本文定义 v0.5 写能力的安全架构。模式不是给模型换一段 prompt，而是选择一份由 Core 强制执行的
Policy profile。模型和 TUI 不能提升当前模式，也不能绕过版本、批准、预算和回退门槛。

## 模式不是任务指令，而是权限上限

| 模式 | 用户意图 | 可用能力 | 默认批准策略 | 回退要求 |
| --- | --- | --- | --- | --- |
| `detect` | 资产扫描、现状、健康概览 | discovery、GET/LIST、受限 Event/metadata | 只读自动 | 无集群变更 |
| `diagnose` | 故障定位、日志/指标/连通性检查 | detect + bounded logs、previous logs、固定 Pod exec/宿主机/SSH 探针、checker、serving probe、持续证据采集 | 低开销主动读取自动；持续采集和可能增加负载的测试逐次批准 | 保存 Evidence，不改 desired state |
| `modify` | 调整既有实例、values、路由、参数 | diagnose + 受控 patch/Helm upgrade/restart | 计划、diff、影响范围逐次批准 | 强制 pre-change version；失败自动恢复 |
| `install` | 拉起新实例、组件或外部路由 | modify + create release/source resources/routes | 容量、目标、外部地址、Chart/source 双重确认 | 安装前集群基线 + 每阶段 checkpoint |
| `recover` | 恢复到已知稳定版本 | 仅 version manager 的 plan/restore/verify | 明确选择 version ID 后批准 | 恢复动作本身先生成当前版本，允许撤销恢复 |

默认是 `detect`。模式切换应带操作者、原因、TTL 和 scope，例如未来命令：

```bash
sudo infernex-agent mode set modify --namespace models --ttl 2h --reason INC-2026-0816
sudo infernex-agent mode show
sudo infernex-agent mode reset
```

TTL 到期自动退回 `detect`。`install` 不隐含“自动安装”；它只允许 Agent 在得到具体目标和批准后调用
安装类工具。

### 通道与动作等级必须分开

Policy 不使用“用了 SSH/exec，所以一定是写操作”这种粗粒度判断。一次调用同时声明：

- channel：`kubernetes-api`、`pod-log`、`pod-exec`、`host-file`、`host-process`、`ssh`；
- action class：`passive-read`、`active-read`、`modify`、`install`、`recover`；
- side effect：是否只写 Agent Evidence/Report/Session，是否改变集群 desired state；
- target/scope、数据敏感度、运行时间、输出和并发预算。

因此，在 `diagnose` 模式通过 Pod exec 执行固定 `npu-smi info` 可以被允许；通过同一个通道执行
`kill`、修改配置或重启服务仍会被 schema 和 Policy 拒绝。任意命令字符串不作为 MCP 参数。

### CANN plog 外部持续采集

plog 不能只在故障发生后临时读取，因为 Pod 重建会丢失容器内日志。当前纵向切片由管理节点 Agent 创建
一个经批准的 `PlogCapture` 诊断任务，监听目标 Pod UID/容器变化，通过只读 exec 或已有日志接口增量
抓取 plog，并写入自己的 Evidence Store：

```text
cluster fingerprint / namespace / workload / pod UID / container / segment time / sha256
```

采集任务不 patch workload、不注入 sidecar、不向容器写文件；Pod UID 改变时关闭旧 segment、保留索引，
并为新 Pod 开始新 segment。启动/停止任务、保留期、最大字节、最大并发需要用户批准和预算，因为它会
持续写 Agent 自有存储。若现场已有 hostPath、Loki 或日志平台，优先通过适配器登记原始证据，避免重复
采集。当前实现已有 start/list/get/stop、Pod UID 分段、进程重启恢复、时限和最大字节门槛；现场仍需
验证不同 CANN 镜像中的 plog 根路径以及 `find/stat/dd` 可用性。只有部署 DaemonSet/sidecar 的可选
方案才进入 `install/modify` 模式并要求配置版本与回退。

## Policy Engine 判定什么

每次工具调用进入 Core 后，Policy Engine 使用下面的输入作决定：

- 当前 mode、TTL、操作者和允许的 cluster/namespace/resource scope；
- MCP tool 的 action class：observe、diagnose、modify、install、recover；
- 目标对象、owner、当前 resourceVersion/Helm revision 和是否由 Agent 管理；
- 数据风险：Secret、日志、外部 endpoint、模型请求和报告导出；
- 运行预算：最大工具轮次、对象数、日志字节、测试并发、token、等待时间；
- 必需前置条件：配置版本、dry-run/diff、容量检查、批准、维护窗口；
- 必需后置条件：readiness、serving-path、warmup/eval、soak 和报告；
- 失败策略：停止、恢复指定 version、保留现场、禁止继续级联修改。

Policy 输出不是简单 allow/deny，而是：`allow`、`deny(reason)`、`require-approval(preview)`、
`require-snapshot(scope)` 或 `defer(until)`。批准记录绑定计划摘要、目标 UID/resourceVersion、配置
版本 ID 和参数 hash；目标发生变化后旧批准失效。

## Configuration Version Manager

现有 `changesafety.ClusterSnapshot` 只处理 Bridge 的 `InferNexService` source object，不能完整恢复
openFuyao 主 Chart、Helm values、LWS、Gateway/HTTPRoute 和相关组件。因此 v0.5 新增配置版本管理器，
并保留现有 change journal 作为单次变更事件层。

### 存储布局

```text
/var/lib/infernex-agent/config-versions/
└── clusters/<api-server-sha256>/
    └── stacks/<stack-id>/
        ├── index.json
        ├── aliases.json                 # stable/latest/last-known-good
        └── versions/<UTC>-<config-hash12>/
            ├── metadata.json            # 原因、操作者、来源、父版本、状态
            ├── checksums.sha256
            ├── helm/<namespace>/<release>/
            │   ├── history.json
            │   ├── values-user.yaml
            │   ├── values-effective.yaml
            │   ├── rendered-manifest.yaml
            │   └── chart-source.json    # name/version/digest/离线包位置
            ├── kubernetes/<group-kind>/<namespace>/<name>.yaml
            ├── routing/gateway-api.yaml
            └── validation/
                ├── readiness.json
                ├── serving-probe.json
                └── report.json
```

`stack-id` 使用归一化的 `helm:<namespace>:<release>`；非 Helm 工作负载使用
`k8s:<namespace>:<root-kind>:<root-name>`。版本 ID 使用 UTC 时间加 canonical configuration SHA-256
前 12 位，既可读又能去重。`index.json` 只保存索引和父子关系，不保存另一份集群真相。

### 保存什么、不保存什么

- 保存用户 values、effective values、Chart 名称/版本/digest、Helm revision、渲染 manifest、受管 source
  resource、Gateway 路由和验证结果；
- Secret 可为恢复目的进入 root-only/encrypted backup，但绝不通过 MCP、报告或模型上下文返回；第一版
  在没有本地加密密钥时只保存 Secret 引用和 key 名，并把“无法完整恢复 Secret”标为阻断风险；
- 不把 Pod、ReplicaSet、Endpoint 等 controller 派生状态当作 desired configuration 恢复；
- 不假定模板永远不变。跨 Chart 版本回退必须同时保留 Chart digest 与 values，不能只保存 values；
- 日志和评测原文进入 Evidence Store，版本目录只保存引用和验收结论。

### 修改事务

```text
Discover target
→ Capture current version Vn
→ Render candidate Vn+1 and semantic diff
→ Policy check + capacity/routing checks
→ Operator approves exact hash
→ Apply through Helm/typed API
→ Observe rollout → warmup → serving probe → optional eval/soak
→ success: tag Vn+1 stable
→ failure: preserve evidence, capture failed version, restore Vn, verify again
```

Helm release 优先使用 `helm rollback <release> <revision>`；只有 revision 不可用时才使用保存的 Chart
和 values 重建。非 Helm source resource 采用 server-side dry-run、UID/resourceVersion 冲突检查和受控
apply。任何恢复都不盲目覆盖随后由其他操作者修改的对象；冲突进入人工处理。

## Dashboard 通过 Istio/Gateway 对外发布

宿主机 Agent 不在 Pod 内，Gateway 不能凭空把流量送到 `127.0.0.1:8081`。自动发布需要显式建立：

```text
Browser → existing Istio/Gateway listener → HTTPRoute/VirtualService
        → selector-less Service → EndpointSlice(management-node IP:dashboard-port)
        → host InferNex Agent dashboard
```

发布工具必须自动发现 GatewayClass、Gateway、listener、可用 hostname 和数据面到管理节点的网络可达性，
然后生成 diff。只有同时满足以下条件才能创建：

1. 用户进入 `install` 或 `modify` mode 并批准 hostname、Gateway 和管理节点 IP；
2. Dashboard 具备 token/OIDC/mesh authentication；只读不等于可以匿名暴露；
3. Agent 绑定管理节点内网 IP，而非 `0.0.0.0`；主机防火墙只允许 Gateway 数据面来源；
4. TLS listener、证书和 DNS 已存在或由明确批准的外部流程提供；
5. Service、EndpointSlice、HTTPRoute 都由 version manager 保存，Route `Accepted` 且 backend healthy；
6. 卸载或 unpublish 只删除带匹配 Agent ownership/change ID 的路由资源。

若 CNI/mesh 不允许数据面访问节点地址，替代方案是一个最小、无集群权限的 reverse-proxy relay Pod；
它只转发 Dashboard，不运行 Agent、不持有 kubeconfig，也不改变“Agent 在管理节点运行”的架构。

当前 Dashboard 没有内建认证，因此 v0.5 第一阶段只完成路由发现、plan 和文档，不能为了省一次 SSH
就默认匿名暴露。认证完成后，再把 `infernex_publish_dashboard_route` 作为 install-mode typed tool 发布。

## 与 Snapshot、Change Journal 和记忆的关系

| 数据 | 作用 | 能否作为回退源 |
| --- | --- | --- |
| Supervisor snapshot | 当前观察视图，供 Dashboard | 否 |
| Evidence artifact | 日志、Event、报告原文 | 否，但用于判断和审计 |
| Semantic memory | 已验证经验、决定、稳定基线含义 | 否，只提供检索线索 |
| Change journal | 单次操作的计划、应用、提交/回退状态 | 部分；记录事务 |
| Configuration version | 可校验的 desired configuration 和恢复输入 | 是 |

模型可以建议恢复哪个版本，但只能由 Policy Engine + Configuration Version Manager 执行恢复。
