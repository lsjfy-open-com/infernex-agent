# InferNex Agent MCP 工具目录与组件映射

本文区分“当前已经发布给模型的工具”和“v0.5 计划工具”。表格是工具契约清单，不是愿望清单；
没有标为已实现的能力，Agent 不得通过提示词声称已经执行。

## 为什么需要 InferNex 专用 MCP，而不只给通用 Agent 一个 Skill

Skill 可以告诉模型应该查什么、按什么顺序查，却不能自动提供稳定 API、RBAC、脱敏、结果上限、
变更事务、readiness 判定和回退语义。OpenCode 等通用 Agent也支持工具权限和外部 MCP，因此它们
可以成为 InferNex Agent 的另一种交互 runtime；真正不可替代的部分是下面这组由 InferNex 组件
模型、现场故障和安全闭环共同定义的 MCP 契约。

InferNex Agent 的优势不是“知道一条 kubectl 命令”，而是直接知道：

- openFuyao 引导、管理和业务集群的边界，以及当前 kubeconfig 只能代表一个 API Server；
- 主 Chart、Bridge、KServe、LeaderWorkerSet、Gateway、PD-Orchestrator、vLLM/vLLM-Ascend、
  Mooncake、CANN、HCCL/RoCE 在一次推理部署中的因果关系；
- 哪些 status、Event、Pod owner、日志时间线和 serving-path 证据才能证明服务真正可用；
- 哪些配置是稳定基线，怎样一次只增加一个特性，何时判定回归并回退；
- 每种写操作需要保存什么、批准什么、验证什么，而不是让模型临时拼一段 YAML。

通用 Agent + Skill 可以调用本 MCP；这不是竞争关系。若换成 OpenCode，仍应复用本工具层、Policy、
Configuration Version Manager 和 Evidence Store，而不能退化为任意 shell + runbook。

## 当前已实现工具

### openFuyao、Kubernetes 与 Helm 通用读取

| MCP tool | 面向组件 | 返回的确定性事实 | 边界 |
| --- | --- | --- | --- |
| `openfuyao_detect_environment` | BKE、管理面、业务面、K8s | 当前集群角色及 BKE/LWS/Bridge/KServe/Gateway/监控能力 | 只读；不因缺少 Bridge CRD 失败 |
| `k8s_cluster_overview` | Node、Pod、Namespace、加速卡资源 | API Server、版本、Node 地址、容量、Pod 健康 | 只读；Secret 不进入结果 |
| `k8s_list_workloads` | Deployment、StatefulSet、DaemonSet、LWS、Pod、Service | owner、镜像、地址、selector、readiness、Helm 关联 | 分页限长；不 exec |
| `k8s_discover_api_resources` | Kubernetes discovery、任意 CRD | groupVersion、plural、kind、scope、verbs | 先发现再读取，禁止猜资源名 |
| `k8s_read_resources` | 任意已发现资源 | GET/LIST 后的 spec/status 和元数据 | 仅 GET/LIST；去 managedFields、凭据脱敏、Secret 只留元数据 |
| `k8s_get_events` | Kubernetes Events | 全局、命名空间或对象级因果事件 | 有时间和数量预算 |
| `k8s_get_pod_logs` | 任意明确 Pod/容器 | current/previous 有界日志 | 凭据脱敏；无宿主机文件和 exec |
| `helm_list_releases` | InferNex 主 Chart 及依赖 Chart | Release、namespace、revision、status | 当前只读 inventory，不读取 Helm Secret payload |

这一组解决 openFuyao 主 Chart 方式没有 `InferNexService` 时的通用探索；它也是未知新 CRD 和 Node
真实 IP 等问题的兜底读取面。

### InferNex Bridge、服务拓扑和专项诊断

| MCP tool | 面向组件 | 作用 | 发布条件 |
| --- | --- | --- | --- |
| `infernex_list_services` | InferNex Bridge / `InferNexService` | 单 namespace 服务摘要 | 检测到 Bridge |
| `infernex_list_all_services` | Bridge | 自动发现 namespace 后汇总服务 | 检测到 Bridge |
| `infernex_inspect_service` | Bridge、baseRefs、component status | 查看模型、来源、条件和控制面状态 | 检测到 Bridge |
| `infernex_get_topology` | Bridge、Deployment、DaemonSet、LWS、Pod | 从服务追到实际运行拓扑 | 检测到 Bridge |
| `infernex_get_events` | Bridge owner graph | 只取服务及其受管工作负载事件 | 检测到 Bridge |
| `infernex_diagnose_service` | vLLM/vLLM-Ascend、PD 组件、相关 Pod | 关联 current/previous 日志、Event 和跨节点时间线 | 显式开启日志诊断 |

专项诊断目前以 owner graph、日志分类和时间线为主；还没有把 vLLM metrics、Mooncake 管理接口、
NPU checker 和 EvalScope 伪装成已实现工具。

### 受控部署、实验与变更状态

