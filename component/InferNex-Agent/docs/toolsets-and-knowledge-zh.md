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

Agent 不是新的集群控制器，不拥有推理工作负载的最终状态。InferNexService 和
Bridge 仍是部署与状态的权威来源。

## Toolsets

| 工具集 | V1 行为 |
| --- | --- |
| `infernex/core` | 发现服务、检查 spec/status、Bridge profile 和实际拓扑 |
| `kubernetes/events` | 关联 InferNex 对象、工作负载、Pod 与 Event |
| `kubernetes/logs` | 有界读取 current/previous logs，脱敏并生成跨组件时间线 |
| `infernex/change` | 稳定基线部署、状态验证、changeId 和精确回退 |
| `infernex/experiments` | 一次增加一个批准特性，对比稳定基线并停止回归阶段 |
| `infernex/hardware` | 后续接入 infernex-checker/Eagle-Eye 的连通性、带宽和时延报告 |
| `infernex/evaluation` | 后续接入 warmup、EvalScope 单轮/多轮测试及报告 |

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
