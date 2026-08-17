# 故障诊断 Subagent 开发与联调指南

本文面向独立开发 vLLM-Ascend、CANN 和 NPU 故障诊断 Subagent 的团队。双方以 MCP 和 Evidence
契约集成，不依赖 InferNex Agent 内部 Go package，也不向 Subagent 交付 kubeconfig。

## 1. 连接

诊断模式安装后，默认端点与凭据为：

```text
MCP URL: http://127.0.0.1:18082/mcp
Token:   /etc/infernex-agent/diagnostic-subagent-token
Header:  Authorization: Bearer <token>
```

端点默认仅 loopback。建议 Subagent 与 InferNex Agent 运行在同一管理节点；跨主机接入必须额外使用
SSH tunnel、mTLS gateway 或内网认证代理，不能直接把 token-only HTTP 暴露到非受信网络。

安装时可关闭：

```bash
sudo ./install.sh --disable-diagnostic-subagent
```

直接调用 `install-host.sh` 时可配置 listener 和并发：

```bash
sudo ./bin/install-host.sh \
  --kubeconfig /path/to/kubeconfig \
  --scan-namespace models \
  --execution-mode diagnose \
  --diagnostic-subagent-listen-address 127.0.0.1:18082 \
  --diagnostic-subagent-max-concurrency 4
```

## 2. 首次握手

连接 MCP 后第一步调用：

```text
infernex_get_diagnostic_delegate_contract
```

返回 `DiagnosticDelegateContract`，包括：

- `apiVersion`、`role`；
- `namespaceScope`；
- 当前可用 `executionChannels`；
- `actionClasses`；
- Evidence 和报告格式；
- 默认采集策略、时长和容量上限；
- 明确排除的能力。

Subagent 必须按实时 contract 降级，不能假定 `host-root`、SSH、Bridge 或某个 Skill 一定存在。

## 3. 推荐诊断流程

1. `openfuyao_detect_environment`：识别当前 kubeconfig 所在控制面。
2. `k8s_cluster_overview`：获取 Node/NPU/Pod 健康摘要。
3. `k8s_list_workloads(namespace, selector)`：确定 owner、Pod、container、Node 和镜像。
4. `k8s_get_events` 与 `k8s_get_pod_logs`：先取小窗口 current/previous 日志。
5. 若检测到 Bridge，再使用 `infernex_inspect_service`、`infernex_get_topology` 和
   `infernex_diagnose_service`。
6. `infernex_list_skills` 后按症状渐进读取 CANN/HiXL/vLLM-Ascend Skill。
7. 只有初始证据不足时才调用固定探针、短时 PlogCapture 或 CollectorRun。
8. 使用 Evidence glob/grep/分页工具分析落盘文件，避免把整份日志塞入模型上下文。
9. 通过 `infernex_create_markdown_report` 保存结论，向主 Agent 返回 report path/hash。

## 4. 可用工具类别

| 类别 | 主要工具 | 说明 |
| --- | --- | --- |
| 能力协商 | `infernex_get_diagnostic_delegate_contract` | 每个任务首先调用 |
| 环境与拓扑 | `openfuyao_detect_environment`、`k8s_cluster_overview`、`k8s_list_workloads` | namespace 严格受限 |
| 日志和事件 | `k8s_get_pod_logs`、`k8s_get_events`、Bridge 诊断工具 | 支持 current/previous、有界脱敏 |
| 主动读取 | `infernex_list_diagnostic_probes`、`infernex_run_diagnostic_probe` | 无任意 shell |
| 短时留证 | PlogCapture、CollectorRun start/list/get/stop | 默认 15 分钟/256 MiB |
| 历史证据 | evidence roots/find/grep/read | 相对路径、分页、SHA-256 |
| 领域知识 | Skill list/read/reference | Skill 不增加权限 |
| 输出 | report create/list/read | Markdown 报告引用证据 |

工具实际列表以 MCP `tools/list` 为准。受限端点不会发布 `k8s_read_resources`、部署、变更、恢复、实验
或 semantic-memory 写工具。

## 5. 报告契约 markdown-v1

报告至少包含：

```markdown
# Incident title

## Scope and deployment context
- namespace / workload / Pod UID / container / Node
- model, image digest, vLLM-Ascend/CANN/driver tuple
- stable configuration version and the single feature delta

## Timeline
- UTC timestamp, target, observation, evidence reference

## Findings
- observed fact
- ranked hypothesis
- supporting and falsifying evidence

## Recommended next action
- smallest reversible validation
- expected result and rollback condition

## Evidence
- rootId + relative path + SHA-256 + line/time range
```

不得把推测写成事实；不得用“增加 timeout”替代连通性、rank 对称性和最早错误检查；不得建议
Subagent 自己执行修改。主 Agent 根据报告生成部署实验或回退计划。

## 6. 错误和重试

- HTTP `401`：token 缺失/错误，不重试，重新建立委派凭据。
- HTTP `429`：并发预算耗尽，读取 `Retry-After` 后有界重试。
- MCP namespace scope error：任务委派范围错误，返回主 Agent 扩大 scope，不自行换 namespace。
- evidence budget/duration error：缩小 selector、时间窗或容量；持续采集需要单独运维策略。
- Pod replaced/not found：重新 list workload，以新 Pod UID 建立新 segment，不把新旧容器混为一个进程。
- truncated：使用 Evidence 分页继续，不重复请求整份日志。

## 7. 双方联调清单

Subagent 提交版本时至少验证：

- 无 token、错误 token、正确 token；
- contract 和 `tools/list` capability negotiation；
- namespace 内成功、namespace 外拒绝；
- 多 Node、多 Pod、多 container 日志关联；
- previous logs、Pod replacement 和 UID 分段；
- 15 分钟默认 burst、60 分钟/2 GiB 硬上限；
- 429 退避、工具超时、截断续读和 Agent 重启后的任务查询；
- Bridge 存在/不存在、host-root 存在/不存在的降级；
- 报告证据 hash 可回读；
- Subagent 故障不会影响部署 Agent、systemd 服务或推理实例。

