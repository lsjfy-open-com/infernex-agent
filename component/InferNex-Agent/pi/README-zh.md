# Pi TUI

Pi 提供终端交互、会话和上下文管理，后台 Go MCP 提供集群工具。

- `/mode_change normal|root|status` 控制本机命令的真实执行身份，默认 normal。
- 本机文件、shell、SSH 统一经过 `infernex_host_exec`，Pi 原生文件/bash 工具被拦截以防绕过模式。
- root 模式需要 root 启动的 TUI；普通模式使用服务用户和 no-new-privileges。
- 支持结构化网络探针、PFC 多次采样及 HCCL MPI 测试，命令经本地终端预览批准。
- MCP 的后台身份、集群执行模式和 Kubernetes RBAC 独立；本机 root 不等于 Pod/SSH 对端 root。

参见[权限与网络诊断指南](../docs/guides/host-network-diagnostics-zh.md)和[安装指南](../docs/guides/install-and-modes-zh.md)。

本目录修改需要同时运行 `npm run typecheck` 和 `npm test`；Linux CI 还以 root 验证真实 UID 切换、
文件访问拒绝、环境隔离、超时与取消。

发布 host bundle 前，在联网构建机执行一次 `npm ci`，再由
`scripts/offline/build-host-bundle.sh --pi-runtime-dir ...` 调用锁定版本的 esbuild，把本目录代码和
MCP stdio client 打成单个自包含 ESM，仍安装为 `/opt/infernex-agent/pi/infernex.ts`。打包脚本本身
不会运行 npm 或访问网络，并会从独立临时路径导入打包暂存副本，确认它不依赖构建机的 `node_modules`
或绝对路径。完全离线的构建机可传入预先生成的 `--pi-extension-bundle` 和配套的
`--pi-extension-licenses`；联网构建机可用
`npm run build:extension -- /path/infernex.mjs /path/THIRD_PARTY_LICENSES.txt` 生成这两个文件。
目标主机运行时无需 npm，也不安装独立 SDK 依赖树。
