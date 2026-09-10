# Pi TUI 使用与边界

Pi 是 InferNex Agent 的交互层。完整 Pi 测试包中，
`infernex-agent chat` 默认进入 TUI；旧 Go 终端仍作为显式兼容入口保留。

> develop 的新增权限模式、网络/PFC/HCCL 工具见[Host 网络诊断指南](host-network-diagnostics-zh.md)。
> `/mode_change normal|root` 控制本机命令 UID；本机文件和 shell 使用 `infernex_host_exec`，原生文件/bash 工具已拦截。
> 以下 alpha.13 工作区描述仅适用于旧包，新模式不以工作区路径规则代替 OS 权限。

## 工具调用的紧凑显示

develop 默认将每次工具调用显示为一行“状态 · 操作 · 目标 · 耗时”，长参数、命令和结果不占据报告区域。
按 `Ctrl+O` 展开/折叠工具详情；自定义键位对应 Pi 的 `app.tools.expand`。打开或恢复会话默认折叠。
失败、超时、取消在摘要中明确标记；批准执行时仍展示完整命令。这里只改变终端渲染，不裁剪模型收到的
结果、历史证据或 Artifact 内容。本改动尚未包含在 alpha.14 安装包中。

## 为什么采用 Pi

Pi 已经提供成熟的终端编辑、流式输出、工具过程展示、Session 恢复/分叉、上下文压缩、模型选择和 token/context 状态。InferNex Agent 因而可以把工程投入集中到集群发现、推理故障知识、证据关联、审批和回退，而不是继续自行重写通用 Agent UI。

本项目固定验证 Pi v0.84.1。上游采用 MIT License，正式发行包必须同时携带其许可证和版本清单。

## 安装与启动

包含 Pi 的候选宿主机包仍然使用原来的一条安装命令。安装并配置模型接口后执行：

当前现场测试版本是 `v0.5.0-alpha.13`。在 Release 中只需按管理节点 CPU 架构选择一个包：

```text
infernex-agent-0.5.0-alpha.13-linux-amd64.tar.gz  # x86_64
infernex-agent-0.5.0-alpha.13-linux-arm64.tar.gz  # aarch64/openEuler A2
```

下载包和同名 `.sha256` 后执行：

```bash
sha256sum --check infernex-agent-0.5.0-alpha.13-linux-*.tar.gz.sha256
tar -xzf infernex-agent-0.5.0-alpha.13-linux-*.tar.gz
cd infernex-agent-0.5.0-alpha.13-linux-*
sudo ./install.sh
sudo infernex-agent chat
```

完整包已经包含固定版本的 Pi runtime，以及 Pi 文件搜索使用的 `ripgrep (rg)` 和 `fd`。安装过程
不会执行 `npm install`，不会访问 npm registry，也不会通过 dnf/yum 下载工具。两个工具安装到
`/opt/infernex-agent/tools/bin`，只在受支持的 InferNex TUI 子进程 PATH 中优先启用，不覆盖宿主机
已有的 `/usr/local/bin/rg` 或 `/usr/local/bin/fd`。
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

TUI 默认把**执行命令时的当前目录**作为文件工作区，并继承启动用户的操作系统权限。以 root 从
`/data/array/case-01` 启动时，可直接只读遍历该磁盘阵列目录，无需 `configure-evidence`：

```bash
cd /data/array/case-01
sudo infernex-agent tui
# 或者不切换目录
sudo infernex-agent tui --workspace /data/array/case-01
```

Session 仍单独保存在 `/var/lib/infernex-agent/pi/sessions`，不会写进工作区。恢复 Session 时对应挂载
目录必须仍然存在；磁盘阵列未挂载时应先恢复挂载，不要把会话静默切换到另一个目录。

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

alpha.13 启用受控的 Pi 文件工具，并由 InferNex 扩展实施工作区和批准策略：

- `read`、`grep`、`find`、`ls` 在当前工作区内免确认；
- 相对路径和绝对路径均不能越出工作区，符号链接也不能逃逸；
- `write`、`edit` 每次必须由当前 TUI 确认；无交互终端时拒绝；
- `bash` 暂按每条命令确认，因为单靠命令文本无法可靠证明其只读；
- 权限来自启动用户；Agent 不绕过 Linux DAC/ACL，也不会自动 chmod/chown 磁盘阵列。

`infernex.ts` 同时从 `http://127.0.0.1:8080/mcp` 动态加载现有 InferNex 集群工具：

- MCP 标记为只读的发现、日志和诊断工具可主动执行；
- 任何未明确标记只读的工具都需要当前终端确认；
- 没有交互终端时写工具默认拒绝；
- Kubernetes RBAC、Secret 脱敏、快照、变更记录、readiness 验证和回退仍由 Go 后端执行；
- `kubectl`、`helm` 或宿主机 shell 只有在本机批准后才可执行；结构化 MCP 工具仍是首选。

Pi 自身不是安全沙箱。若启动时移除上述限制、手工加载其他扩展或直接运行原始 Pi 二进制，行为不属于 InferNex Agent 的受支持模式。

## 当前阶段

当前分支已完成 TUI 启动器、OpenAI-compatible 模型配置转换、MCP 动态工具桥接、写操作确认、
Session 目录隔离，以及大工具结果的 Artifact 化和渐进读取。进入正式 RC 前仍需完成：

1. 在 openEuler aarch64 管理节点验证 Pi standalone binary；
2. 用 GLM 5.x、Qwen 3.x 验证工具调用、压缩和恢复；
3. 将 Pi ARM64/AMD64 二进制、SHA-256 与 MIT License 固定进 Release 构建；
4. 对 Artifact 分页和审批事件做折叠面板等专用渲染；
5. 保留 TUI 与 `chat --classic` 的同任务对比报告，持续验证默认入口与兼容回退。
