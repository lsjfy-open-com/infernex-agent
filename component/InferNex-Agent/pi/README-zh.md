# Pi TUI

Pi 提供终端交互、会话和上下文管理，后台 Go MCP 提供集群工具。

- `/mode_change normal|root|status` 控制本机命令的真实执行身份，默认 normal。
- 本机文件、shell、SSH 统一经过 `infernex_host_exec`，Pi 原生文件/bash 工具被拦截以防绕过模式。
- root 模式需要 root 启动的 TUI；普通模式使用服务用户和 no-new-privileges。
- 支持结构化网络探针、PFC 多次采样及 HCCL MPI 测试，命令经本地终端预览批准。
- MCP 的后台身份、集群执行模式和 Kubernetes RBAC 独立；本机 root 不等于 Pod/SSH 对端 root。

参见[权限与网络诊断指南](../docs/guides/host-network-diagnostics-zh.md)和[安装指南](../docs/guides/install-and-modes-zh.md)。

本目录修改需要同时运行 `npm run typecheck` 和 `npm test`；Linux CI 还以 root 验证真实 UID 切换、
文件访问拒绝、环境隔离、超时与取消。安装包包含 infernex.ts 和 host-tools.ts，运行时无需 npm/Node 安装。
