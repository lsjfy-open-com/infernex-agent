# 私有化部署形态与能力演进验收补充

本文补充[下一代课题验收细则](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/development/next-generation-acceptance-spec-zh.md)，专门验证 Docker／Kubernetes 下聚合式与 Prefill/Decode（PD）分离式部署，以及固定能力向受约束能力构建的演进。原细则的批准、证据、SLO、恢复和发布阻断规则继续适用；本文只增加私有化部署矩阵，不代表相应能力已经实现。

文中的百分比均为开测前冻结的门槛，不是已取得的结果。客户推理框架、GPU／NPU 型号、镜像、模型、PD 传输方式和绝对 SLO 尚待环境方提供，本文不代填或推断。

## 1. 两阶段边界

| 阶段 | 可验收范围 | 明确缺口与出口 |
| --- | --- | --- |
| 第一阶段：alpha.22 基线盘点 | Linux amd64／arm64 离线安装；Host TUI；Kubernetes／Helm／Bridge 只读发现、日志和事件采集；同构单实例 Profile 的只读资源规划；已有 Bridge 受管候选纵向切片 | 不具备 Docker 通用部署事务、原生 Kubernetes 通用写路径、四种部署单元的自动交付或通用 PD 编排。输出逐项“支持／部分支持／不支持／未测”及证据，不能把源码存在或 CPU fixture 通过写成私有化部署验收通过 |
| 第二阶段：目标能力验收 | Docker 聚合式、Docker PD、Kubernetes 聚合式、Kubernetes PD 四个单元分别完成发现、计划、批准、部署、业务验证、升级和回退 | 四个单元必须独立达到样本、SLO、权限、离线和恢复门槛；未提供真实硬件或框架合同时记为“未就绪”，不能缩减为模拟通过 |

第一阶段以 [alpha.22 发布说明](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/releases/v0.5.0-alpha.22-zh.md)、[Kubernetes 通用底座](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/architecture/kubernetes-first-zh.md)及实际测试记录为依据。阶段二只能在对应单元的环境合同签收后开测。

## 2. 部署单元及声明合同

| 单元 | 最小目标 | 开测前必须声明 |
| --- | --- | --- |
| D-A：Docker 聚合式 | 一个受管聚合式推理实例及其服务入口；完成拉起、探针、请求、升级和恢复 | Docker／runtime 版本、容器和模型 digest、挂载、端口、资源限制、健康与停止合同 |
| D-P：Docker PD | 按获批模板拉起 Prefill、Decode 及框架要求的路由／KV 传输组件，验证端到端请求和角色恢复 | 框架及精确版本明确支持 PD；角色、实例数、端口、传输配置、就绪顺序、排空和恢复合同 |
| K-A：Kubernetes 聚合式 | 通过声明的 Native、Helm 或客户平台拥有者部署一个聚合式 serving 单元和稳定入口 | 集群／namespace、对象管理者、Chart／模板 digest、Profile、RBAC、探针、路由和恢复合同 |
| K-P：Kubernetes PD | 通过声明的资源拥有者部署 Prefill、Decode、路由／KV 传输对象并验证端到端路径 | 框架及精确版本明确支持 PD；CRD／Chart／API、角色映射、调度与网络约束、排空和恢复合同 |

“聚合式／PD”描述 serving 角色组织，“TP”描述单个角色内部的张量并行，两者不能互相替代。每个 Profile 必须声明各角色的 TP、rank、设备数和拓扑。Agent 不得把聚合式 TP 配置自动改写为 PD，不得猜测 Prefill／Decode rank、KV 传输或路由参数；框架未声明支持或合同字段不全时，结果必须是 `unsupported` 或 `blocked`，且变更数为零。

每个单元都要包含框架支持判定用例：精确支持版本、未支持版本、版本未知、合同缺字段各至少 1 例。只有精确支持版本进入写路径，其余用例须在部署前停止。

## 3. 环境和搭建责任

| 环境 | 用途 | 责任与限制 |
| --- | --- | --- |
| L-CPU 控制逻辑 | 确定性数据、Docker mock、Kind 对象、计划／批准／冲突／权限／恢复状态机 | 仓库维护者提供 fixture 和校验器；执行方记录工具版本和命令。这里只能证明控制逻辑，不能证明模型可用、加速卡兼容、PD 数据面或性能 |
| H-GPU 真实环境 | 四单元中客户声明支持 GPU 的单元 | 客户／环境方提供真实 GPU、驱动、runtime、框架、网络、模型、权重、镜像、配额、访问窗口与绝对 SLO；验收方冻结清单并采集证据 |
| H-NPU 真实环境 | 四单元中客户声明支持 NPU 的单元 | 客户／环境方提供真实 NPU、固件、驱动、CANN／通信库、框架、网络、模型、权重、镜像、配额、访问窗口与绝对 SLO；验收方冻结清单并采集证据 |
| O-离线镜像环境 | 对各已支持真实单元复跑制品导入、部署、升级和恢复 | 环境方阻断并审计互联网出口，预置内网模型入口及全部依赖；供应方交付摘要与来源清单；执行方不得临时联网补件 |

