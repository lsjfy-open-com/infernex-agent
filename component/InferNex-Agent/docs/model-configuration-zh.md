# InferNex Agent 模型配置手册

普通宿主机用户只需执行：

```bash
sudo infernex-agent setup
```

按提示填写 OpenAI 兼容接口的真实 Base URL、真实 model ID 和可选 API Key。不要把
文档中的示例域名或模型名原样复制。`setup` 会测试 Chat Completions 和 tool calling，
失败时恢复原配置。后文命令用于非交互自动化、密钥轮换和高级维护。

## 1. 模型是否必需

不必需。InferNex Agent 的确定性采集、问题分类、MCP 工具、Dashboard 和
JSON 快照都不依赖模型。

配置模型后，只有存在问题的服务才会调用 OpenAI 兼容
`chat/completions` 接口。相同证据在同一进程生命周期内复用缓存，以减少
重复请求。后台扫描中的模型输出只是建议，不能触发部署、恢复或其他写操作。交互
终端中的模型可以提出受限 MCP 工具调用，但每个写工具仍必须由本机运维人员批准。

推荐上线顺序：

1. 无模型安装并完成只读集群验收；
2. 配置内网模型并执行小请求测试；
3. 观察调用量、延迟和诊断质量；
4. 再决定是否调整扫描周期和每轮最大分析数。

## 2. 接口要求

模型服务需要提供 OpenAI 兼容的非流式 Chat Completions：

```text
POST <base-url>/chat/completions
```

配置 `http://model.example:8000/v1` 时，Agent 请求
`http://model.example:8000/v1/chat/completions`。也可以直接配置完整的
`.../chat/completions` 地址。

要求：

- HTTP 或 HTTPS；
- base URL 不包含用户名、密码、query 或 fragment；
- 响应包含非空 `choices[0].message.content`；
- 支持 `temperature: 0` 和 `stream: false`；
- 如需认证，使用 Bearer API Key。

仅使用后台诊断时，普通 Chat Completions 文本响应即可。使用 XShell/SSH 自然语言
终端时，模型还必须支持 OpenAI `tools`、`tool_choice` 和
`message.tool_calls`（function/tool calling）。Agent 不使用模型厂商私有的 Agent
协议。

## 3. 宿主机安装时配置

```bash
sudo ./bin/install-host.sh \
  --kubeconfig /root/infernex-agent-host.kubeconfig \
  --scan-namespace model-a \
  --openai-base-url http://10.20.0.30:8000/v1 \
  --openai-model ops-diagnostic-model \
  --openai-api-key-file /root/infernex-openai.key \
  --openai-timeout 60s
```

如果端点不要求认证，省略 `--openai-api-key-file`。

不建议把密钥直接放在命令行或环境变量中。密钥源文件必须只包含一行文本。

## 4. 宿主机安装后配置

安装后工具位于：

```text
/opt/infernex-agent/bin/configure-model.sh
```

离线包解压目录中的 `./bin/configure-model.sh` 也可操作已经安装的服务。

### 查看配置

```bash
sudo /opt/infernex-agent/bin/configure-model.sh --show
```

输出只显示启用状态、URL、模型名、超时和“是否存在密钥”，不会显示密钥值。

### 首次配置并测试

```bash
sudo /opt/infernex-agent/bin/configure-model.sh \
  --base-url http://10.20.0.30:8000/v1 \
  --model ops-diagnostic-model \
  --api-key-file /root/infernex-openai.key \
  --timeout 60s \
  --test-tools \
  --show
```

`--test` 在写入和重启之前发送固定的短提示词，验证网络、鉴权、模型名和普通响应
结构。`--test-tools` 会进一步强制调用无副作用的 `infernex_test_tool`，验证交互终端
依赖的 OpenAI tool-calling 响应结构。两种操作都会产生少量 Token；计划使用自然
语言终端时应使用 `--test-tools`。

### 换模型或端点

未指定的字段沿用现值：

```bash
sudo /opt/infernex-agent/bin/configure-model.sh \
  --model ops-diagnostic-model-v2 \
  --test

sudo /opt/infernex-agent/bin/configure-model.sh \
  --base-url https://new-model.intra.example/v1 \
  --test
```

### 轮换或清除 API Key

