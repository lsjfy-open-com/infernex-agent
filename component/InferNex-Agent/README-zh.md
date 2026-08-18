# InferNex Agent

[English](README.md) | 简体中文

InferNex Agent 是运行在 InferNex 管理节点上的本地 AI 运维 Agent。它和
`kubectl-ai`、K8sGPT 的 CLI 形态一样，使用当前 `kubectl` 上下文探索集群；它不会
为了运行自身而在 Kubernetes 中安装 Agent Pod、Controller 或新 CRD。

用户用自然语言描述目标，Agent 负责：

```text
理解意图 → 自动发现 → 选择工具 → 执行只读探索 → 形成计划
         → 本机确认写操作 → 验证结果 → 诊断/回退 → 输出证据
```

它首先复用 openFuyao 的 BKE/Cluster API、Kubernetes、Helm 和应用管理方式，再按实际
部署入口复用 InferNex 主 Chart 或可选的 InferNex Bridge/KServe，以及
vLLM/vLLM-Ascend、Mooncake、Hermes、PD Orchestrator、Eagle-Eye 和
infernex-checker；不建立第二套推理编排器。

## 安装：只选 CPU 架构

正常用户只使用一个发行包：

```text
infernex-agent-<版本>-linux-amd64.tar.gz  # x86_64
infernex-agent-<版本>-linux-arm64.tar.gz  # aarch64
```

这里没有“宿主机包”和“集群包”的选择。管理节点、master 节点、引导节点或普通
Linux 运维机，只要当前 `kubectl` 能访问 InferNex 集群，使用的都是同一个包。

联网安装：

```bash
curl -fsSL https://raw.githubusercontent.com/lsjfy-open-com/infernex-agent/main/component/InferNex-Agent/scripts/install.sh | sudo bash
```

离线安装：

```bash
sha256sum --check infernex-agent-*-linux-*.tar.gz.sha256
tar -xzf infernex-agent-*-linux-*.tar.gz
cd infernex-agent-*-linux-*
sudo ./install.sh
```

安装器自动完成架构识别、kubeconfig/当前 context 检测、Bridge CRD 或 Helm/BKE
形态识别、静态二进制安装和 systemd 常驻服务配置。默认复用当前
kubectl 身份，不创建 ServiceAccount/RBAC；有合规隔离要求时才使用高级选项
`--hardened-identity`。

一键安装默认使用 `diagnose` 模式：允许 Core 调用固定的 Pod exec/管理节点诊断探针，但不开放任意
shell，也不获得修改业务服务的权限。需要纯被动读取时使用 `sudo ./install.sh --execution-mode detect`。
需要跨节点时，可由运维人员提供 OpenSSH config 和 alias allow-list；模型不能提供 IP、用户或密钥。

没有 Bridge CRD 不再导致安装失败：Agent 会进入不修改集群的 Kubernetes/Helm 模式。
该模式已经能够识别当前 kubeconfig 指向的 openFuyao 引导/管理/业务集群角色，列出
Helm Release、Deployment/StatefulSet/DaemonSet/LWS、Pod、Service，查询 Event 和
经过限长、脱敏的 current/previous Pod 日志。对于其他原生资源和 CRD，Agent 可使用 API
discovery 与分页 GET/LIST 自动探索当前 kubeconfig/RBAC 可见的对象；Node、Pod 和 Service
的网络地址也会返回。Secret payload 始终排除。Bridge 专属观察和写工具都不会在此模式
发布给模型，避免 Agent 在不存在 InferNexService 的集群里反复调用无效工具。

唯一需要人工提供的是 Agent 模型接口：OpenAI 兼容 Base URL、真实 model ID 和
可选 API Key，以及该模型实际支持的上下文窗口（不确定时使用默认 32768）。Agent 会
限制单次工具结果和模型输出，在到达窗口前自动压缩较早轮次。安装完成后：

```bash
sudo infernex-agent chat
```

包含 Pi 的 v0.5 测试包中，上述命令默认进入完整 TUI。旧版 readline 终端保留为兼容入口：

```bash
sudo infernex-agent chat --classic
```

旧终端支持本次进程内历史。使用 `/undo` 撤回最后一个错误轮次，
`/context` 查看当前预算，`/usage` 查看模型调用与 token，`/compact` 主动压缩，`/clear` 清空当前会话。
如果模型以 `finish_reason=length` 截断回答，Agent 会自动续写并在无法完整恢复时明确提示。
新安装默认单次输出上限为 8192 token（小上下文窗口自动降低）；模型配置交互会显示该值，后续可用
`configure-model.sh --max-output-tokens N` 调整，classic chat 和 Pi TUI 共用同一配置。

跨 Session 语义记忆保存在 `/var/lib/infernex-agent/semantic-memory`。它只接受用户确认、工具验证或
运维人员录入的结构化事实、决定、偏好、incident 和稳定配置；按当前 API Server 指纹隔离，并通过
MCP 的 search/remember/forget 工具供任意 Agent runtime 使用。记忆是历史上下文，修改前仍须重新
读取实时集群状态。

用户自行保存、重启后无法重新采集的日志可登记为本地历史证据。Agent 提供受控的 glob、grep、分页读取和 Markdown 报告工具，默认过滤正常的 `/metrics`、`/health*`、`/readyz`、`/livez` 噪声并报告过滤计数；不会开放整个宿主机文件系统。详见[本地历史日志分析与 Markdown 报告](docs/local-evidence-and-reports-zh.md)。

