# InferNex Agent 上下文管理

InferNex Agent 不会把整个对话和所有日志无限追加后直接发给模型。交互终端为每个会话维护
明确的 token 预算，在每次模型调用前执行预算、压缩和硬上限检查。

## 默认策略

默认值适合上下文窗口为 32K 的内网模型：

| 配置 | 默认值 | 作用 |
| --- | ---: | --- |
| `context-window-tokens` | 32768 | 模型输入与输出合计的硬上限 |
| `max-output-tokens` | 8192 | 每次调用预留且通过 `max_tokens` 请求的最大输出；小窗口按窗口的 1/4 派生 |
| `context-compaction-threshold` | 80 | 预计总量达到窗口的 80% 时开始压缩 |
| `context-keep-recent-turns` | 4 | 压缩时原样保留最近 4 个用户轮次及其工具链 |
| `tool-result-max-tokens` | 4096 | 单次送入模型的工具结果近似上限；更大的结果先保存为本机会话 Artifact |
| `reasoning-display` | hidden | 仅控制 TUI 是否展开 reasoning block；不关闭模型推理能力 |

处理顺序如下：

1. 大型工具结果进入历史前先写入受控会话目录，模型只接收 SHA-256、行数、小预览和 Artifact ID；
2. 模型需要更多证据时，只能通过 `infernex_read_artifact` 按起始行、最大行数或字面过滤词渐进读取；
3. 每次调用模型前估算 system prompt、工具定义、消息和输出预留量；
4. 达到阈值后，由已配置模型把较早轮次压缩为结构化工作记忆，保留事实、证据、资源名、
   判断与推测的区别、审批结果、回退状态和未完成事项；
5. 压缩调用不可用时使用确定性摘要兜底，再按需移除旧工具结果正文；
6. 仍超过硬上限时不向模型发送请求，而是提示使用 `/compact`、`/clear`、缩短问题或增大窗口。

每个聊天进程的 Artifact 默认最多保存 128 MiB，目录权限为 `0700`，文件权限为 `0600`。
Linux 宿主机默认根目录为：

```text
/var/lib/infernex-agent/chat-artifacts/<UTC时间-随机会话ID>/
```

文件名是内容 SHA-256，扩展名为 `.log`。`infernex_read_artifact` 只接受当前会话已经登记的
Artifact ID，不接受文件路径，因此不能借此读取宿主机其他文件。可使用一次性参数改变目录，
或在不希望日志落盘时关闭：

```bash
sudo infernex-agent chat --artifact-dir /data/infernex-agent-chat-artifacts
sudo infernex-agent chat --artifact-dir=
```

关闭 Artifact 后仍会按 `tool-result-max-tokens` 截断大型工具结果。当前阶段 Artifact 不进入
长期记忆，也不会被模型接口直接上传；只有模型主动渐进读取的片段会进入请求上下文。正式的
会话恢复和留存清理策略将在持久化 Session 阶段统一管理。

这里使用偏保守的本地估算器，不依赖特定厂商 tokenizer：ASCII 文本约每 4 个字符一个
token，中文等非 ASCII 字符按每字符一个 token 计算。模型服务报告的 token 数可能略有不同，
所以不应把压缩阈值设到 100%。

Agent 同时读取 Chat Completions 响应中的 `usage.prompt_tokens`、`completion_tokens` 和
`total_tokens`，在终端显示 `[tokens]` 并累计到 `/context` 和 `/usage`。`/usage` 还会显示
模型调用次数以及其中多少次响应真正携带 `usage`，便于识别“不支持统计”和“实际为零”。`usage` 是服务端实际报告的消费量，
本地 `estimated` 用于在下一次请求前保护上下文窗口，两者分别展示。部分内网网关不返回
`usage`，此时 reported 数值为 0，Agent 不会把估算值伪装成服务端精确统计。`/clear` 清除
对话上下文，但不会抹掉本进程已经消费的累计 token。

## 首次安装时设置

正常的一键交互安装会在模型 URL、model ID、API Key 后询问：

```text
模型上下文窗口 token 数 [32768]:
```

