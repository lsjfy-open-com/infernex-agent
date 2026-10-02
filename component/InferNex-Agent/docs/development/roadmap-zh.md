# 当前开发路线图

本页是唯一当前排期入口。旧 v0.5 提案与多阶段路线图已移入 `archive/`，作为背景保留。

基线：`0.5.0-alpha.22`；保留 alpha.20 的只读资源规划、alpha.21 的命令分类与 root risk，新增 alpha.22 的 Pi 工具循环自动压缩修复。当前并未交付类型化 Docker 部署或原生 K8s 写路径。持续演进在 `codex/private-deployment-evolution` 分支进行，目标扩展为私域 Docker/K8s 的聚合与 PD 部署；InferNex Bridge 是可选适配。该分支当前交付建模、契约与验收设计，不改变已发布包。设计入口见[私域跨环境部署与自演进](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/architecture/private-deployment-evolution-zh.md)。

下一大版本面向客户的范围见[课题简介](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/architecture/next-generation-topic-and-acceptance-zh.md)；环境、出题方数据责任、轻量领域模型、能力包、异构资源、联网／离线制品及 A1–A7、B1–B12 量化门槛见[验收细则](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/development/next-generation-acceptance-spec-zh.md)。“部署基础与受管发布验收”复核参考实现基线，“跨环境自动部署与生命周期管理验收”检查增量闭环；指标是待执行的验收目标，不是已取得效果。该课题复用本页 K0/A1、D1/D2/D3、E1/E2、T1/R1、O1，不另设平行排期。

已有 [Bridge 渐进实验](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/guides/progressive-experiments-zh.md)包含独立候选、Ready/日志回归/浸泡门禁及失败候选回退。后续复用这套基础，补业务 SLO、补丁制品和通用执行器，不重新建设一套平行实验系统。

| 阶段 | 范围 | 本轮状态 / 验收 |
| --- | --- | --- |
| K0 通用底座与治理 | 中立发现入口、Service 后端诊断、文档分层、开发基线收敛 | 已纳入当前基线；只读，不宣称流量已均衡 |
| E1 SLO 与对照实验 | 版本化目标、固定负载、请求样本、质量/延迟/吞吐对照，首个 Mooncake 场景 | alpha.16 已实现 Bridge 固定串行非流式请求门禁、端到端 p95/成功率/请求吞吐、三态判定和持久证据；TTFT/TPOT、并发和现场 Mooncake 验收待补 |
| E2 修复与补丁制品 | 隔离构建、固定基础镜像、补丁/组合版本清单、离线交付与实验恢复；以 Git 期望配置、变更前后 snapshot 和 ReleaseManifest 关联版本 | alpha.16 提供 config-version 的 Git commit / 快照 / 文件证据关联及本地完整性校验；补丁构建、语义对齐、全栈快照和执行恢复仍待实现 |
| E2a 离线知识与补丁 | 本地知识、已安装版本及日志；人工导入经批准的补丁或资料，核验来源、适用版本与校验值 | 待实现；断网可形成有证据的建议并交付可复验补丁，不自动在线对齐 |
| E2b 授权联网核对 | 经授权的只读工具查询上游发布、补丁及兼容矩阵，核验来源与版本后关联 E2 制品 | 待实现；默认仅建议，实际部署按批准流程；E2 离线交付不依赖 E2b |
| D1 规格与容量计划 | 批准 Profile、资源快照、用户需求、实例规格/数量/放置、计划 hash | alpha.20 已交付第一个只读切片：调用方提供单一同构 Profile，按 namespace、replicas 和当前可见快照估算资源与逐节点放置，输出 profile/snapshot/plan hash；它不批准 Profile、不预留资源、不写 Kubernetes、不验证性能。复杂调度未知时 blocked，读取权限失败时拒绝生成计划。异构组合、完整配额/拓扑/成组调度和硬件现场回归仍待实现，D1 尚未完成 |
| D2 原生部署事务 | Deployment + Service，计划批准、持久日志、就绪/预热、失败补偿和回退 | 待实现；无 Bridge Kind 全链路以及重启/并发编辑/部分创建失败 |
| T1 请求级分流 | 对接已有 L7 数据面；纯 K8s 可选网关；容量权重、摘流排空与指标 | 待实现；同一客户端长连接、多后端实际命中、后端故障转移和流式响应 |
| R1 受控发布与稳定晋级 | 复用成熟发布控制器，SLO 灰度门禁、恢复方案与版本晋级 | 待实现；依赖 E1/E2 及对应环境执行/流量能力，区分流量恢复与配置/数据恢复 |
| O1 通用故障闭环 | 原生工作负载 Incident、证据关联、处置计划、服务与流量恢复验收 | 待实现；保留 Kubernetes/Operator 管理权，不能绕写子资源 |
| O2 持续工程优化 | 组件/源码候选、性能与成本实验、跨版本经验检索 | 待实现；质量与稳定性不退化，收益经端到端复验，生产变更按计划批准 |
| A1 环境适配 | 将现有 Bridge 写路径迁入边界，接入批准 Helm Chart 和首个客户 API/CRD | 按客户真实接口逐个验收；发现存在不等于具备写权限 |
| D3 分布式与硬件 Profile | StatefulSet/LWS、多节点多卡、GPU/NPU/DRA/网络与调度协同 | 分期；CPU Kind 不代替硬件现场验收 |

