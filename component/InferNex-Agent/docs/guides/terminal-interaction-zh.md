# InferNex Agent Linux 终端交互

`sudo infernex-agent chat --classic` 使用真正的 readline 行编辑器，而不是简单地从标准输入逐行读取。
完整 Pi 测试包中的 `sudo infernex-agent chat` 默认进入 TUI；本文只描述兼容终端。
它适用于 XShell、SSH 和 Linux 控制台，并与 kubectl-ai 的普通终端模式采用相同的基础交互
方案。

## 发送前编辑

| 按键 | 功能 |
| --- | --- |
| `Left` / `Right` | 在当前输入中移动光标 |
| `Home` / `End` | 移到行首或行尾 |
| `Backspace` / `Delete` | 删除光标前或光标处字符 |
| `Ctrl+W` | 删除光标前一个词 |
| `Ctrl+U` | 清空当前输入行 |
| `Up` / `Down` | 调出本次 `chat` 进程中的历史输入 |
| `Ctrl+C` | 取消并清空当前输入，不退出 Agent |
| `Ctrl+D` | 在空行退出 Agent |
| `Tab` | 补全 `/help`、`/undo` 等内置命令 |

中文输入、粘贴文本和光标编辑都由 readline 处理，不再依赖不同 SSH 客户端对退格键的
默认解释。

## 发送后发现语句错误

执行：

```text
/undo
```

Agent 会从当前模型上下文中删除最后一个用户请求，以及由该请求产生的 assistant 和 tool
消息。随后按 `Up` 调出刚才的文字，修改后重新发送。

需要特别注意：`/undo` 是“对话撤回”，不是“集群回滚”。如果错误语句已经触发并批准了
写操作，删除对话不会撤销 Kubernetes、Helm 或 InferNex 资源变化；必须使用对应 change ID、
部署回退或恢复流程。写工具仍然要求本机明确输入 `yes`。

## 内置命令

| 命令 | 作用 |
| --- | --- |
| `/help` | 显示命令和按键帮助 |
| `/context` | 查看当前上下文预算和压缩统计 |
| `/usage` | 查看模型调用次数、接口实际返回 usage 的次数、累计 token 与当前窗口占用估算 |
| `/compact` | 立即压缩可压缩的旧轮次 |
| `/undo` | 删除最后一个尚可定位的用户轮次及其响应 |
| `/clear` | 清空整个当前会话 |
| `/exit` | 退出终端 |

输入历史和对话消息默认只保存在当前进程内。大型日志或工具结果可能按[上下文管理](context-management-zh.md)
写入权限隔离的 SHA-256 Artifact；它们不是可恢复的聊天历史。后续跨会话历史会采用权限隔离、
可清理策略，并允许用户查看和删除记忆。

## TTY 与管道模式

正常 XShell/SSH 会话可以检查：

```bash
test -t 0 && echo TTY || echo non-TTY
```

TTY 模式启用完整行编辑。通过管道或重定向输入时，Agent 自动回退到普通逐行读取，保证自动化
兼容，但这时没有光标和历史按键。无人值守调用建议直接使用：

```bash
sudo infernex-agent chat --ask '检查当前集群异常，只读取不要修改'
```

## 方案取舍

- kubectl-ai 的 Terminal UI 使用 `github.com/chzyer/readline` 提供行编辑和历史；InferNex
  Agent 复用这一成熟方案，并增加不落盘历史和 `/undo` 上下文撤回。
- OpenCode/Crush 一类工具使用 Bubble Tea 构建全屏 TUI，适合多面板、会话列表和流式布局；
  InferNex Agent 将在事件流、Session 和记忆存储稳定后增加 Bubble Tea 界面，现有 readline
  模式继续作为低能力终端和自动化场景的兼容入口。

- kubectl-ai Terminal UI：<https://github.com/GoogleCloudPlatform/kubectl-ai/blob/main/pkg/ui/terminal.go>
- chzyer/readline：<https://github.com/chzyer/readline>
