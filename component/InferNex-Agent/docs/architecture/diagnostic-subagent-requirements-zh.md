# 故障诊断 Subagent 接入需求

状态：v0.5 纵向切片；用于双方解耦开发和验收。

## 1. 产品定位

InferNex Agent 的主职责始终是把新模型部署为稳定、高吞吐、低 TTFT 的推理服务。故障诊断
Subagent 是部署闭环中的专家角色，重点处理 vLLM-Ascend、CANN、HCCL/HiXL、Mooncake、PD 分离和
NPU 硬件问题，但不拥有部署控制面。

典型闭环为：

```text
部署/新增一个加速特性 → readiness/warmup/eval/soak
→ 失败或性能回归 → 主 Agent 创建诊断委派
→ 诊断 Subagent 获取受控证据并输出假设、证据和建议
→ 主 Agent 决定继续实验、回退或请求人工批准
```

## 2. 功能需求

1. 自动发现授权 namespace 内的 Node、workload、Pod、container、owner、镜像和重启状态。
2. 通过 Kubernetes API 跨节点读取 current/previous container stdout，不要求 Subagent 登录节点。
3. 在 `diagnose` 及以上模式，通过固定 profile 执行 Pod exec、安装节点 root helper 和运维预授权 SSH。
4. 按 namespace + label selector 跨实例启动有界 plog 或 PFC/HCCN/NPU/CANN/HCCL CollectorRun。
5. 原始日志先进入本地 Evidence Store；Subagent 使用 glob、grep、分页和 SHA-256 引用渐进分析。
6. Subagent 可读取 CANN/HiXL 及以后新增的 vLLM-Ascend/NPU Skill，可创建引用证据的 Markdown 报告。
7. 主 Agent 能把部署 change/configuration version、特性 delta、验证阶段和失败时间窗传入诊断任务。
8. 所有委派调用具有独立身份、namespace scope、时间/容量/并发预算和审计关联 ID。

## 3. 采集时机

默认不是长期全量采集，而是 `event-triggered-burst`：

- 新部署、warmup、serving-path、EvalScope 或 soak 失败；
- 未处于批准升级/change window 时，Pod restart、unexpected deletion、replacement 或节点异常；
- TPS、TTFT、错误率或跨 rank 行为相对稳定基线发生显著回归；
- 操作者明确要求一次诊断。

默认委派采集窗口 15 分钟、单任务 256 MiB；受限端点硬上限 60 分钟和 2 GiB。持续采集仅在集群
确有需要时由操作者显式启用，不作为安装后的默认行为。

Pod 已彻底删除后，未外置保存的容器内 plog 无法事后恢复。因此后续事件触发器必须在异常、
Terminating 或 replacement 新 Pod 启动阶段尽早留证；若平台已有 hostPath、Loki 或日志平台，优先
登记现有证据，避免重复常驻采集。

## 4. 权限需求

诊断 Subagent 只能访问独立 `/mcp` 端点授予的能力：

- `observe`：环境、Node 摘要、workload、Event、Pod logs、拓扑；
- `active-read`：编译进 Core 的固定探针；
- `evidence-write`：短时 plog/CollectorRun 和 Agent 自有报告文件。

明确排除部署、配置修改、恢复、实验启动、语义记忆写入、任意 shell、任意 Kubernetes resource 读取、
kubeconfig、Secret payload 和宿主机任意路径。诊断建议返回主 Agent 后，任何变更仍由主 Agent 的
Policy、Snapshot、Approval、Verification 和 Rollback 闭环执行。

## 5. 验收标准

- 无 token 返回 HTTP 401；超过并发预算返回 429。
- Subagent 工具列表不出现 deploy/change/recover/experiment/memory-write 或 `k8s_read_resources`。
- 越过委派 namespace 的日志、Event、workload、Pod exec 和采集请求被 Core 拒绝。
- 同一 selector 能覆盖分布在不同 Node 的多个 Pod/container，并按 Pod UID 保存证据。
- 默认采集不会变成长时间后台任务；超出 60 分钟或 2 GiB 的委派请求被拒绝。
- 报告可以追溯到 evidence root、相对路径、SHA-256、目标 UID 和采集时间。
- Subagent 不可用时不影响已有部署和推理服务，主 Agent仍可回退或由人工接管。

