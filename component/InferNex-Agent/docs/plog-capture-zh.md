# CANN plog 外部持续采集

## 解决什么问题

CANN plog 常在推理容器内部。实例崩溃、Pod 重建或节点驱逐后，旧容器中的现场可能无法再次采集。
InferNex Agent 在 `diagnose` 及以上模式提供 `PlogCapture`：通过只读 Pod exec 增量复制 plog 到管理
节点的 Evidence Store，并按 Pod UID 分段保留。

它不是 sidecar，也不会 patch Deployment/LWS/Pod，不向容器写文件，不重启服务。采集任务只修改
Agent 自己的状态和证据目录，所以归类为需要批准的 `diagnose-local-write`，不是业务 `modify`。

## 用户怎样使用

一键安装默认启用 `diagnose`。在 TUI 或 classic chat 中直接说明目标，例如：

```text
请对 models 命名空间中 app=qwen-pd,role=prefill 的 vllm 容器持续采集 CANN plog，
采集 2 小时，最多保存 4GiB；先把目标和预算展示给我确认。
```

Agent 应先用 Kubernetes 工具确认 selector 对应的 Pod 和容器，再展示 namespace、selector、container、
持续时间与最大字节。用户批准后才调用 `infernex_start_plog_capture`。后续可自然语言查询进度或停止；
停止不会删除已经保存的证据。

默认预算为 60 分钟、1GiB；允许范围为 1–10080 分钟、1MiB–100GiB。相同 namespace、selector 和
container 同时只允许一个运行任务，避免重复采集。

## 数据位置和生命周期

```text
/var/lib/infernex-agent/
├── plog-captures/<task-id>.json
└── imports/plog/<task-id>/<pod-uid>/<container>/<source-path-hash>.plog
```

任务状态包含 deadline、最大/已采集字节、segment 数和最近错误。Pod UID 改变后创建新 segment，旧
segment 不覆盖；进程重启后，deadline 尚未到期的 `running` 任务从持久状态恢复。达到时间上限时变为
`completed`，达到容量上限时变为 `capacity-reached`。

`imports` 始终作为 Agent 自有 Evidence Root 注册。模型应先 grep，再有界读取需要的行；读取工具会
做常见凭据脱敏、噪声过滤并计算文件 SHA-256。原始文件保持 `0600`，不得直接发送给外部模型。

## 当前固定容器路径和依赖

第一版只发现以下固定根目录下的常规文件：

- `/root/ascend/log/plog`；
- `/home/HwHiAiUser/ascend/log/plog`；
- `/var/log/npu/slog`。

容器需要提供 `find`、`stat` 和 `dd`。读取以最大 256KiB 的 chunk 递增进行，每轮最多处理 20 个
Running Pod/container target 和合计 200 个文件。当前 kubeconfig 还必须在目标 namespace
拥有 `get/list pods` 和 `create pods/exec`。

若某个 vLLM-Ascend/CANN 镜像使用不同 plog 根目录，应先记录为兼容性缺口，而不是让模型传入任意
路径。后续由经过审阅的 image/version capability profile 扩充固定 root。若已有 hostPath、Loki 或
企业日志平台，应优先开发只读适配器，避免重复搬运。

## 安全和剩余边界

- label selector 必须非空且符合 Kubernetes 语法；
- exec 命令和 root 在 Core 中固定，MCP 不接受 shell 或路径参数；
- 任务必须有用户批准、deadline 和容量上限；
- stop 不删除证据，清理/保留期策略尚未实现；
- segment 最终 hash/report、Loki/hostPath adapter 和 A2 现场镜像矩阵仍在路线图；
- 采用 DaemonSet/sidecar 的可选采集形态属于 `install/modify`，必须有配置版本和回退，不能冒充当前
  无侵入实现。
