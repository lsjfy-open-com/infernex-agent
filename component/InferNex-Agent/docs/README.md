# InferNex Agent 文档入口

产品方向：**Kubernetes 通用部署与运维 + 可选环境适配**。名称保留 InferNex Agent，但原生能力不以安装 InferNex 为前提。

| 你要做什么 | 从这里开始 |
| --- | --- |
| 安装/升级实验包 | [alpha.22 发布说明](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/releases/v0.5.0-alpha.22-zh.md)、[离线安装](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/guides/offline-install-zh.md)、[安装模式](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/guides/install-and-modes-zh.md)、[openEuler 管理节点](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/guides/host-install-openeuler-zh.md) |
| 配模型与使用 TUI | [模型配置](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/guides/model-configuration-zh.md)、[Pi TUI](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/guides/pi-tui-zh.md)、[产品使用](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/guides/product-guide-zh.md) |
| 看当前到底支持什么 | [通用底座能力矩阵](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/architecture/kubernetes-first-zh.md)、[MCP 工具目录](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/reference/mcp-tool-catalog-zh.md) |
| 只读估算同规格实例能否放入当前集群快照 | [部署规格资源规划](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/guides/deployment-planning-zh.md) |
| 下一大版本立项与两阶段验收 | [面向客户的课题简介](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/architecture/next-generation-topic-and-acceptance-zh.md)、[环境、数据与量化验收细则](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/development/next-generation-acceptance-spec-zh.md)、[实验室搭建、案例与日志数据](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/guides/next-generation-acceptance-lab-zh.md) |
| 看智谱与业界方案、效果及我们的差距 | [基础设施 Agent 洞察（2026-09-20）](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/architecture/infra-agent-industry-insights-2026-09-20-zh.md) |
| 设计原生部署/规格/均衡/客户适配 | [Kubernetes 分层契约](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/architecture/kubernetes-first-zh.md) |
| 运维、取证和回退 | [运维手册](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/guides/operations-runbook-zh.md)、[日志报告](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/guides/local-evidence-and-reports-zh.md)、[变更保护](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/guides/change-safety-zh.md) |
| SLO 对照实验 / Git + snapshot 版本记录 / Dashboard 展示 | [alpha.16 操作指南](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/guides/slo-and-config-versions-zh.md)、[alpha.20 发布说明](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/releases/v0.5.0-alpha.20-zh.md) |
| 验证 Mooncake prefix hit 超时 | [网络与交换机同窗取证](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/guides/mooncake-prefix-hit-network-validation-zh.md) |
| 参与开发与查优先级 | [当前路线图](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/development/roadmap-zh.md)、[分支与发布](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/development/branches-and-releases-zh.md)、[贡献规范](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/CONTRIBUTING.md) |
| 查内部实现 | [现有代码架构](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/architecture/architecture.md)、[安全边界](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/reference/security-boundaries-zh.md) |

## 目录规则

- `guides/`：用户可以实际操作的说明。
- `reference/`：当前接口与约束；功能未实现要明确标注。
- `architecture/`：架构决策与设计契约；区分现状和目标。
- `development/`：唯一当前路线图、验证、分支与发布流程。
- `archive/`：历史路线图、提案、社区材料和视频，供追溯，不能作为当前能力承诺。

源仓库保留的 InferNex 平台说明位于根目录 `docs/upstream/`，不等于 Agent 的安装前置条件。历史 Release 使用固定 tag 查阅当时的文档；alpha.13 安装包不被本次开发变更覆盖。

- [Host 权限切换、SSH、PFC 和 HCCL/网络诊断](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/guides/host-network-diagnostics-zh.md)

命令反复审批的分类策略、`root risk` 与边界见 [Host / full / risk 命令分类与审批](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/guides/command-approval-zh.md)（alpha.21）。

## 私域部署演进分支

- [Docker/K8s 聚合与 PD 部署：领域模型、适配契约和受控自演进](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/architecture/private-deployment-evolution-zh.md)
- [两阶段验收补充：环境、用例、证据和量化标准](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/development/private-deployment-acceptance-zh.md)

- [实施入口：顺序、首批 PR 与开工门禁](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/development/private-deployment-implementation-plan-zh.md)
- [首批字段与接口合同](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/architecture/private-deployment-contracts-zh.md)
- [首批实现验收：fixture、用例和命令](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/development/private-deployment-implementation-acceptance-zh.md)

首批领域记录、只读发现与 CLI/stdio 清单入口已在 `codex/private-deployment-evolution` 实现，尚未发布安装包。后续部署执行、PD 与能力自演进仍为规划。

- [已实现的私域清单：安装前提、登记、发现与保存](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/guides/private-inventory-zh.md)
