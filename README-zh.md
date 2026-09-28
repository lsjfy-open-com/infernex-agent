# InferNex Agent

[English](README.md) · [文档入口](component/InferNex-Agent/docs/README.md)

面向推理服务的 Kubernetes 通用部署与运维 Agent，InferNex、Helm 和客户平台通过环境适配接入。Agent 使用当前 kubeconfig，通用发现、日志和诊断不要求安装 InferNex。

**当前边界**：alpha.20 在完整 Pi TUI、证据/诊断和 Bridge 受控部署/恢复之上，交付原生部署规格资源规划的第一个只读切片。它只估算同一种单实例 Profile 能否放入当前身份可见的集群快照；不创建 Kubernetes 资源，不预留容量，不批准部署，也不证明业务性能。原生部署写路径、请求级均衡和通用故障闭环仍待实现。详见[能力矩阵与分层契约](https://github.com/lsjfy-open-com/infernex-agent/blob/develop/component/InferNex-Agent/docs/architecture/kubernetes-first-zh.md)。

alpha.20 保留 `sudo ./install.sh` 的无参数 kubeconfig 自动发现和 Host Dashboard 实时 YAML。alpha.20 安装包仍待 Release CI 发布及硬件现场验证；规划结论只是当前读取权限下的估算。参见[发布说明](https://github.com/lsjfy-open-com/infernex-agent/blob/develop/component/InferNex-Agent/docs/releases/v0.5.0-alpha.20-zh.md)与[部署规格资源规划指南](https://github.com/lsjfy-open-com/infernex-agent/blob/develop/component/InferNex-Agent/docs/guides/deployment-planning-zh.md)。

- 安装实验包：Release CI 发布后从 [alpha.20 Release](https://github.com/lsjfy-open-com/infernex-agent/releases/tag/infernex-agent-v0.5.0-alpha.20) 按管理节点架构选择归档及校验文件，解压后运行 `sudo ./install.sh`；已经是 root 时运行 `./install.sh`。
- 使用：[安装指南](component/InferNex-Agent/docs/guides/offline-install-zh.md)、[模型配置](component/InferNex-Agent/docs/guides/model-configuration-zh.md)、[Pi TUI](component/InferNex-Agent/docs/guides/pi-tui-zh.md)。
- 开发：[当前路线图](component/InferNex-Agent/docs/development/roadmap-zh.md)、[分支与发布](component/InferNex-Agent/docs/development/branches-and-releases-zh.md)、[贡献规范](CONTRIBUTING.md)。

`develop` 是统一开发入口；main 保留历史已合并基线。已发布包以固定 tag 追溯。仓库中保留的[原 InferNex 平台说明](docs/upstream/README-zh.md)不构成 Agent 在其他公司 Kubernetes 环境的安装前提。