GPU 和 NPU 只验收客户声明且提供的受支持框架组合；不得自行创造“通用支持”结论。每种设备至少覆盖一个真实模型，若某框架只支持其中一种设备，另一种记为“不适用”并附框架依据，而不是伪造对称矩阵。

CPU 起步环境直接复用[验收实验室 starter kit](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/guides/next-generation-acceptance-lab-zh.md)及[验收包目录](https://github.com/lsjfy-open-com/infernex-agent/tree/codex/private-deployment-evolution/component/InferNex-Agent/test/acceptance/next-generation)。从仓库根目录运行：

```bash
cd component/InferNex-Agent/test/acceptance/next-generation
python3 selfcheck.py
fresh_dir="$(mktemp -d)"
python3 generate.py --output "${fresh_dir}/generated"
diff -ru generated "${fresh_dir}/generated"
export KIND_NODE_IMAGE='kindest/node:v1.35.0@sha256:452d707d4862f52530247495d180205e029056831160e22870e37e3f6c1ac31f'
kind create cluster --name infernex-nextgen-acceptance --config kind.yaml --image "${KIND_NODE_IMAGE}" --wait 120s
./scripts/lab.sh apply
./scripts/lab.sh check
```

离线导入和安全清理复用同一指南中的固定命令。真实环境另交付 setup／preflight／cleanup 脚本、环境 manifest 和 SHA-256；清理只能撤销本次测试拥有的对象，冲突时保留现场。

## 4. 用例记录合同

每次执行分配不可复用的 `caseId`，并保存以下字段：

| 项目 | 必填内容 |
| --- | --- |
| 输入 | 单元、环境指纹、被测提交、Agent／Docker／Kubernetes／框架版本、模型和制品 digest、Profile、期望对象、绝对 SLO、批准与权限范围、联网状态、基线 ID、注入项 |
| 步骤 | 原始用户目标；发现、计划和 diff；批准记录；确定性工具调用及参数；部署、预热、负载、升级、排空和回退步骤；起止 UTC 时间 |
| 输出 | 结构化状态、创建／修改／删除对象身份、端点、配置版本、最终 digest、业务验证值、人工介入、错误类别、是否达到恢复目标 |
| 日志 | stdout／stderr、Docker event 或 Kubernetes Event、控制器及各 serving 角色日志、request／trace ID、逐请求结果、指标原始样本、权限拒绝、网络审计、制品校验和回退后探针 |
| 判定 | `pass`／`fail`／`timeout`／`blocked`／`insufficient-evidence`／`not-ready`；失败阶段、原因、是否计入成功率分母及原始证据链接 |

截图和聚合 Dashboard 不能替代原始记录。Secret 只保存存在性和引用，值必须脱敏。缺日志、指标样本不足、时钟不可比或证据断链均判 `insufficient-evidence`，不能判通过。

## 5. 第二阶段用例与门槛

每个 D-A、D-P、K-A、K-P 单元都执行以下同构用例，不能用一个单元的结果代替另一个：

| ID | 输入与步骤 | 必须输出与通过条件 |
| --- | --- | --- |
| P1 正常首部署 | 已签收环境合同；从空候选范围执行发现→计划→批准→部署→预热→业务负载 | 对象身份、实际配置／digest、日志与指标齐全；达到全部绝对 SLO，无计划外人工修改 |
| P2 正常升级 | 从冻结健康基线升级一个明确版本；保持模型、资源预算和负载；验收后排空旧版本 | 新旧组合清单、路由／排空证据和业务结果齐全；无丢失的已接受请求，满足绝对 SLO |
| P3 失败补偿与回退 | 分别注入镜像／模型不可用、就绪超时、执行中断；恢复到预先冻结目标 | 只撤销本次拥有的变更；基线连续 100 个验收请求正确且窗口指标达标；记录恢复时间和残留检查 |
| P4 冲突与权限 | 计划后改变对象版本；并发修改；只读身份和缺单项权限分别执行 | 漂移或权限不足时拒绝写入并指出具体缺口；不得覆盖外部变更，不得把 `forbidden` 写成“不存在” |
| P5 离线与来源 | 同一制品联网准备、离线导入；再测损坏包、错架构、错基础版本、缺依赖和摘要不符 | 在线／离线目标 digest 一致；来源、许可证／授权和导入链可追溯；5 类负例全部在变更前阻断；外网成功连接 0 次 |
| P6 框架与形态边界 | 支持／不支持／未知版本及缺合同的聚合式和 PD 请求；含 TP 参数 | 仅声明支持且合同完整者进入计划；不自动做 TP→PD 转换；拒绝结果无环境变更 |

P7 流量专项：每单元另建立至少两个完整且可独立路由的服务拓扑副本，在同一客户端长连接与并发流下观测请求计数、容量权重与在途量，并摘除一个副本验证新请求转移和旧流排空。PD 的 P/D worker 不独立计作对外副本。至少 3 组、每组 1,000 请求；健康副本均实际命中，容量归一化流量偏差阈值预先冻结，摘流后不接新请求且已接受流无非预期丢失；仅提供一个拓扑时此项 not-ready，不宣称多副本均衡已通过。

### 样本与成功率

- 每单元正常首部署不少于 20 次，正常升级不少于 20 次；两类分别计算自主业务成功率，均须 ≥95%。每组恰为 20 次时最多允许 1 次失败。
- 所有按正常 P1/P2 启动的尝试，即使最终 `fail`、`timeout`、`blocked`、证据不足、计划外人工修改或执行后回退，也保留并计入对应成功率分母；只有开测前判定的环境 `not-ready` 不启动计时且不进入执行分母。报告总数、各状态数、失败阶段和最长耗时，不能删除失败重跑。
- P3–P6 预设负例单独统计场景判定准确率与恢复结果，不混入 P1/P2 成功率；预期写入前阻断可以判通过，所有意外结果仍保留。每单元 P3 至少 5 次恢复演练，覆盖执行中断、就绪超时与部分成功，恢复目标命中率必须 100%。P4 的漂移、并发冲突、只读及权限撤销至少各 1 次，越权删除或覆盖为 0 次。

### 性能与质量

每单元先冻结一个健康稳定基线 `H`，候选 `C` 使用同模型 revision、精度、硬件预算、输入／输出长度、并发、缓存条件和数据集，至少做 3 组交替对照，每侧每组不少于 1,000 个计量请求，预热不计。

- 候选逐项满足客户预注册的成功率、质量、p95 延迟、吞吐及资源上限等绝对 SLO；缺任何必选指标即失败或证据不足。
- 流式场景分别检查 `p95_TTFT(C) / p95_TTFT(H) < 1.2` 和 `p95_TPOT(C) / p95_TPOT(H) < 1.2`；仅非流式场景检查 `p95_E2E(C) / p95_E2E(H) < 1.2`。
- 有效 goodput 指满足质量和全部绝对 SLO 的正确请求／秒，须满足 `goodput(C) / goodput(H) > 0.8`；质量评分还须达到绝对门槛，且相对基线下降不超过预注册容差（默认 1 个百分点）。
- 任一分母为零、单位不一致或请求样本无法关联原始日志时，该组证据不足。达到相对门槛只说明未超过允许退化，不等于性能优化。

## 6. 能力演进对照

能力演进单独比较固定工具基线 `F` 与受约束构建候选 `L`。环境方提供 2 类公开开发框架和至少 1 类保留框架；每类提供 schema、脱敏实例、API mock、管理语义、权限与恢复合同。保留框架及用例在构建阶段由独立评估者隔离保管，构建账号不可访问；正式评估时仅提供完成任务必需的目标范围、接口 schema 与脱敏观察，评分答案、故障真值及后续用例继续封存。

1. 开测前冻结同一模型、系统提示、任务、工具权限、token／时间预算和硬件资源；`F` 只使用发布时固定工具／Skill，`L` 只使用开发框架材料构建并固化候选能力包。
2. 候选包记录来源、生成输入、版本、摘要、依赖、权限和适用矩阵，通过静态检查及参数无效、缺权限、重复执行、并发漂移、取消和恢复负例后封存；保留测试可按输入进行运行时发现与规划，但不能持久学习、改写工具/Skill 或跨用例积累答案。
3. 独立评估者才在隔离环境注入保留框架合同。每种模式执行同一批不少于 20 个保留用例，报告部署成功、耗时 p95、人工介入、模型调用成本、构建成本、失败和既有框架回归；所有失败保留在分母中。
4. `L` 只有在保留框架自主业务成功率 ≥95%、未经批准写入和权限放大均为 0、既有支持单元无回归，且质量／延迟／goodput 仍满足第 5 节时才通过。报告必须同时给出 `F`，不能只展示候选成绩。

评估者保存 participant／evaluator 包摘要、访问审计和测试后污染检查。保留信息一旦出现在代码、Skill、提示、缓存、日志或人工修复输入中，该轮作废并更换未泄漏变体；仅用不同目录隔离不算防泄漏。

## 7. 阶段出口

第一阶段交付 alpha.22 能力映射、运行记录和四单元缺口，不产生“已支持私有化自动部署”的结论。第二阶段交付四单元逐项报告、原始证据索引、失败总表、兼容矩阵、离线来源清单、能力演进对照和已演练恢复点。任一必选单元未就绪、成功率不足、绝对 SLO 未达标、权限越界、来源不可追溯或回退失败，整体状态均不得写为通过。
