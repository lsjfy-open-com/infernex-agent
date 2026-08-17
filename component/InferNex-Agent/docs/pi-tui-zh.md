# Pi TUI 使用与边界

`agent/pi-agent-foundation` 分支把 Pi 作为 InferNex Agent 的交互层候选实现。完整 Pi 测试包中，
`infernex-agent chat` 默认进入 TUI；旧 Go 终端仍作为显式兼容入口保留。

## 为什么采用 Pi

Pi 已经提供成熟的终端编辑、流式输出、工具过程展示、Session 恢复/分叉、上下文压缩、模型选择和 token/context 状态。InferNex Agent 因而可以把工程投入集中到集群发现、推理故障知识、证据关联、审批和回退，而不是继续自行重写通用 Agent UI。

本项目固定验证 Pi v0.84.1。上游采用 MIT License，正式发行包必须同时携带其许可证和版本清单。

## 安装与启动

包含 Pi 的候选宿主机包仍然使用原来的一条安装命令。安装并配置模型接口后执行：

当前现场测试版本是 `v0.5.0-alpha.7`。在 Release 中只需按管理节点 CPU 架构选择一个包：

```text
infernex-agent-0.5.0-alpha.7-linux-amd64.tar.gz  # x86_64
infernex-agent-0.5.0-alpha.7-linux-arm64.tar.gz  # aarch64/openEuler A2
```

下载包和同名 `.sha256` 后执行：

```bash
sha256sum --check infernex-agent-0.5.0-alpha.7-linux-*.tar.gz.sha256
tar -xzf infernex-agent-0.5.0-alpha.7-linux-*.tar.gz
cd infernex-agent-0.5.0-alpha.7-linux-*
sudo ./install.sh
sudo infernex-agent chat
```

完整包已经包含固定版本的 Pi runtime，安装过程不会执行 `npm install`，也不会访问 npm registry。
`--skip-checksums` 只用于外层归档校验已经通过、但需要临时跳过包内逐文件校验的故障处置；正常安装
不应使用：

```bash
sudo ./install.sh --skip-checksums
```

这是独立的 alpha 测试包，不需要 Node、Bun、Go 或 Python。请保留当前稳定版安装包和安装前自动
生成的恢复点；发现阻断问题时先退出 TUI，后台 Go Agent 和现有推理实例不会因 TUI 退出而停止。

安装并配置模型接口后，日常启动命令是：

```bash
sudo infernex-agent chat
```

需要旧版逐行终端或使用旧参数时：

```bash
sudo infernex-agent chat --classic
```

带参数的 `chat --ask ...` 继续自动进入兼容 Go 终端，不改变已有自动化脚本。

恢复或选择已有会话时，把 Pi 参数放在 `--` 后：

```bash
sudo infernex-agent tui -- --resume
sudo infernex-agent tui -- --continue
```

模型地址、模型名、上下文窗口和输出预算继续来自 `/etc/infernex-agent/agent.conf`，API key 只通过子进程环境传递，不写入 Pi 的 `models.json`。Session 保存在 `/var/lib/infernex-agent/pi/sessions`。
用户不需要执行 Pi `/login`：启动器会把 InferNex 安装时配置的 OpenAI-compatible Base URL、真实
model ID、上下文窗口和可选 API key 自动迁移为本地 `infernex` provider。无 API key 的内网接口会
使用非秘密占位凭据满足 Pi 的本地 provider 可用性检查；启动前还会执行离线 auth preflight，配置
无法识别时直接报告 InferNex 配置错误，不进入互联网登录流程。

`infernex-agent tui --check` 只检查本地 provider、凭据引用和模型配置能否被 Pi 识别，并不会向模型
发送推理请求。alpha.7 对 vLLM/vLLM-Ascend 使用保守的 Chat Completions 兼容参数：保留流式输出
和 tool calls，但不发送 `store`、developer role、reasoning effort 和 strict tool schema 等不同版本
实现不一致的可选字段，并使用 `max_tokens`。

