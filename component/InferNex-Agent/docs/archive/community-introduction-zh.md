> 历史资料：不代表当前功能或排期。以[文档入口](../README.md)和当前路线图为准。

# InferNex Agent 社区介绍提纲

本文用于社区会议、内部技术评审和合作伙伴介绍。详细依据见
[领域 Insight、设计原则与治理边界](domain-insights-and-governance-zh.md)。

## 1. 30 秒介绍

InferNex Agent 是面向 InferNex 的智能部署与长期运维入口。它运行在现有管理节点，复用 openFuyao、
Kubernetes、Helm 和 InferNex 组件，不建立第二套控制面。用户通过自然语言提出部署或优化目标，Agent
自动发现环境，从稳定基线生成计划，经批准执行，并以 readiness、serving probe、warmup、评测和
稳定性证据决定晋级或回退。当前项目优先补齐故障证据和诊断能力，是为了让后续自动部署具备可靠的
观察、验证和失败恢复基础。

## 2. 建议的 10 分钟叙事

完整的阶段、角色、输出与价值指标见[推理服务全生命周期](../architecture/deployment-lifecycle-zh.md)。介绍时可以把
产品概括为：前期是环境侦察员和部署架构助手，中期是模型接入工程师、实验编排者与发布门禁，生产期
是 SRE 助手；任一阶段发生异常时切换为证据协调者和 Incident Copilot。

### 第 1 页：问题不是“把 Pod 拉起来”

- 新模型部署跨越 openFuyao、InferNex、Gateway、PD、vLLM-Ascend、Mooncake、CANN/HCCL 和 NPU；
- Pod Running 之后仍可能出现超时、乱码、tool-call 异常、通信问题和性能回归；
- 多节点证据分散，Pod 重建后 previous logs/plog 可能丢失；
- 每增加一个加速特性都要重新进行配置、warmup、评测和长稳对比，人工成本随组合增长。

核心判断：这不是增加几条部署脚本可以解决的问题，而是缺少一个贯穿部署生命周期的 Agent 闭环。

### 第 2 页：产品定位

```text
自然语言目标
  → 自动发现
  → 稳定基线与单变量计划
  → Policy/批准
  → InferNex/Helm/Typed API
  → readiness/warmup/eval/soak
  → 晋级稳定版本或精确回退
```

强调三个“不”：

- 不替代 InferNex、Bridge、Hermes、PD-Orchestrator、Eagle-Eye；
- 不默认在集群常驻一个高权限 Agent Pod；
- 不向模型开放任意 shell、任意 YAML 或裸 kubeconfig。

### 第 3 页：为什么不是“通用 Agent + Skill”

通用 Agent Runtime 很有价值，应复用其 TUI、Session、上下文和工具循环；Skill 也适合承载 CANN、
HiXL 等诊断知识。但二者通常不直接提供：

- InferNex/openFuyao 组件和部署入口的 capability discovery；
- readiness、serving path 和稳定基线的成功语义；
- 受 RBAC、脱敏和预算控制的实时证据；
- 与具体对象版本绑定的批准、变更日志和回退；
- 面向 PD/vLLM-Ascend/Mooncake/CANN 的跨组件时间线。

我们的差异化不是“模型知道更多命令”，而是把社区 Insight 固化为 typed tools、Policy、Evidence、
验收 gate 和版本契约。通用 Agent 可以成为前端，也可以直接复用这些 MCP 能力。

### 第 4 页：领域内核

```text
Skill/Runbook          解释如何诊断
Typed MCP Tool         提供确定性证据和动作
Policy                 约束 mode、scope、approval、budget
Evidence Store         保存大日志、来源、时间和 hash
Configuration Version 保存可恢复配置
Deterministic Gate     决定继续、停止、晋级或回退
```

模型 Runtime 可替换，领域内核随 InferNex 版本持续演进。

### 第 5 页：读宽写严与主动读取

- detect：广泛读取当前 RBAC 可见的非敏感状态；
- diagnose：允许固定 Pod exec、宿主机和预授权 SSH 探针；
- modify/install/recover：必须使用独立 typed tool、精确 diff 和批准；
- 通道与动作分开：使用 exec 不等于获得写权限；
- Secret payload、任意命令、任意 IP/密钥、任意 patch 始终排除。

### 第 6 页：证据和故障诊断为什么先做

- 大日志先落盘，模型通过 hash、grep 和分页渐进读取；
- 自动过滤 metrics/health 噪声，但保留过滤计数；
- PlogCapture 按 Pod UID 分段，重建后旧证据仍可追溯；
- CollectorRun 用固定 profile 跨 Pod/节点采集 NPU/CANN/HCCN/PFC 证据；
- 专项 Subagent 只有诊断材料和报告权限，部署决策仍回到主 Agent。

诊断不是偏离部署目标，而是部署自动化的观察和失败恢复基础。

### 第 7 页：稳定基线与回退

- 一次只增加一个特性；
- 每阶段保存计划、基线、候选和 `changeId`；
- 失败只处理当前候选，不破坏稳定基线；
- 模型建议不直接触发回退，使用 Ready、Degraded、probe、eval 和超时等确定性事实；
- 自动回退只覆盖 Agent 纳管、所有权可证明、具备逆操作的配置，不宣称整个集群灾备。

### 第 8 页：当前成果与下一阶段

当前可演示：

- 管理节点在线/离线安装，amd64/arm64、openEuler；
- OpenAI-compatible 模型和 Pi TUI；
- openFuyao/K8s/Helm/Bridge 自动发现；
- Event、日志、固定主动探针、plog、CollectorRun；
- 本地历史日志、CANN/HiXL Skill、Markdown 报告；
- 受限诊断 Subagent MCP；
- Bridge 路径的受控部署、实验、change journal 和回退纵向切片；
- Session、上下文压缩、token 统计和跨 Session 语义记忆。

