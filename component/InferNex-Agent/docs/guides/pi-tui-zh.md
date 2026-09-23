# Pi TUI 使用与边界

Pi 是 InferNex Agent 的交互层。完整 Pi 测试包中，
`infernex-agent chat` 默认进入 TUI；旧 Go 终端仍作为显式兼容入口保留。

> 权限模式、网络/PFC/HCCL 工具见[Host 网络诊断指南](host-network-diagnostics-zh.md)。
> `/mode_change normal|root` 控制本机命令 UID；本机文件和 shell 使用 `infernex_host_exec`，原生文件/bash 工具已拦截。
> 以下 alpha.13 工作区描述仅适用于旧包，新模式不以工作区路径规则代替 OS 权限。

## 工具调用的紧凑显示

alpha.15 默认将每次工具调用显示为一行“状态 · 操作 · 目标 · 耗时”，长参数、命令和结果不占据报告区域。
按 `Ctrl+O` 展开/折叠工具详情；自定义键位对应 Pi 的 `app.tools.expand`。打开或恢复会话默认折叠。
失败、超时、取消在摘要中明确标记；批准执行时仍展示完整命令。这里只改变终端渲染，不裁剪模型收到的
结果、历史证据或 Artifact 内容。此功能从 alpha.15 安装包开始提供。

## 为什么采用 Pi

Pi 已经提供成熟的终端编辑、流式输出、工具过程展示、Session 恢复/分叉、上下文压缩、模型选择和 token/context 状态。InferNex Agent 因而可以把工程投入集中到集群发现、推理故障知识、证据关联、审批和回退，而不是继续自行重写通用 Agent UI。

本项目固定验证 Pi v0.84.1。上游采用 MIT License，正式发行包必须同时携带其许可证和版本清单。

## 安装与启动

包含 Pi 的候选宿主机包仍然使用原来的一条安装命令。安装并配置模型接口后执行：

当前现场测试版本是 `v0.5.0-alpha.15`。在 Release 中只需按管理节点 CPU 架构选择一个包：

```text
infernex-agent-0.5.0-alpha.15-linux-amd64.tar.gz  # x86_64
infernex-agent-0.5.0-alpha.15-linux-arm64.tar.gz  # aarch64/openEuler A2
```

下载包和同名 `.sha256` 后执行：

```bash
sha256sum --check infernex-agent-0.5.0-alpha.15-linux-*.tar.gz.sha256
tar -xzf infernex-agent-0.5.0-alpha.15-linux-*.tar.gz
cd infernex-agent-0.5.0-alpha.15-linux-*
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

TUI 使用 `/mode_change` 选定的本机身份执行文件与 shell 操作。`normal` 身份固定使用
`infernex-agent` 用户及其 home，清除继承能力并禁止重新提权；`root` 身份使用启动时的工作区，且要求
通过 sudo 启动 TUI。需要 root 检查 `/data/array/case-01` 时：

```bash
cd /data/array/case-01
sudo infernex-agent tui
# 或者不切换目录
sudo infernex-agent tui --workspace /data/array/case-01
```

随后执行 `/mode_change root` 或 `/mode_change root full`。Session 仍单独保存在
`/var/lib/infernex-agent/pi/sessions`，不会写进工作区。恢复 Session 时对应挂载目录必须仍然存在。

新安装默认 `max-output-tokens=8192`，小窗口按 context window 的 1/4 降低。该值会转换为 Pi 模型
配置的 `maxTokens`；可重新运行模型配置脚本调整，不需要重新打包或安装 Pi。Session 负责恢复一次
对话；跨 Session 的稳定知识由 Go Core 的 `infernex_search_memory`、`infernex_remember` 和
`infernex_forget_memory` 提供，保存于 `/var/lib/infernex-agent/semantic-memory`。记忆写入和忘记在
manual 模式要求本机批准，在 full 模式仅对已验证的任务知识自动执行；历史集群事实仍需工具重新验证。

### 报告和记忆的可读名称（alpha.15）

新报告和记忆以标题/主题关键词加 UTC 日期时间命名，例如：

- 报告：`mooncake-prefix-hit-超时_2026-09-10_12-30-00Z.md`
- 记忆：`pfc背压排查_2026-09-10_12-30-00Z.json`

中文关键词保留，路径分隔符和控制字符会清理；同一秒内重名追加 `-2`、`-3` 等编号，不覆盖已有文件。
工具返回 `name` 和 `path`，对话优先展示可读名称；`id` 用于工具引用，各存储目录下的 `.index/` 提供
ID 到文件名的直接索引。报告 ID 为内容 SHA-256，记忆 ID 为生成后保持不变的 hash 标识，忘记操作
不会改变它。记忆搜索支持关键词、日期及完整 ID；按 ID 精确查询仍执行集群隔离、过期和忘记过滤。

旧版文件无需手工重命名，旧 ID 继续有效，列表和搜索会补充可读名称。索引缺失时可从源文件重建，
正常按 ID 读取无需扫描整个目录。已有文件原路径保留，以免破坏历史引用；升级不会追溯改写 alpha.14
创建的文件。工具原始日志 Artifact 的内容 hash 和证据引用协议不受影响。

### 完全访问与连续执行（alpha.15）

执行身份与批准策略独立设置：

```text
/mode_change full          # 当前身份 + 完全访问
/mode_change root full     # root 身份 + 完全访问（TUI 须由 sudo 启动）
/mode_change normal full   # infernex-agent 身份 + 完全访问
/mode_change manual        # 当前身份 + 逐次批准
/mode_change status
```

开启后再输入任务。完全访问是对当前会话内任务的低影响操作预授权，不改变 Kubernetes RBAC、远端
SSH 身份或后台服务 UID。每次新建/恢复会话仍从 `normal + manual` 开始；切换时必须无正在运行的
模型或工具操作。状态栏同时显示身份和批准策略，工具详情记录自动执行或人工批准。

| 操作 | 完全访问模式 |
| --- | --- |
| 集群只读查询、固定诊断探针、PFC/网络计数器、有限连通性探测 | 自动执行 |
| 受支持的单条只读本机/SSH/Pod 命令、最多 60 秒的 sleep 等待 | 自动执行 |
| 有界 plog/CollectorRun 采集、报告、任务所需的已验证记忆写入/软删除 | 自动执行 |
| 集群部署/删除/实验、其他未分类 MCP 写操作 | 人工批准 |
| 重启、配置/路由修改、HCCL/iperf 压测 | 人工批准 |
| 任意脚本/解释器、重定向、管道及无法可靠分类的 shell | 人工批准；优先改用结构化诊断工具 |

只读 shell 支持常用 `ls/cat/head/tail/grep`、系统/网络状态，以及受限的 `kubectl get/describe/logs/exec`
和 SSH 读取。它不靠模型声称 `risk=safe` 或 `confirm=true` 来放行，也不尝试把任意 shell 当作安全语言解析。

模型给出阶段报告却未完成任务时会自动续跑。任务完成并验证后调用 `infernex_task_status complete`，
真实缺少输入/外部依赖时使用 `blocked`，随后给出最终报告。Esc 取消、人工拒绝、模型错误不会被自动续跑
覆盖；连续三次结束且没有新工具证据时暂停并提示阻塞，避免空转。已有采集任务不会因 Esc 或模式切换自动
停止，需要通过采集任务工具管理。此机制不保证故障模型永不出错，也不取消集群影响操作的人为判断。

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