发出问题后，状态栏会依次显示 `waiting for first response event` 和 `response streaming`。20 秒内没有
收到 Pi 能解析的流式事件时，TUI 会给出等待告警但不会擅自中断仍在推理的请求；服务端错误或最终
assistant message 为空时也会直接显示原因。这样可以区分“模型仍在算”“SSE 格式未被解析”和
“服务端返回空消息”，不再只停留在无输出界面。若同一接口在 `chat --classic` 正常而 TUI 报错，
请保留告警中的 provider error、vLLM access log 对应请求，以及接口返回的首个 SSE event，作为后续
适配具体 vLLM-Ascend 版本和 tool-call parser 的证据。

TUI 固定使用 `/var/lib/infernex-agent/pi/workspace` 作为工作目录，并把该目录写入新 Session。
它不会继承安装包解压目录或运维人员执行 `sudo` 时所在的临时目录，因此升级、删除旧安装目录后
仍可恢复会话。旧 Session 如果记录的目录已经不存在，Pi 会提示在当前稳定工作目录继续；确认一次
后，新会话记录将使用稳定目录。

新安装默认 `max-output-tokens=8192`，小窗口按 context window 的 1/4 降低。该值会转换为 Pi 模型
配置的 `maxTokens`；可重新运行模型配置脚本调整，不需要重新打包或安装 Pi。Session 负责恢复一次
对话；跨 Session 的稳定知识由 Go Core 的 `infernex_search_memory`、`infernex_remember` 和
`infernex_forget_memory` 提供，保存于 `/var/lib/infernex-agent/semantic-memory`。记忆写入和忘记会像
其他写工具一样要求本机批准，历史集群事实仍需工具重新验证。

### Reasoning 展示

alpha.7 保持模型的 reasoning 和 tool parser 能力，但默认折叠 TUI 中的思维块，只显示简短状态和最终回答。这只是显示策略，不会通过 `--thinking off` 关闭模型推理。

```bash
# 持久显示或隐藏
sudo /opt/infernex-agent/bin/configure-model.sh --reasoning-display visible
sudo /opt/infernex-agent/bin/configure-model.sh --reasoning-display hidden

# 仅覆盖本次启动
infernex-agent tui --reasoning-display visible
```

运行中可按 `Ctrl+T` 临时切换。classic chat 只输出最终 `message.content`，不会打印服务端扩展字段 `reasoning_content`。

当一个 MCP 工具结果超过 16 KiB 时，TUI 不会把全文反复加入模型上下文。原文以 SHA-256
作为 Artifact ID 保存到 `/var/lib/infernex-agent/pi/artifacts`，模型先收到头尾预览，再通过
`infernex_read_artifact` 按字节偏移读取最多 16 KiB 的相关片段。状态栏会显示当前 InferNex
工具和本次进程生成的 Artifact 数量。

## 工具与安全边界

启动器固定使用 `--no-builtin-tools`，因此模型不能使用 Pi 自带的任意 Shell 和文件读写能力。`infernex.ts` 从 `http://127.0.0.1:8080/mcp` 动态加载现有 InferNex 工具：

- MCP 标记为只读的发现、日志和诊断工具可主动执行；
- 任何未明确标记只读的工具都需要当前终端确认；
- 没有交互终端时写工具默认拒绝；
- Kubernetes RBAC、Secret 脱敏、快照、变更记录、readiness 验证和回退仍由 Go 后端执行；
- Pi 不能绕过 MCP 直接执行 `kubectl`、`helm` 或宿主机命令。

Pi 自身不是安全沙箱。若启动时移除上述限制、手工加载其他扩展或直接运行原始 Pi 二进制，行为不属于 InferNex Agent 的受支持模式。

## 当前阶段

当前分支已完成 TUI 启动器、OpenAI-compatible 模型配置转换、MCP 动态工具桥接、写操作确认、
Session 目录隔离，以及大工具结果的 Artifact 化和渐进读取。进入正式 RC 前仍需完成：

1. 在 openEuler aarch64 管理节点验证 Pi standalone binary；
2. 用 GLM 5.x、Qwen 3.x 验证工具调用、压缩和恢复；
3. 将 Pi ARM64/AMD64 二进制、SHA-256 与 MIT License 固定进 Release 构建；
4. 对 Artifact 分页和审批事件做折叠面板等专用渲染；
5. 保留 TUI 与 `chat --classic` 的同任务对比报告，持续验证默认入口与兼容回退。
