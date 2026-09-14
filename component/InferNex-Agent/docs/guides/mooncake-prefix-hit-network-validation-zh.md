# Mooncake prefix hit 超时网络验证

目标是判断 prefix hit 超时是否与交换机或 RoCE 链路拥塞处在同一时间窗口，并保留足够证据区分 Mooncake、Prefill/Decode、主机网络和交换机。PFC 历史累计值非零不能单独证明本次超时由交换机导致。

## 现场准备

在所有节点确认 UTC/NTP 时间一致，记录测试 namespace、Prefill/Decode Pod selector、容器名、节点、NPU device、业务网卡、交换机端口和一次可识别的请求 ID。SSH 使用已经配置且启用严格 host key 校验的别名；本机 root 权限不代表远端 root 或 Kubernetes RBAC。

启动 TUI 后可进入持续诊断模式：

```text
/mode_change root full
```

给 Agent 的任务应包含上述明确目标，并要求围绕一次受控复现保留报告。例如：“采集 models 命名空间中 selector 为 app=moon-pd 的 P/D Pod 与所在节点证据，复现一次 prefix hit 超时，关联 PFC/RDMA、Pod 日志和交换机端口计数，生成报告；不要修改业务配置或发起压测。”

## 同窗证据

从复现前 30–60 秒开始，覆盖超时后至少 60 秒；若业务超时阈值更长，应覆盖完整阈值并留出前后窗口。

1. 在 Prefill 与 Decode 两侧采集带 UTC 时间戳的 Mooncake/vLLM 日志、请求 ID、prefix hit 判定、KV 传输开始/结束/超时和 Pod UID。发生重启时同时保留 previous logs 与 plog。
2. 对两侧节点和所有相关 NPU device 至少采集三次 `hccn_tool -stat -g`，建议 5–10 秒间隔；同时采集 RDMA statistic、业务网卡 `ethtool -S`、链路状态和路由。判断增量及增长时间，不能只看累计总数。
3. 从交换机导出相同 UTC 窗口、对应物理端口和优先级的 PFC pause Rx/Tx、buffer/queue、drop、ECN、端口 flap/error。标明计数器清零、设备重启或采样间隔。
4. 保留 Kubernetes 事件、Pod 到节点/NPU/网卡/交换机端口的映射，以及同一窗口内健康请求的对照证据。

## 判定

- 只有当超时窗口内相关端口与两端主机计数出现一致的新增 PFC、丢包或队列拥塞，并可在健康窗口消失或显著降低时，才把交换机拥塞列为强候选。
- 若交换机和主机增量平稳，先检查 prefix 元数据、KV 对象可见性、Mooncake 传输状态、P/D 配置一致性、Pod 重启与超时参数。
- 若只有单侧增长，继续核对端口映射、流向和优先级，避免把历史累计量或其他业务流量归因到本次请求。
- 禁用缓存、调整路由、运行 HCCL/iperf 或重启服务属于会影响集群或产生负载的对照实验，必须单独由人工批准。

最终报告应以 `mooncake-prefix-hit-超时_<UTC日期时间>.md` 这类可读名称保存，列出请求时间线、P/D 与交换机计数增量、证据 hash、已排除项和仍待验证项。
