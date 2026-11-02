# InferNex Agent

[English](README.md) · [文档入口](component/InferNex-Agent/docs/README.md) · [外部 Agent / 合作方接入](component/InferNex-Agent/docs/guides/external-agent-integration-zh.md) · [接入 Skill](skills/infernex-operations/SKILL.md)

面向推理服务的 Kubernetes 通用部署与运维 Agent，InferNex、Helm 和客户平台通过环境适配接入。Agent 使用当前 kubeconfig，通用发现、日志和诊断不要求安装 InferNex。

**当前边界**：alpha.22 修复 Pi TUI 在持续工具调用中的上下文自动压缩，并从压缩后的会话单次续行。它保留 alpha.21 的 Host 命令分类和当前会话显式 `root risk`，以及 alpha.20 的第一个原生部署规格资源规划只读切片。Linux 身份、Kubernetes RBAC、工具参数与后台一致性检查继续生效；原生部署写路径、请求级均衡和通用故障闭环仍待实现。详见[能力矩阵与分层契约](component/InferNex-Agent/docs/architecture/kubernetes-first-zh.md)。

alpha.22 保留 `sudo ./install.sh` 的无参数 kubeconfig 自动发现、Host Dashboard 实时 YAML 和 alpha.20 的只读资源规划。Linux 测试与双架构安装包门禁已完成，硬件现场仍待验证；规划结论仍只是当前读取权限下的估算。参见[发布说明](component/InferNex-Agent/docs/releases/v0.5.0-alpha.22-zh.md)与[上下文管理说明](component/InferNex-Agent/docs/guides/context-management-zh.md)。

- 安装实验包：从 [alpha.22 Release](https://github.com/lsjfy-open-com/infernex-agent/releases/tag/infernex-agent-v0.5.0-alpha.22) 按管理节点架构选择归档及校验文件，解压后运行 `sudo ./install.sh`；已经是 root 时运行 `./install.sh`。升级后退出旧 TUI 并重新启动，使压缩设置生效。
- 使用：[安装指南](component/InferNex-Agent/docs/guides/offline-install-zh.md)、[模型配置](component/InferNex-Agent/docs/guides/model-configuration-zh.md)、[Pi TUI](component/InferNex-Agent/docs/guides/pi-tui-zh.md)。
- 开发：[当前路线图](component/InferNex-Agent/docs/development/roadmap-zh.md)、[分支与发布](component/InferNex-Agent/docs/development/branches-and-releases-zh.md)、[贡献规范](CONTRIBUTING.md)。

`develop` 是统一开发入口；main 保留历史已合并基线。已发布包以固定 tag 追溯。仓库中保留的[原 InferNex 平台说明](docs/upstream/README-zh.md)不构成 Agent 在其他公司 Kubernetes 环境的安装前提。

## 外部 Agent 与合作方接入

从[合作方接入指南](component/InferNex-Agent/docs/guides/external-agent-integration-zh.md)开始，将
[infernex-operations Skill](skills/infernex-operations/SKILL.md)完整目录交给调用方 Agent，再配置
运维提供的 MCP 连接。Skill 说明调用时机、环境、工具流程、示例和失败处理；诊断服务优先使用
带认证与 namespace scope 的诊断委派端点。主 HTTP MCP 没有内置认证。

CANN/HiXL 内置 Skill 继续提供服务端诊断知识；调用方接入 Skill 单独分发，当前 alpha.22
安装包不会自动包含新增文件。

## 开发进展（2026-11-02 核对）

`codex/private-deployment-evolution` 的 `6bf0c0e` 已实现 Docker/K8s 只读发现、私域清单持久化、
本机 CLI/stdio 工具，以及带认证的清单 Dashboard 和快照差异。这些增量尚未进入 alpha.22。
类型化 Docker/原生 K8s 部署、PD 编排与能力自演进仍待实现，具体状态以
[当前路线图](component/InferNex-Agent/docs/development/roadmap-zh.md)为准。
