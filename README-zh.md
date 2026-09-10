# InferNex Agent

[English](README.md) · [文档入口](component/InferNex-Agent/docs/README.md)

面向推理服务的 Kubernetes 通用部署与运维 Agent，InferNex、Helm 和客户平台通过环境适配接入。Agent 使用当前 kubeconfig，通用发现、日志和诊断不要求安装 InferNex。

**当前边界**：alpha.14 已有完整 Pi TUI、证据/诊断和 Bridge 受控部署/恢复；原生按规格自动部署、请求级均衡和通用故障闭环仍待实现。不要把架构目标当成已发布功能。详见[能力矩阵与分层契约](component/InferNex-Agent/docs/architecture/kubernetes-first-zh.md)。

alpha.14 新增 `/mode_change normal|root`、SSH/网络工具、带时间戳的 PFC 采样和 MPI HCCL 测试，参见[Host 网络诊断指南](component/InferNex-Agent/docs/guides/host-network-diagnostics-zh.md)。Mooncake prefix hit 超时的验证流程见 Release 说明。

- 安装实验包：[alpha.14 Release](https://github.com/lsjfy-open-com/infernex-agent/releases/tag/infernex-agent-v0.5.0-alpha.14)，按管理节点架构选择归档及校验文件，解压后 `sudo ./install.sh`。
- 使用：[安装指南](component/InferNex-Agent/docs/guides/offline-install-zh.md)、[模型配置](component/InferNex-Agent/docs/guides/model-configuration-zh.md)、[Pi TUI](component/InferNex-Agent/docs/guides/pi-tui-zh.md)。
- 开发：[当前路线图](component/InferNex-Agent/docs/development/roadmap-zh.md)、[分支与发布](component/InferNex-Agent/docs/development/branches-and-releases-zh.md)、[贡献规范](CONTRIBUTING.md)。

`develop` 是统一开发入口；main 保留历史已合并基线。已发布包以固定 tag 追溯。仓库中保留的[原 InferNex 平台说明](docs/upstream/README-zh.md)不构成 Agent 在其他公司 Kubernetes 环境的安装前提。