下一阶段重点：

- 主 Chart/Helm configuration version、diff、upgrade 和 rollback；
- serving-path、warmup、EvalScope 和 soak；
- infernex-checker、Prometheus/Eagle-Eye 适配；
- 与 vLLM-Ascend/NPU 专项诊断 Subagent 联调；
- 在真实 A2/PD 分离环境完成验收并逐步合入 InferNex 主仓。

### 生命周期角色一句话

```text
接入/建栈：环境侦察员 + 变更守门员
0-day 模型：模型接入工程师
服务拉起：验收工程师
特性演进：实验编排者 + 发布门禁
生产运行：SRE 助手 + 知识管家
异常发生：证据协调者 + Incident Copilot
```

## 3. 建议演示顺序

1. `infernex-agent check`：证明自动发现当前 API Server 和部署形态；
2. 自然语言询问资产和推理实例：展示 typed discovery，而不是预填模板；
3. 请求分析一个日志目录：展示 glob/grep、噪声过滤、Artifact 和 Markdown 报告；
4. 选择一个固定 NPU/CANN probe：展示 active-read 和 Policy 边界；
5. 展示 PlogCapture/CollectorRun 的 Evidence ID、Pod UID 和容量预算；
6. 展示诊断 Subagent contract：证明它没有部署和修改工具；
7. 展示一个实验/change journal：说明稳定基线、候选和回退范围；
8. 最后展示路线图，不把设计中能力伪装成现场结果。

演示应使用可公开或脱敏的环境；任何 token、kubeconfig、模型输入和业务日志不得进入截图或录屏。

## 4. 常见问题与建议回答

### Q1：用 OpenCode、Claude Code 或其他 Agent 配几个 Skill 不就可以了吗？

可以复用它们作为 Runtime，但 Skill 主要描述流程和知识，不能单独提供 InferNex 资源模型、实时证据、
RBAC、脱敏、结果预算、配置版本、批准和回退事务。本项目的核心交付物正是这些可由任意 Runtime
复用的领域契约。

### Q2：为什么现在看起来故障诊断能力很多？

因为 vLLM-Ascend + InferNex 部署新模型和逐步开启加速特性时，最急迫的成本集中在跨节点取证、定位、
重试和回归判断。没有观察和证据基础，自动部署只能变成更快地反复失败。接入专项诊断 Subagent 后，
主项目重点继续回到配置生成、部署、验证、实验和长期运维。

### Q3：为什么运行在管理节点，不做 Agent Pod？

运维人员本来就在管理/引导节点使用 kubeconfig、Helm、离线包和本地模型接口。管理节点形态最少侵入，
Agent 停止也不影响推理数据面。强制 Kubernetes 托管生命周期的环境仍可以使用高级 Helm 形态。

### Q4：读取宿主机、进 Pod、SSH 是否不安全？

风险来自“允许什么动作”，而不仅是通道名称。Agent 允许经过 Policy 的固定主动读取探针；模型不能
提供命令、IP、密钥或路径。修改、重启和安装属于不同 action class，必须由独立工具和批准处理。

### Q5：如何保证不会和 InferNex 控制器冲突？

Agent 不直接接管 controller 派生对象，优先通过 InferNex、Helm 或稳定 source API 表达 desired state；
写入前检查 owner、UID/resourceVersion 和配置版本，冲突时停止并交由人工处理。

### Q6：能否保证任何问题都自动回退？

不能，也不应这样承诺。只对 Agent 纳管、逆操作完整、所有权可证明且没有并发漂移的配置提供自动回退。
etcd、PVC/业务数据、外部系统副作用和硬件状态不在该承诺内。

### Q7：是否绑定某个模型？

不绑定。面向 OpenAI-compatible 模型，Runtime 也可替换。模型能力会影响规划和分析质量，但权限、
Evidence、成功 gate 和回退不依赖模型自觉遵守提示词。

### Q8：内网能否使用？

可以。正式包提供 amd64/arm64 离线归档，包含 Agent、TUI Runtime、必要的 `rg`/`fd`、Skill、许可证
和校验和；目标服务器安装时不需要 Go、Node、npm 或在线编译环境。模型接口可以是内网服务。

## 5. 希望社区讨论和决策的事项

介绍的结尾不要只请求“接受一个新组件”，而应提出清晰决策：

1. 是否认可 InferNex Deployment Agent 作为 InferNex 原生管理入口的方向；
2. 管理节点优先、集群内可选的交付形态是否符合社区边界；
3. 哪些 InferNex/Helm/checker/Eagle-Eye 接口可以作为稳定 typed tool 来源；
4. Policy、Evidence、Configuration Version 和 Subagent contract 应归属哪个社区模块；
5. Alpha 阶段优先选择哪些模型、拓扑和真实故障场景联合验收；
6. 如何以小 PR 分阶段合入，而不是一次性引入完整候选实现。

## 6. 对外表述规则

- 说“复用/编排/关联 InferNex 组件”，不说“替代现有组件”；
- 说“部署 Agent，诊断是关键支撑”，不把项目介绍成纯故障诊断工具；
- 说“受控主动读取”，不笼统说“只读”或“拥有 root 任意权限”；
- 说“可逆范围内精确回退”，不说“任何状态都能完全回退”；
- 明确区分已实现、部分实现和设计中；
- 用现场证据和确定性 gate 证明结果，不用模型回答截图代替验收；
- 强调领域内核可被通用 Runtime 和第三方 Subagent 复用，保持开放协作。
