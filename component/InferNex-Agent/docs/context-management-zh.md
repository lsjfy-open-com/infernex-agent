# InferNex Agent 上下文管理

InferNex Agent 不会把整个对话和所有日志无限追加后直接发给模型。交互终端为每个会话维护
明确的 token 预算，在每次模型调用前执行预算、压缩和硬上限检查。

## 默认策略

默认值适合上下文窗口为 32K 的内网模型：

| 配置 | 默认值 | 作用 |
| --- | ---: | --- |
| `context-window-tokens` | 32768 | 模型输入与输出合计的硬上限 |
| `max-output-tokens` | 2048 | 每次调用预留且通过 `max_tokens` 请求的最大输出；小窗口按窗口的 1/8 派生 |
| `context-compaction-threshold` | 80 | 预计总量达到窗口的 80% 时开始压缩 |
| `context-keep-recent-turns` | 4 | 压缩时原样保留最近 4 个用户轮次及其工具链 |
| `tool-result-max-tokens` | 4096 | 单个工具结果的近似上限；小窗口按窗口的 15% 派生 |

处理顺序如下：

1. 工具结果进入历史前先做单结果限额，保留开头、结尾和“已截断”标记；
2. 每次调用模型前估算 system prompt、工具定义、消息和输出预留量；
3. 达到阈值后，由已配置模型把较早轮次压缩为结构化工作记忆，保留事实、证据、资源名、
   判断与推测的区别、审批结果、回退状态和未完成事项；
4. 压缩调用不可用时使用确定性摘要兜底，再按需移除旧工具结果正文；
5. 仍超过硬上限时不向模型发送请求，而是提示使用 `/compact`、`/clear`、缩短问题或增大窗口。

这里使用偏保守的本地估算器，不依赖特定厂商 tokenizer：ASCII 文本约每 4 个字符一个
token，中文等非 ASCII 字符按每字符一个 token 计算。模型服务报告的 token 数可能略有不同，
所以不应把压缩阈值设到 100%。

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
  --max-output-tokens 2048 \
  --context-compaction-threshold 75 \
  --context-keep-recent-turns 6 \
  --tool-result-max-tokens 3072 \
  --show
```

配置工具会验证各值、原子写入 `/etc/infernex-agent/agent.conf` 并重启服务；启动失败时恢复
原配置。也可以只对一次终端会话使用同名 `infernex-agent chat` 参数覆盖配置文件。

## 会话中观察与控制

在 `sudo infernex-agent chat` 中：

- `/context`：查看预计输入、输出预留、硬窗口、压缩阈值、消息数、已压缩次数和已裁剪工具结果数；
- `/compact`：立即压缩可压缩的旧轮次；
- `/clear`：清空当前会话，重新从系统指令开始；
- 自动压缩发生时终端输出 `[context] ...`，便于运维人员观察工作流。

当前会话记忆只存在于正在运行的 `chat` 进程内；退出终端或重启进程后不会恢复。这避免默认
把集群证据和日志长期落盘。跨会话加密持久化、按事件时间线检索和服务级长期记忆属于后续能力，
不应与本版本的窗口安全机制混淆。

## 设计来源与取舍

本实现采用运维 Agent 常见的“两级限制”：单工具输出先限额，完整历史在模型调用前按预算压缩。
该思路与 HolmesGPT 的上下文管理相近；会话内 `/clear`、显式会话控制也参考了 kubectl-ai 的
交互模型。不同之处是 InferNex Agent 默认不把截断内容写到宿主机临时文件，因为当前模型没有
任意文件读取工具，写出一个模型无法重新访问的文件只会增加敏感数据落盘面。

- HolmesGPT Context Management：<https://holmesgpt.dev/dev/reference/context-management/>
- kubectl-ai：<https://github.com/GoogleCloudPlatform/kubectl-ai>
