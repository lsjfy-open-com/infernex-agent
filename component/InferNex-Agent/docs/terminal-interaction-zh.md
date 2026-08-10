# InferNex Agent Linux 终端交互

`sudo infernex-agent chat` 使用真正的 readline 行编辑器，而不是简单地从标准输入逐行读取。
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
| `/compact` | 立即压缩可压缩的旧轮次 |
| `/undo` | 删除最后一个尚可定位的用户轮次及其响应 |
| `/clear` | 清空整个当前会话 |
| `/exit` | 退出终端 |

历史记录默认只保存在当前进程内，不写入磁盘。退出 `chat` 后历史消失，避免把业务名称、
故障条件和运维意图默认保存到管理节点。后续如果增加跨会话历史，会采用显式开启、权限隔离
和可清理策略。

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
  当前 InferNex Agent 仍以 SSH 运维终端和低依赖静态二进制为第一优先，因此暂不引入完整 TUI。

- kubectl-ai Terminal UI：<https://github.com/GoogleCloudPlatform/kubectl-ai/blob/main/pkg/ui/terminal.go>
- chzyer/readline：<https://github.com/chzyer/readline>