PFC、HCCN、NPU、CANN 和 HCCL 前置检查可由持久 `CollectorRun` 自动按 label selector 展开 Pod/container，周期执行固定 Profile，并把 JSONL 样本直接保存到 Agent Evidence Store，不需要运维人员逐节点执行和搬运文件。详见[节点与容器持续诊断 CollectorRun](docs/collector-runs-zh.md)。

`diagnose` 模式还可经用户批准启动外部 `PlogCapture`：按 workload label selector 监听 Pod，通过只读
exec 从容器挂载与 CANN/NPU 兼容根发现日志，保存 Pod 元信息、current/previous logs 和 plog，并按
Pod UID 写入 Agent Evidence Store。Pod 重建后旧 segment 不丢失；
采集任务有持续时间和最大字节上限，停止任务不会删除证据，也不会 patch 或注入业务 Pod。

`agent/pi-agent-foundation` 分支正在并行验证基于 Pi 的完整 TUI，复用其 Session 恢复、上下文压缩、
token/context 状态和流式工具展示，同时继续由现有 Go 服务执行受控 MCP 工具、审批和回退。详见
[Pi TUI 使用与边界](docs/pi-tui-zh.md)。

当前 Pi TUI 默认入口测试包为 `v0.5.0-alpha.12`，同时提供 amd64 与 arm64，不替换当前 v0.4 RC 稳定测试线。alpha.12 包含默认 8192 输出上限、跨 Session 语义记忆、默认折叠 reasoning block，以及受控的历史日志分析与 Markdown 报告。完整离线包也已内置固定版本的 `ripgrep (rg)` 和 `fd`，TUI 文件搜索不再依赖目标服务器联网安装工具。

模型仍可在内部进行 reasoning，但 TUI 默认只展示最终回答，避免长分析淹没运维结论。按 `Ctrl+T` 可在当前 TUI 中临时切换，也可运行 `configure-model.sh --reasoning-display visible` 持久显示；classic chat 本身不会打印服务端的 `reasoning_content`。

## Agent 如何探索

Agent 的知识库描述 InferNex 组件关系、常见故障模式、稳定变更方法和安全边界；
实时事实始终通过工具获取，而不是让模型猜测：

- openFuyao/BKE 能力、当前集群角色、Kubernetes/LWS 工作负载和 Helm Release；
- Kubernetes Event，以及明确 Pod/容器的限长、脱敏日志；
- `diagnose` 及以上模式中的固定 NPU/CANN/网络探针，可通过管理节点、Pod exec 或预配置 SSH alias 执行；
- 检测到 Bridge 后的 InferNex CRD、status 和专属拓扑；
- Bridge 已有配置和 Ready 的稳定服务；
- 后续按稳定接口接入 infernex-checker、Eagle-Eye、Prometheus 和 EvalScope；
- 本地批准后的受限写工具，以及对应验证和回退工具。

模型不持有 kubeconfig，也不能直接执行任意 Shell、YAML、镜像或 Patch。执行通道与修改权限分离：
固定只读探针可以使用 exec/SSH，修改仍必须由独立 typed tool、Policy、批准、快照和回退约束。第一版的
部署只复用实际 Ready 的稳定服务或管理员已有的完整 profile；缺少基线时，Agent
会说明缺口，不凭空生成生产配置。

## Web 与持续扫描

本地 systemd 服务持续扫描，Dashboard 默认只监听 `127.0.0.1:8081`：

```bash
ssh -L 8081:127.0.0.1:8081 <管理节点>
```

然后访问 `http://127.0.0.1:8081/`。

通过现有 Istio/Gateway 自动发布 Dashboard 已进入 v0.5 设计，但必须先提供认证、TLS、Gateway 到
管理节点的可达性检查、Policy 批准和路由配置回退；当前版本不会默认匿名暴露运维数据。

## 文档

- [产品使用指南](docs/product-guide-zh.md)
- [离线安装](docs/offline-install-zh.md)
- [工具集与知识库设计](docs/toolsets-and-knowledge-zh.md)
- [MCP 工具目录与组件映射](docs/mcp-tool-catalog-zh.md)
- [运行模式、Policy、配置版本与 Dashboard 路由](docs/policy-modes-config-versions-zh.md)
- [v0.5 可执行路线图](docs/v0.5-roadmap-zh.md)
- [v0.5 产品与工程提案](docs/proposals/infernex-agent-v0.5-proposal-zh.md)
- [上下文预算与自动压缩](docs/context-management-zh.md)
- [本地历史日志分析与 Markdown 报告](docs/local-evidence-and-reports-zh.md)
- [CANN plog 外部持续采集](docs/plog-capture-zh.md)
- [节点与容器持续诊断 CollectorRun](docs/collector-runs-zh.md)
- [CANN/HiXL 诊断 Skill 与用户扩展](docs/skills-and-cann-hixl-zh.md)
- [Linux 终端编辑、历史与撤回](docs/terminal-interaction-zh.md)
- [openFuyao v26.06 对齐基线](docs/openfuyao-alignment-zh.md)
- [产品设计与边界](docs/product-design-zh.md)
- [变更保护与回退](docs/change-safety-zh.md)
- [安全边界](docs/security-boundaries-zh.md)
- [候选版本验证](docs/candidate-validation-zh.md)
- [v0.5 产品与工程推进提案](docs/proposals/infernex-agent-v0.5-proposal-zh.md)

Helm/Pod 安装保留给确实需要 Kubernetes 原生托管 Agent 的团队，属于高级模式，
不出现在 V1 默认 Release 下载项中。
