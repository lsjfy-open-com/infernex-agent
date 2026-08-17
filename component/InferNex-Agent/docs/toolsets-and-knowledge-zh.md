# InferNex Agent 工具集与知识库设计

## 设计来源

InferNex Agent V1 借鉴三类成熟模式：

- [kubectl-ai](https://github.com/GoogleCloudPlatform/kubectl-ai) 与
  [K8sGPT](https://k8sgpt.ai/docs/reference/cli)：本地 CLI 使用当前 kubeconfig，
  提供简洁安装入口；
- [HolmesGPT](https://github.com/HolmesGPT/holmesgpt)：模型运行 Agentic loop，
  按需选择 [Kubernetes toolsets](https://docs.robusta.dev/improve_holmes_docs/configuration/holmesgpt/toolsets/kubernetes.html)，
  并用 runbook/知识库解释证据；
- k9s：从当前 context 自动发现资源，不让用户先填写 namespace 参数。

[kagent](https://www.kagent.dev/docs/kagent/introduction/installation) 的集群内
Controller/CRD 形态保留为参考，但不是 V1 默认架构。

## 运行结构

```text
自然语言
   ↓
本地 Agent runtime（模型、会话、计划、审批）
   ↓
toolset registry ── knowledge/runbooks
   ↓
kubectl/Kubernetes API + InferNex 已有组件接口
```

Agent 不是新的集群控制器，不拥有推理工作负载的最终状态。openFuyao 的 BKECluster /
BKENode 与 bkeagent/Cluster API 是集群生命周期权威；Helm Release 和其原生资源是
InferNex 主 Chart 部署权威；只有实际使用 Bridge 时，InferNexService/Bridge 才是
该入口的部署与状态权威。

离线包内置 CANN Runtime 与 HiXL/LLM DataDist 诊断 Skill；详细使用与用户扩展格式见
[CANN/HiXL 诊断 Skill 与用户扩展](skills-and-cann-hixl-zh.md)。Skill 负责领域诊断流程和版本化知识，
typed MCP tool 负责实时证据与权限边界，两者不能互相替代。

## Toolsets

逐个 MCP tool 的当前实现、发布条件和计划项见
[MCP 工具目录与组件映射](mcp-tool-catalog-zh.md)。

| 工具集 | V1 行为 |
| --- | --- |
| `openfuyao/discovery` | 识别当前 kubeconfig 的 API Server、引导/管理/业务角色及 BKE、LWS、Gateway、Bridge、KServe、监控能力 |
| `kubernetes/core` | 汇总节点/NPU资源，列出 Deployment、StatefulSet、DaemonSet、LWS、Pod 与 Service |
| `helm/inventory` | 只从 metadata 获取 Release 名称、命名空间、修订号和状态，不读取 Release Secret 数据 |
| `infernex/core` | 发现服务、检查 spec/status、Bridge profile 和实际拓扑 |
| `kubernetes/events` | 查询全局/命名空间或指定对象的近期 Event，不要求 InferNex owner 标签 |
| `kubernetes/logs` | 对明确 Pod/容器有界读取 current/previous logs 并脱敏；Bridge 诊断再生成跨组件时间线 |
| `infernex/change` | 稳定基线部署、状态验证、changeId 和精确回退 |
| `infernex/experiments` | 一次增加一个批准特性，对比稳定基线并停止回归阶段 |
| `infernex/hardware` | 后续接入 infernex-checker/Eagle-Eye 的连通性、带宽和时延报告 |
| `infernex/evaluation` | 后续接入 warmup、EvalScope 单轮/多轮测试及报告 |

通用 Kubernetes 读取采用 discovery + GET/LIST，而不是为每种资源硬编码一个工具。它覆盖当前
kubeconfig/RBAC 可见的原生资源和 CRD，支持 namespace、name、label/field selector、limit 与
continuation token。Secret payload、任意写入、exec 和 shell 不在该通道内；常用资源仍优先使用
专用工具，以获得更稳定的小型结构化结果。

工具输出必须限长、结构化和可溯源。大规模日志在本地过滤，不能把完整集群对象或
无限日志直接塞入模型上下文。

## Knowledge base

知识库保存相对稳定的认知：

- InferNex CRD、Bridge、Hermes、Mooncake、PD Orchestrator、Eagle-Eye 的职责关系；
- vLLM/vLLM-Ascend、CANN、HCCL/RDMA 和 KV 传输的常见故障模式；
- “稳定配置 + 单一变化”的部署/实验方法；
- 组织批准的 runbook、故障案例和验收规则；
- 每个工具的前置条件、风险、验证和回退语义。

实时资源、日志、指标和配置不写死在知识库中，必须在每次任务中通过工具重新发现。
工具结果是证据，不是提示词指令。

## 权限原则

- 默认使用当前 kubectl context，与 kubectl-ai/K8sGPT 的本地 CLI 一致；
- 模型不直接获得 kubeconfig，只有 typed tools 可以访问；
- 只读工具可主动调用；写工具必须显示计划并由本机用户确认；
- 高合规环境可启用独立 ServiceAccount/RBAC；
- 不向模型开放通用 Shell、任意 YAML 或任意 Kubernetes Patch。