主线以 D1 → D2 → T1 → R1 完成 Native、Helm 和客户平台的自动部署、业务 SLO 验收、升级与恢复；客户已有发布能力可通过适配器接入并单独验收。Mooncake prefix hit 超时仅在环境与封存真值具备时作为部署失败／上线保障专项贯穿 E1 → E2，终点是经验证的处置及可恢复制品。E1/E2 可先复用 Bridge 候选环境；O1/O2 复用同一目标、证据、变更、工作负载身份和业务验收。操作深度按配置/组件版本 → 源码/通信 → 算子/底层逐步扩展，环境适配与授权分别处理。

E2a/E2b 是 E2 的知识来源增量验收，不改变 E2 原有的构建、离线交付和恢复范围。完整组合版本管理仍属 E2 规划；alpha.16 的关联记录不等于恢复执行器，当前恢复范围仍是 Bridge 源对象、变更记录与受管候选。

每个阶段拆成可独立验证的 PR；不将规划、创建成功、模型可用和请求分布通过混写成“自动部署已支持”。下一次实验包必须明确本次包含的阶段和未验证项。

## 私域部署增量顺序（沿用本页阶段编号）

以下是现有阶段的新场景拆分，不建立第二套排期。全部处于设计状态；以客户首个已确认的引擎、硬件与模板为起点，真实资料未齐时记录阻塞。

| 增量 | 对应阶段 | 可独立评审的交付 | 依赖与完成条件 |
| --- | --- | --- | --- |
| 领域记录与发现 | K0/A1 | 中立身份、字段级证据、Docker/K8s Inventory 与管理归属 | 权限不足、未知组件与配置冲突被明确呈现；不产生写入 |
| Docker 聚合闭环 | D1/D2/E1/E2 | Profile、计划/渲染、变更账本、加载/请求验收、Git/快照与恢复 | 一个实际引擎/硬件组合可部署和恢复，外部卷边界明确 |
| Docker PD 闭环 | D3/T1 | P/D 角色组、版本化 KV/TP 契约、路由与真实链路测量 | 依赖资源/执行账本；不能由聚合部署成功推定支持 PD |
| 原生 K8s 对等执行 | A1/D2/D3 | 相同 Intent 的聚合与已确认 PD 适配、管理权与状态映射 | CPU 逻辑和真实硬件分别验收；Helm/Operator 资源不被绕写 |
| 物料与稳定发布 | E2a/E2b/R1 | 离线物料闭包、授权联网核对、组合版本与回退演练 | 先完成一种执行环境闭环；发布恢复和能力撤回分别验证 |
| 能力候选晋级 | O2/A1 | 版本化工具/Skill、隔离构建、保留用例、启用与撤回 | 依赖前述证据；第二框架或版本测量复用收益与退化 |

验收增补见[私域部署两阶段验收](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/development/private-deployment-acceptance-zh.md)。实际实施顺序可随客户 Docker/K8s 的交付优先级调整，但必须保留同一领域模型、授权、执行账本与验收契约。每个增量单独 PR/验证，稳定后合入 develop；不直接在长期分支覆盖发布版本。