```bash
sudo /opt/infernex-agent/bin/configure-model.sh \
  --api-key-file /root/infernex-openai-new.key \
  --test

sudo /opt/infernex-agent/bin/configure-model.sh \
  --clear-api-key \
  --test
```

### 禁用模型

```bash
sudo /opt/infernex-agent/bin/configure-model.sh --disable --show
```

禁用会删除模型参数和已安装的 API Key，但不停止 Agent。下一轮扫描继续使用
确定性诊断。

### 延迟重启

```bash
sudo /opt/infernex-agent/bin/configure-model.sh \
  --model ops-diagnostic-model-v2 \
  --no-restart

sudo systemctl restart infernex-agent
```

正常变更不建议使用 `--no-restart`。它主要用于维护窗口内合并多项操作。

### XShell/SSH 自然语言终端

配置和 `--test` 成功后：

```bash
sudo /opt/infernex-agent/bin/chat.sh
```

交互命令包括 `/help`、`/clear` 和 `/exit`。只读工具自动执行；写工具在本地显示
名称与 JSON 参数并要求精确输入 `yes`。自动化查询可使用：

```bash
sudo /opt/infernex-agent/bin/chat.sh \
  --ask '检查 models 中是否存在异常实例，并给出证据'
```

`--ask` 是无人值守模式，固定拒绝全部写工具。若模型不支持 tool calling，后台
诊断仍可工作，但交互查询无法取得实时集群证据，应更换模型或兼容网关。

## 5. 宿主机配置文件

```text
/etc/infernex-agent/agent.conf
  root:infernex-agent 0640
  保存 base URL、模型名、超时以及其他非敏感有效参数

/etc/infernex-agent/openai-api-key
  infernex-agent:infernex-agent 0600
  只保存 API Key
```

systemd unit 和启动命令只包含密钥文件路径，不包含密钥内容。建议只使用配置
工具变更模型参数，不直接编辑 `agent.conf`。

## 6. 集群内 Helm 配置

先创建已有 Secret：

```bash
kubectl --namespace infernex-system create secret generic infernex-agent-openai \
  --from-literal=api-key='replace-with-internal-provider-key'
```

安装或升级：

```bash
helm upgrade --install infernex-agent \
  ./infernex-agent-0.3.0.tgz \
  --namespace infernex-system \
  --set-string supervisor.analysis.openAI.baseURL=http://ops-model.ai-system.svc:8000/v1 \
  --set-string supervisor.analysis.openAI.model=ops-diagnostic-model \
  --set-string supervisor.analysis.openAI.existingSecret=infernex-agent-openai \
  --set-string supervisor.analysis.openAI.timeout=60s
```

Chart 不创建 API Key，只引用运维人员管理的 Secret。

## 7. 模型接收的数据

模型可接收：

- 服务名称、命名空间和 readiness；
- base template 名称；
- 组件摘要和工作负载副本摘要；
- 有界 Pod 状态；
- 有界 Event 元数据；
- 确定性问题列表。

模型不会接收：

- kubeconfig、ServiceAccount Token 或 API Key；
- Secret、环境变量或完整 Pod spec；
- Event note 原文；
- 节点名称；
- 模型 URI 的 userinfo、query 和 fragment；
- 通用 Kubernetes 访问能力。

## 8. 故障行为

| 故障 | 产品行为 |
| --- | --- |
| 模型网络不可达 | 本次分析记录错误，扫描继续 |
| 401/403 | 本次分析记录鉴权错误，扫描继续 |
| 模型名不存在 | 本次分析记录服务端错误，扫描继续 |
| 响应过大/格式错误 | 拒绝该响应，扫描继续 |
| 请求超时 | 取消请求，扫描继续 |
| 模型未配置 | 完全不发起模型请求 |
| 模型不支持 tool calling | 后台建议仍可用；自然语言终端无法调用实时 MCP 工具 |

模型不可用不会影响已有推理服务，也不会触发自动恢复。

## 9. 排查命令

```bash
sudo /opt/infernex-agent/bin/configure-model.sh --show
sudo /opt/infernex-agent/bin/configure-model.sh --test
sudo journalctl -u infernex-agent --since -30min --no-pager
sudo systemctl status infernex-agent --no-pager
```

如测试成功但 Dashboard 没有模型内容，先确认当前服务是否存在确定性问题。
健康服务不会调用模型。
