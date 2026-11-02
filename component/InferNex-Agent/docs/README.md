# InferNex Agent 文档入口

面向 Kubernetes 推理部署与运维，InferNex Bridge、Helm 和客户平台按环境适配。
已发布实验基线是 **alpha.22**；私域 Docker/K8s 清单、认证 Dashboard 与快照差异已在
演进分支实现，尚未发布。当前进度、版本差异与待实现项统一见[路线图](development/roadmap-zh.md)。

## 外部 Agent / 合作方

- **从这里接入**：[合作方接入指南](guides/external-agent-integration-zh.md)。
- 给调用方 Agent 的标准包：[infernex-operations Skill](../../../skills/infernex-operations/SKILL.md)，包含调用时机、环境、连接与调用示例。
- 查工具：[MCP 工具目录](reference/mcp-tool-catalog-zh.md)；接口参数以运行时 `tools/list` 为准。
- 诊断团队联调：[受限诊断委派与报告契约](development/diagnostic-subagent-development-guide-zh.md)。
- 服务端领域知识：[CANN/HiXL 内置 Skill 与扩展](guides/skills-and-cann-hixl-zh.md)。

## 安装与使用

- 安装/升级：[alpha.22 发布说明](releases/v0.5.0-alpha.22-zh.md)、[离线安装](guides/offline-install-zh.md)、[安装模式](guides/install-and-modes-zh.md)、[openEuler 管理节点](guides/host-install-openeuler-zh.md)。
- 模型与交互：[模型配置](guides/model-configuration-zh.md)、[Pi TUI](guides/pi-tui-zh.md)、[上下文管理](guides/context-management-zh.md)、[产品使用](guides/product-guide-zh.md)。
- 只读容量估算：[部署规格资源规划](guides/deployment-planning-zh.md)。
- 运维、取证与回退：[运维手册](guides/operations-runbook-zh.md)、[日志报告](guides/local-evidence-and-reports-zh.md)、[变更保护](guides/change-safety-zh.md)。
- Host/网络诊断：[SSH、PFC 与 HCCL](guides/host-network-diagnostics-zh.md)、[命令分类与审批](guides/command-approval-zh.md)、[Mooncake 网络同窗取证](guides/mooncake-prefix-hit-network-validation-zh.md)。
- 实验与记录：[SLO 与 Git/snapshot 版本记录](guides/slo-and-config-versions-zh.md)、[Bridge 渐进实验](guides/progressive-experiments-zh.md)。

## 私域环境清单（分支已实现，未发布）

- 实际操作：[登记、只读发现与持久清单](https://github.com/lsjfy-open-com/infernex-agent/blob/6bf0c0ef9981889e6ad7a8d3946ba0360ed6587e/component/InferNex-Agent/docs/guides/private-inventory-zh.md)。
- 展示与查询：[Host IP 访问、认证与快照差异](https://github.com/lsjfy-open-com/infernex-agent/blob/6bf0c0ef9981889e6ad7a8d3946ba0360ed6587e/component/InferNex-Agent/docs/guides/private-inventory-dashboard-zh.md)。
- 实现约束：[首批字段与接口合同](https://github.com/lsjfy-open-com/infernex-agent/blob/6bf0c0ef9981889e6ad7a8d3946ba0360ed6587e/component/InferNex-Agent/docs/architecture/private-deployment-contracts-zh.md)、[实现验收、fixture 与命令](https://github.com/lsjfy-open-com/infernex-agent/blob/6bf0c0ef9981889e6ad7a8d3946ba0360ed6587e/component/InferNex-Agent/docs/development/private-deployment-implementation-acceptance-zh.md)。

本阶段提供 Linux 本机 CLI/受信 stdio 清单能力和独立只读 Dashboard。
部署执行、PD 编排与能力自演进按后续增量推进；不要将设计文档当作可调用接口。

## 架构、路线图与参与开发

- 当前状态：[通用底座能力矩阵](architecture/kubernetes-first-zh.md)、[唯一当前路线图](development/roadmap-zh.md)。
- 协作：[分支与发布](development/branches-and-releases-zh.md)、[贡献规范](../../../CONTRIBUTING.md)。
- 内部实现：[代码架构](architecture/architecture.md)、[安全边界](reference/security-boundaries-zh.md)。
- 私域演进设计：[领域模型与适配契约](https://github.com/lsjfy-open-com/infernex-agent/blob/6bf0c0ef9981889e6ad7a8d3946ba0360ed6587e/component/InferNex-Agent/docs/architecture/private-deployment-evolution-zh.md)、[实施顺序与门禁](https://github.com/lsjfy-open-com/infernex-agent/blob/6bf0c0ef9981889e6ad7a8d3946ba0360ed6587e/component/InferNex-Agent/docs/development/private-deployment-implementation-plan-zh.md)、[两阶段验收补充](https://github.com/lsjfy-open-com/infernex-agent/blob/6bf0c0ef9981889e6ad7a8d3946ba0360ed6587e/component/InferNex-Agent/docs/development/private-deployment-acceptance-zh.md)。
- 下一大版本：[面向客户的课题简介](architecture/next-generation-topic-and-acceptance-zh.md)、[环境、数据与量化验收](development/next-generation-acceptance-spec-zh.md)、[实验室、案例与日志](guides/next-generation-acceptance-lab-zh.md)。
- 背景研究：[基础设施 Agent 洞察（2026-09-20）](architecture/infra-agent-industry-insights-2026-09-20-zh.md)。

## 维护与版本规则

- `guides/` 是可操作指南，`reference/` 是当前接口约束。
- `architecture/` 记录设计契约，明确区分已实现和目标；`development/` 维护路线图、验证与发布流程。
- `archive/` 保留历史提案和材料，供追溯。
- 仓内导航使用相对链接，跟随所查看的提交；对外交付固定服务端版本与 Skill 所属提交。
  历史 Release 应查看固定 tag 的文档。
- 原 InferNex 平台说明保留在[上游文档](../../../docs/upstream/README-zh.md)，不构成通用 Agent 的安装前提。