这里填写 Agent 背后的对话模型实际支持的上下文窗口，不是要部署的推理模型参数。不确定时
直接回车使用 32768。安装器会自动计算输出预留和工具结果限额。

高级非交互安装也可显式传入：

```bash
sudo ./bin/install-host.sh \
  --kubeconfig /root/admin.conf \
  --generic-kubernetes \
  --openai-base-url http://10.20.0.30:8000/v1 \
  --openai-model ops-model \
  --context-window-tokens 16384 \
  --max-output-tokens 1024
```

## 安装后随时修改

只修改窗口，其他派生值会自动重新计算：

```bash
sudo /opt/infernex-agent/bin/configure-model.sh \
  --context-window-tokens 65536 \
  --show
```

需要精细控制时：

```bash
sudo /opt/infernex-agent/bin/configure-model.sh \
  --context-window-tokens 32768 \
	--max-output-tokens 8192 \
  --context-compaction-threshold 75 \
  --context-keep-recent-turns 6 \
  --tool-result-max-tokens 3072 \
  --show
```

`--max-output-tokens` 是上限而不是要求模型必须生成这么多 token。应填写 endpoint 实际支持的单次
输出上限；长报告可使用 8192、16384 或更高，但必须小于 context window 和 compaction threshold
预算。该设置同时写入 classic chat 的 `max_tokens` 和 Pi TUI 的模型 `maxTokens`。

reasoning 展示与 token 生成预算是两个概念。默认 `hidden` 可以减少终端噪声，但 reasoning token 仍可能由模型生成并计入 completion usage。需要查看时可执行 `configure-model.sh --reasoning-display visible`，或在 TUI 中按 `Ctrl+T` 临时切换。

配置工具会验证各值、原子写入 `/etc/infernex-agent/agent.conf` 并重启服务；启动失败时恢复
原配置。也可以只对一次终端会话使用同名 `infernex-agent chat` 参数覆盖配置文件。

## 会话中观察与控制

在 `sudo infernex-agent chat` 中：

- `/context`：查看预计输入、输出预留、硬窗口、压缩阈值、消息数、压缩/裁剪次数，以及模型接口报告的本进程累计 token；
- `/usage`：单独查看模型调用数、服务端 usage 覆盖次数、累计输入/输出/总 token 和当前窗口占用估算；
- `/compact`：立即压缩可压缩的旧轮次；
- `/clear`：清空当前会话，重新从系统指令开始；
- 自动压缩发生时终端输出 `[context] ...`，便于运维人员观察工作流。
- 大结果落盘时输出 `[artifact] 路径、字节数、行数、SHA-256`；
- 每次模型轮次输出 `[model] round ...`；相同工具与参数第三次出现时输出 `[loop blocked]`；
- 达到工具轮次上限时输出 `[checkpoint]`，停止继续调用工具并让模型基于已有证据给出阶段性结论。
- 当模型以 `finish_reason=length` 截断最终回答时输出 `[model] output reached max_tokens`，最多自动续写 3 次；续写仍失败或仍被截断时保留已有正文并打印明确提示，不再静默停在半句。

当前对话消息和压缩记忆仍只存在于正在运行的 `chat` 进程内；退出终端或重启进程后不会恢复。
大型工具结果 Artifact 会保留在受控目录，便于本地核验，但当前不能恢复为新对话的上下文。
跨会话持久化、按事件时间线检索、Artifact 留存清理和服务级长期记忆属于下一阶段能力。

## 设计来源与取舍

本实现采用运维 Agent 常见的多级限制：大结果外置并渐进读取、单次送模限额、完整历史预算压缩、
重复工具调用阻断和轮次耗尽后的无工具总结。
该思路与 HolmesGPT 的上下文管理相近；会话内 `/clear`、显式会话控制也参考了 kubectl-ai 的
交互模型。InferNex Agent 不提供任意文件读取工具，而是通过当前会话内的 Artifact ID 白名单
读取受限片段，避免日志任务因一次性注入大文本迅速耗尽模型上下文。

- HolmesGPT Context Management：<https://holmesgpt.dev/dev/reference/context-management/>
- kubectl-ai：<https://github.com/GoogleCloudPlatform/kubectl-ai>