| MCP tool | 类型 | 当前约束 |
| --- | --- | --- |
| `infernex_list_deployment_sources` | 只读 | 只返回 Ready 稳定服务或管理员已有 profile 的 opaque source ID |
| `infernex_deploy_model` | 写 | 固定 workspace；不接受任意 image、command、URL、namespace 或 YAML；需要批准 |
| `infernex_delete_model` | 写/破坏 | 只能删除带 Agent ownership 和 change ID 的对象；需要批准 |
| `infernex_get_change` | 只读 | 查询 change journal、commit、apply failure 和 rollback 状态 |
| `infernex_start_experiment` | 写 | 从稳定基线克隆；每阶段只增加一个批准特性；回归即停止和回退 |
| `infernex_get_experiment` | 只读 | 查询阶段、对照、证据和回退结果 |
| `infernex_list_experiments` | 只读 | 查询持久化实验列表 |

上述写工具仍偏 Bridge 路径；openFuyao 主 Chart 的 values、Helm upgrade/rollback 和 Gateway 路由
修改属于 v0.5 后续 typed tools，不能通过通用 `k8s_read_resources` 绕过。

### Evidence、Session 和跨 Session 记忆

| tool/能力 | 所在层 | 状态与边界 |
| --- | --- | --- |
| `infernex_read_artifact` | Pi extension | 大工具结果按 SHA-256 落盘并分页读取，不是集群写操作 |
| `infernex_list_evidence_roots` | Go MCP Core | 列出运维人员显式授权的宿主机历史证据根目录 |
| `infernex_find_evidence_files` | Go MCP Core | 在授权根目录内有界 glob，拒绝路径和符号链接逃逸 |
| `infernex_grep_evidence_files` | Go MCP Core | RE2 检索历史日志，默认过滤 metrics/health 探针噪声并报告过滤计数 |
| `infernex_read_evidence_file` | Go MCP Core | 分页读取常规文件、脱敏并返回 SHA-256，不修改源日志 |
| `infernex_create_markdown_report` | Go MCP Core | 经批准在保护目录创建带证据 hash 的持久 Markdown 报告 |
| `infernex_list_reports` / `infernex_read_report` | Go MCP Core | 跨 Session 枚举和读取既有报告 |
| `infernex_list_skills` | Go MCP Core | 列出内置和运维人员安装的离线诊断 Skill、来源、摘要 hash 和参考文件 |
| `infernex_read_skill` | Go MCP Core | 按精确名称渐进读取一个 Skill 的诊断流程，不授予任何额外权限 |
| `infernex_read_skill_reference` | Go MCP Core | 读取已选 Skill 的一个受限 Markdown 参考文件；拒绝路径穿越、符号链接和超大内容 |
| `infernex_search_memory` | Go MCP Core | 检索当前集群和 global 的结构化长期记忆；结果使用前需重新验证 |
| `infernex_remember` | Go MCP Core | 写入 fact/decision/preference/procedure/incident/configuration-baseline；必须批准且来源受限 |
| `infernex_forget_memory` | Go MCP Core | 软删除并保留审计 tombstone；必须批准 |

语义记忆位于 `/var/lib/infernex-agent/semantic-memory`。`cluster` scope 使用 API Server 的 SHA-256
指纹隔离，避免把 A 集群的稳定配置带到 B 集群；`global` 只用于明确的运维偏好或通用决定。第一阶段
使用离线确定性相关度检索，不要求另装 embedding 模型。向量索引以后可以作为可重建加速层，不能
成为安全事实的唯一存储。

## v0.5 计划工具包

| toolset | 计划 typed tools | 复用对象 | 安全要求 |
| --- | --- | --- | --- |
| `infernex/config-version` | capture/list/diff/tag/restore configuration version | Helm history、Chart、values、K8s source resources | 修改前强制 capture；内容校验；恢复前二次 diff/批准 |
| `helm/change` | get values/history、render、diff、upgrade、rollback | 当前 Helm release 与离线 Chart | 不接受模型自由拼 Chart；revision 和 values 有版本索引 |
| `infernex/routing` | discover gateway、plan/publish/unpublish dashboard route | Gateway API、Istio Gateway/VirtualService、Service/EndpointSlice | 必须 TLS/认证；写工具受 install/modify Policy 控制 |
| `infernex/checker` | NPU/CANN/驱动、HCCS/RoCE、DNS、存储连通性 | infernex-checker、Eagle-Eye | 复用官方 checker；可能影响设备的测试需诊断批准 |
| `infernex/serving` | warmup、readiness、serving-path、OpenAI endpoint probe | Gateway、HTTPRoute、推理 endpoint | 请求预算、模型数据边界、结果留 Evidence |
| `infernex/evaluation` | EvalScope single/multi-turn、基线比较、报告 | EvalScope | 数据集许可、并发和 token 预算 |
| `infernex/runtime` | vLLM/vLLM-Ascend、Mooncake、PD timeline/metrics | metrics、日志、管理接口 | 版本 capability discovery，不根据模型名猜 |

## 工具选择原则

1. 常见领域事实优先 typed tool，未知资源才用 discovery + generic read。
2. Skill/runbook 负责解释“何时用”，MCP contract 负责保证“能做什么、不能做什么”。
3. Read-only 标注只是第一层；仍受 kubeconfig RBAC、脱敏、分页、Evidence 和预算控制。
4. 所有写能力必须成为独立 typed tool，绑定 Policy、目标、前置版本、批准、验证和回退。
5. 不增加任意 shell、任意 YAML 或通过参数切 verb 的万能 Kubernetes 写工具。
