# InferNex Agent 产品设计

## 1. 设计目标

InferNex Agent 将 InferNex 已有能力组织成一个长期运行、证据驱动且权限
受控的运维产品。设计目标是：

1. 复用 `InferNexService` 和 Bridge 的权威状态，不建立第二套编排器；
2. 在没有大模型时仍能持续扫描、分类问题和展示证据；
3. 将大模型限制为诊断建议者，不向其提供集群凭据或写操作；
4. 同时支持 Kubernetes 原生部署和固定运维节点部署；
5. 所有写能力都需要显式启用，并受 RBAC、目录、所有权和确认机制约束；
6. 离线包可校验、可升级、可回退并自带完整产品文档。

## 2. 非目标

Agent 不负责：

- 推理请求转发、流量切换或负载均衡；
- 创建任意 Kubernetes YAML、运行 shell/SSH 或暴露通用 `kubectl`；
- 安装 CANN、NPU 驱动、固件、模型权重或推理框架；
- 替代 InferNex Bridge、Hermes、PD Orchestrator 或 Eagle-Eye；
- 基于模型文本自动选择恢复模板或执行变更；
- 跨集群统一控制、租户认证、审计存储和告警通知。

这些能力需要通过稳定的 InferNex API 或外部产品集成逐步加入，不能绕过
现有控制面。

## 3. 逻辑架构

```text
运维人员 / Agent Runtime
          |
          | MCP 或只读 Web
          v
+---------------- InferNex Agent ----------------+
| Typed MCP tools                                |
| Periodic supervisor                            |
|  - Kubernetes evidence collector               |
|  - deterministic issue classifier              |
|  - optional OpenAI-compatible analyzer         |
|  - optional guarded remediator                  |
| Immutable in-memory snapshot -> Dashboard/API  |
+------------------------------------------------+
          |
          | namespace-scoped Kubernetes API
          v
InferNexService + status + managed workloads + Events
          |
          v
InferNex Bridge / 现有 InferNex 控制器
```

Agent 位于管理面，不位于推理数据面。Agent 停止不会中断已经运行的推理请求，
Bridge 和 Kubernetes 仍继续维护现有工作负载。

## 4. 组件职责

| 组件 | 输入 | 输出 | 持久状态 |
| --- | --- | --- | --- |
| Observer | Kubernetes API | 有界、脱敏的服务/拓扑/事件证据 | 无 |
| Classifier | 归一化证据 | 确定性问题列表 | 无 |
| Analyzer | 问题证据、模型配置 | 建议文本或错误 | 进程内结果缓存 |
| Supervisor | 命名空间和扫描周期 | 不可变快照 | 进程内快照 |
| Dashboard | 快照 | HTML 和 JSON | 无 |
| MCP server | 显式工具参数 | 有界领域结果 | 无 |
| Catalog deployer | 固定 catalog ID | Agent 所有的 `InferNexService` | Kubernetes CR |
| Remediator | 批准模板和连续故障证据 | 新的恢复 `InferNexService` | Kubernetes CR |

重启 Agent 会清空分析缓存和当前内存快照，下一轮扫描会自动重建。权威状态
始终位于 Kubernetes/InferNex，而不在本地 Agent 数据库中。

## 5. 扫描与分析流程

```text
定时触发
  -> 遍历显式命名空间
  -> 读取 InferNexService 状态
  -> 关联受管 Deployment/DaemonSet/LeaderWorkerSet/Pod/Event
  -> 归一化、截断和脱敏
  -> 确定性分类
  -> 有问题且模型已配置：调用模型
  -> 相同证据：复用进程内分析缓存
  -> 原子替换 Dashboard 快照
  -> 可选：评估与模型无关的恢复门禁
```

模型超时、鉴权失败或返回格式错误只影响该次建议，错误会记录在分析结果中。
扫描循环、MCP 和 Dashboard 不因模型故障停止。

## 6. 部署设计

### 6.1 宿主机 systemd

推荐用于固定 openEuler master/引导节点：

- 单个 CGO-disabled 静态二进制；
- `infernex-agent` 非登录系统用户；
- 专用、命名空间级 kubeconfig；
- MCP/Dashboard 默认回环地址；
- systemd capability 清空和文件系统保护；
- 模型参数位于 `/etc/infernex-agent/agent.conf`；
- Kubernetes 凭据和模型密钥分别保存为 `0600` 文件。

安装器只在引导阶段使用传入的管理员 kubeconfig 检查权限。长期服务不读取
`admin.conf`。

### 6.2 集群内 Helm

适用于 Kubernetes 原生部署：

- 使用投射的短期 ServiceAccount Token；
- Pod 级自动 Token 挂载关闭；
- 默认 namespace Role 为只读；
- Secret 由运维人员预先创建，Chart 只引用，不生成密钥；
- NetworkPolicy 默认限制 MCP 和 Dashboard 来源。

## 7. 配置设计

宿主机有效参数以“一行一个 CLI 参数”的形式写入
`/etc/infernex-agent/agent.conf`。启动器使用 Bash 数组读取，不执行配置
内容，因此参数中的 shell 元字符不会被解释为命令。

配置分为三类：

| 类别 | 示例 | 变更方式 |
| --- | --- | --- |
| 集群和监听配置 | kubeconfig、命名空间、端口 | 重跑安装器 |
| 模型配置 | base URL、model、timeout | `configure-model.sh` |
| 敏感凭据 | kubeconfig、API key | 独立 `0600` 文件和专用轮换流程 |

重跑安装器升级二进制时，如未传入任何模型选项，会保留已经存在的模型配置
和 API Key。显式换模、换端点、轮换密钥或禁用模型由配置命令完成。

## 8. 一致性和故障语义

- 每轮 Dashboard 快照作为整体替换，读者不会看到半轮扫描结果；
- Kubernetes 读取失败会以受限错误呈现，不把原始凭据带入输出；
- Catalog 创建具有所有权和 spec 漂移检查，重复请求幂等；
- 恢复服务使用独立名称，不覆盖源服务；
- Agent 不负责流量切换，因此恢复资源创建成功不等于业务已经切流；
- 模型输出不进入 mutation 输入；
- 二进制升级保留 `.previous`，便于人工回退；
- 模型配置更新先进行可选连通性测试，服务启动失败时恢复旧配置。
- 宿主机安装覆盖文件前保存带校验和的安装前集群和本机恢复点，安装失败自动恢复；
- Catalog 新建先追加写 `planned`/`applied` 记录，未 Ready 或当前 generation
  Degraded 时仅删除同一 `change-id` 创建的资源；
- 未完成部署在 Agent 重启后继续监控，终态可通过 `infernex_get_change` 查询。
- 自动恢复服务也使用同一追加写变更记录和 `change-id`，Dashboard 显示该关联 ID。

## 9. 扩展原则

后续能力优先接入 InferNex 自有稳定 API，并遵守：

1. 先只读证据，再建议，再计划，最后才是经批准执行；
2. 不增加通用 YAML、shell 或任意 patch；
3. 变更接口必须带前置条件、影响、验证、超时和回滚；
4. 观测数据必须有数量、长度和敏感字段边界；
5. 跨网络入口必须由认证网关承载；
6. 产品版本必须同时提供离线产物、校验和、迁移说明与验收结果。
