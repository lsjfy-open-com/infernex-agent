# Pi TUI 基础实现

本目录是 `agent/pi-agent-foundation` 分支的实验性 TUI。它复用 Pi 的终端界面、Session、上下文压缩和 token 展示，但不把 Pi 当成 Kubernetes 后端。

安全结构如下：

- Pi 只负责模型循环和终端交互；
- `infernex.ts` 从本机 InferNex Agent `/mcp` 动态发现工具；
- 启动器禁用 Pi 内置 `bash`、`read`、`write`、`edit` 等 coding tools；
- MCP 标记为只读的工具可自动执行，其他工具必须由交互终端确认；
- 快照、变更记录、验证和回退仍由现有 Go 服务处理。

开发验证（Pi v0.84.1）：

```bash
sudo install -m 0755 pi /opt/infernex-agent/bin/pi
sudo install -d -m 0755 /opt/infernex-agent/pi
sudo install -m 0644 infernex.ts /opt/infernex-agent/pi/infernex.ts
sudo /opt/infernex-agent/bin/tui.sh
```

正式离线包将在完成真实 A2/openEuler aarch64 验证后，把固定版本的 Pi Linux ARM64/AMD64 二进制及许可证一并打入宿主机包。当前分支不改变既有 `infernex-agent chat`，便于并行对比。

开发构建时先按 `upstream.json` 下载并校验对应架构的官方归档，解压后执行：

```bash
./scripts/offline/build-host-bundle.sh \
  --architecture arm64 \
  --pi-runtime-dir /path/to/pi
```
