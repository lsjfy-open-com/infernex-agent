# InferNex Agent 文档入口

产品方向：**Kubernetes 通用部署与运维 + 可选环境适配**。名称保留 InferNex Agent，但原生能力不以安装 InferNex 为前提。

| 你要做什么 | 从这里开始 |
| --- | --- |
| 安装/升级实验包 | [离线安装](guides/offline-install-zh.md)、[安装模式](guides/install-and-modes-zh.md)、[openEuler 管理节点](guides/host-install-openeuler-zh.md) |
| 配模型与使用 TUI | [模型配置](guides/model-configuration-zh.md)、[Pi TUI](guides/pi-tui-zh.md)、[产品使用](guides/product-guide-zh.md) |
| 看当前到底支持什么 | [通用底座能力矩阵](architecture/kubernetes-first-zh.md)、[MCP 工具目录](reference/mcp-tool-catalog-zh.md) |
| 设计原生部署/规格/均衡/客户适配 | [Kubernetes 分层契约](architecture/kubernetes-first-zh.md) |
| 运维、取证和回退 | [运维手册](guides/operations-runbook-zh.md)、[日志报告](guides/local-evidence-and-reports-zh.md)、[变更保护](guides/change-safety-zh.md) |
| 参与开发与查优先级 | [当前路线图](development/roadmap-zh.md)、[分支与发布](development/branches-and-releases-zh.md)、[贡献规范](https://github.com/lsjfy-open-com/infernex-agent/blob/develop/CONTRIBUTING.md) |
| 查内部实现 | [现有代码架构](architecture/architecture.md)、[安全边界](reference/security-boundaries-zh.md) |

## 目录规则

- `guides/`：用户可以实际操作的说明。
- `reference/`：当前接口与约束；功能未实现要明确标注。
- `architecture/`：架构决策与设计契约；区分现状和目标。
- `development/`：唯一当前路线图、验证、分支与发布流程。
- `archive/`：历史路线图、提案、社区材料和视频，供追溯，不能作为当前能力承诺。

源仓库保留的 InferNex 平台说明位于根目录 `docs/upstream/`，不等于 Agent 的安装前置条件。历史 Release 使用固定 tag 查阅当时的文档；alpha.13 安装包不被本次开发变更覆盖。
