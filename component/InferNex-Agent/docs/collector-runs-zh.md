# 节点与容器持续诊断 CollectorRun

`CollectorRun` 用于把 PFC、HCCN、NPU、CANN 和 HCCL 前置检查从“人工逐节点执行并搬运日志”改为
Agent 管理的可恢复任务。它在 `diagnose` 及以上模式可用，按 namespace 和 label selector 自动重新
发现 Running Pod；Pod 重建后使用新的 Pod UID 记录后续样本，旧样本仍保留在管理节点。

## 当前内置 Profile

| Profile | 行为 | 风险等级 |
| --- | --- | --- |
| `hccn-pfc-stats` | 对指定 device 执行固定 `hccn_tool -i ID -stat -g` | active-read |
| `hccn-device` | 读取指定 device 的 HCCN link | active-read |
| `npu-inventory` | 读取 `npu-smi info` | active-read |
| `cann-version` | 读取固定 CANN/driver 版本文件 | passive/active-read |
| `hccl-root-info` | 读取固定 `/etc/hccl_rootInfo.json` | active-read |
| `hccl-test-layout` | 检查固定 HCCL Test 目录，不运行测试 | active-read |

Profile 不接受 shell、脚本正文、路径、镜像、环境变量或额外命令参数。HCCL Test 本体不在当前列表：
它会占用设备并产生网络流量，后续作为 `benchmark` action class 实现。

## 自然语言示例

```text
对 models 命名空间 app=qwen-pd 的 vllm 容器，在 device 0-7 上每 60 秒采集一次 PFC 计数，
持续 120 分钟，最多保存 2 GiB。先显示目标、频率和预算，等我批准后启动。
```

模型应先发现工作负载和容器，再调用：

1. `infernex_start_collector_run`；
2. `infernex_list_collector_runs` / `infernex_get_collector_run`；
3. 通过 Evidence 工具 grep/分页读取样本；
4. `infernex_stop_collector_run` 提前停止。

启动和停止需要本机确认。任务状态保存在：

```text
/var/lib/infernex-agent/collector-runs/<run-id>.json
```

原始 JSONL 样本保存在：

```text
/var/lib/infernex-agent/imports/collectors/<run-id>/samples.jsonl
```

每条样本包含采集时间、namespace、Pod、Pod UID、container、device、实际固定命令、退出码、stdout、
stderr、截断标记和错误。`imports` 是 Agent 自有 Evidence Root，因此不需要人再次复制文件。

## 权限与容器用户

安装器由 root 运行，但主 Agent 服务默认是 `User=infernex-agent`。Pod exec 的准入由 kubeconfig RBAC
决定，而不是宿主机 UID；exec 后命令使用容器已有用户，Kubernetes exec 不提供任意切换 root 用户的
能力。需要：

```bash
grep '^--execution-mode=' /etc/infernex-agent/agent.conf
kubectl --kubeconfig /etc/infernex-agent/kubeconfig \
  auth can-i create pods/exec -n <namespace>
```

若容器没有 `hccn_tool` 或未映射驱动设备，当前样本会记录失败原因，不会要求用户手工搬运输出。
下一通道是 root-only Collector Helper 和经批准的短命节点 DaemonSet/Job：模型服务仍保持非 root，
只有固定 profile 在隔离执行面获得所需权限。

## 预算和边界

- interval：10–3600 秒；
- duration：1 分钟–7 天；
- evidence：1 MiB–100 GiB；
- 每轮最多 20 个 Pod/container target；
- device ID：0–63；
- Agent 重启后恢复仍在 deadline 内的任务；
- 停止任务不删除 Evidence；
- 不 patch 业务工作负载、不注入 sidecar、不在容器内写文件。

