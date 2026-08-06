# InferNex Agent 变更保护、备份与回退

## 1. 产品保证

只要启用了 Agent 写能力，以下两项是强制约束，不是可选建议：

1. 宿主机安装或升级在覆盖任何 Agent 二进制、配置、凭据和 systemd unit
   之前，必须生成安装前恢复点；
2. 每个基于稳定来源的新部署必须先持久化变更前状态。新服务在规定时间内不能
   Ready，或 InferNex 控制面报告当前 generation 已进入 `Degraded`，Agent
   必须自动撤销本次创建。
3. 每个渐进实验阶段必须先持久化计划和变更事件。候选超时、丢失 Ready、
   Degraded 或相对稳定基线新增临界诊断类别时，只回退当前候选，稳定基线不变。

这里的“集群状态”指 Agent 管理边界内的源事实：
目标命名空间中的 `InferNexService`。Deployment、DaemonSet、Pod 和 Service
是 InferNex Bridge 从源 CR 派生的对象，回退源 CR 后由 Bridge 负责收敛，
Agent 不直接备份或覆盖这些派生对象。

这不是 etcd 灾备，也不会回滚其他控制器或运维人员在同一时间进行的无关变更。

## 2. 宿主机安装前恢复点

`install-host.sh` 完成 Kubernetes 连通性和权限检查后、覆盖本机文件前执行：

1. 使用待安装的新二进制扫描全部 `--scan-namespace`；
2. 保存每个 `InferNexService` 的完整 API 对象、UID、resourceVersion 和采集时间；
3. 对快照内容计算 SHA-256；
4. 保存原有二进制、`.previous`、启动器、`agent.conf`、kubeconfig、可选
   API Key、配置工具和 systemd unit；
5. 记录安装前服务的 active/enabled 状态。

恢复点位于：

```text
/var/lib/infernex-agent/backups/install-<UTC时间>-<PID>/
├── cluster-state.json
├── cluster-state.sha256
└── host/
    ├── manifest
    ├── checksums.sha256
    └── <编号备份文件>
```

目录为 root 专用 `0700`，文件为 `0600`。其中可能包含 kubeconfig 和模型
API Key，应按凭据备份处理，不得上传到代码仓、工单或普通日志系统。

如果安装、systemd 启动或安装后验证失败，退出 trap 会自动：

- 停止失败的新服务；
- 原样恢复安装前存在的本机文件，删除本次新产生但安装前不存在的对应文件；
- 恢复 systemd 的 enabled/active 状态；
- 使用安装前集群快照撤销带有 Agent `change-id` 的新增服务；
- 保留恢复点，供事后审计和人工复核。

安装成功不会删除恢复点。

## 3. 手工核验和恢复

核验快照格式与内容校验和：

```bash
sudo /opt/infernex-agent/bin/infernex-agent cluster-state verify \
  --input /var/lib/infernex-agent/backups/install-*/cluster-state.json
```

显式恢复：

```bash
sudo /opt/infernex-agent/bin/infernex-agent cluster-state restore \
  --kubeconfig /etc/infernex-agent/kubeconfig \
  --input /var/lib/infernex-agent/backups/install-20260731T010203Z-1234/cluster-state.json \
  --confirm
```

如需同时恢复宿主机文件、凭据和 systemd 状态，使用恢复点中经过校验的清单：

```bash
sudo /opt/infernex-agent/bin/restore-host-install.sh \
  --backup-dir /var/lib/infernex-agent/backups/install-20260731T010203Z-1234 \
  --confirm
```

该命令会先恢复 Agent 管理的集群源资源，再恢复宿主机文件。卸载器默认保留
`/var/lib/infernex-agent` 中的变更记录和恢复点；只有显式传入
`--purge-state` 才会删除。

恢复操作遵守防误伤规则：

- 只删除快照后新增、同时带有 `app.kubernetes.io/managed-by=infernex-agent`
  和 `agent.infernex.io/change-id` 的服务；
- 只重建快照中原本由 Agent 管理、当前已经缺失的服务；
- 不删除无 Agent 所有权或无 `change-id` 的对象；
- 不盲目覆盖快照后被其他主体修改过的现有 spec，而是在结果中标记
  `skipped`，交给运维人员处理冲突。

## 4. 新部署失败自动回退

`infernex_deploy_model` 的执行顺序固定为：

```text
保存 planned 记录
  → 创建带 change-id 的 InferNexService
  → 保存 applied 记录
  → 后台观察 status
      ├─ Ready 且 observedGeneration >= generation → committed
      └─ 超时/当前 generation Degraded
           → 校验所有权和 change-id
           → 删除本次新建资源
           → 确认资源不存在
           → rolled-back
```

默认 Ready 窗口为 `10m`。宿主机安装可调整：

```bash
sudo ./bin/install-host.sh \
  --kubeconfig /root/infernex-agent-host.kubeconfig \
  --scan-namespace models \
  --enable-deployment \
  --deployment-readiness-timeout 15m
```

部署调用立即返回 `changeId` 和 `changeStatus=applied`。通过只读工具查询：

```json
{
  "name": "infernex_get_change",
  "arguments": {
    "changeId": "32位十六进制ID"
  }
}
```

终态为：

- `committed`：服务已通过 InferNex Ready 判定；
- `rolled-back`：失败部署已经恢复到创建前的“不存在”状态；
- `rollback-failed`：对象所有权改变、API 不可用或删除确认超时，需要告警和人工处理；
- `apply-failed`：创建没有完成，集群保持创建前状态。

变更事件以追加写方式保存在
`/var/lib/infernex-agent/changes/<change-id>/`。Agent 在创建后崩溃、重启，
仍会从 `planned` 或 `applied` 记录恢复监控；不会因为进程重启放弃回退。

受控自动恢复服务的创建同样先写 `planned`，创建成功后写 `committed`，对象也
携带 `change-id`。因此安装前快照恢复能够识别并撤销快照之后由自动恢复路径
新增的服务；该 ID 同时出现在 Dashboard/JSON 的 remediation 信息中。

渐进实验为每个阶段分配独立 `changeId`，并把计划追加写入
`/var/lib/infernex-agent/experiments/`。候选带实验 ID、阶段、基线、profile 和
`changeId`；回退前全部所有权必须匹配。通过的候选写入 `committed` 并成为下一
阶段基线，失败阶段写入 `rolled-back` 或 `rollback-failed`。Agent 重启后恢复
状态为 planned/running 的计划，不重新创建已经存在且 spec 完全相同的候选。

## 5. Kubernetes/Helm 模式

Helm 安装和升级使用 `--atomic --wait`，Agent 自身启动失败时由 Helm 恢复
上一 release 或清理失败的首次安装。

启用部署或实验写能力时，生产环境必须给变更记录使用持久卷：

```bash
./bin/install-agent.sh \
  --target-namespace models \
  --dashboard-cidr 10.20.0.0/16 \
  --enable-deployment \
  --deployment-readiness-timeout 10m \
  --state-storage-class local-path
```

也可以指定组织已经创建的 PVC：

```bash
--state-existing-claim infernex-agent-state
```

离线安装器启用部署能力时会强制
`changeSafety.persistence.enabled=true`。直接使用 Helm 时必须显式设置：

```yaml
tools:
  deployment:
    enabled: true
changeSafety:
  persistence:
    enabled: true
    existingClaim: infernex-agent-state
```

Kind 测试可以使用默认 `emptyDir`；它能承受容器重启，但 Pod 被替换后记录会
丢失，因此不属于生产级持久回退配置。

## 6. 明确边界

当前已保证：

- 安装前恢复点；
- 安装失败自动恢复 Agent 本机文件和 Agent 变更过的集群源资源；
- Agent 新建服务失败自动回到创建前状态；
- Agent 重启后继续未完成变更；
- 明确的 `change-id`、追加写记录和只读状态查询。
- 渐进实验失败时保留原始基线和已通过阶段，只删除当前匹配所有权的候选。

当前不承诺：

- etcd、Secret、PVC 数据或整个 Kubernetes 集群的灾备；
- 自动覆盖运维人员在快照后修改的 spec；
- 自动恢复一次明确确认的删除操作；删除前对象已经进入变更记录，但恢复删除
  仍需后续的独立审批接口；
- 推理流量切换或业务数据回滚；
- 用大模型输出判断是否回退。回退判定只使用确定性的 generation、Ready、
  Degraded 和超时规则。

## 7. 上线验收

- [ ] 安装输出包含 `pre-install recovery point`；
- [ ] `cluster-state verify` 返回 `valid: true`；
- [ ] 恢复点目录和文件权限分别为 `0700`、`0600`；
- [ ] 启用写能力的 Kubernetes 安装使用 PVC，而不是 `emptyDir`；
- [ ] 部署结果返回 `changeId`；
- [ ] 正常部署最终为 `committed`；
- [ ] 故障注入部署最终为 `rolled-back`，目标 `InferNexService` 不存在；
- [ ] 重启 Agent 后，未完成的 `applied` 变更仍继续监控；
- [ ] `rollback-failed` 已接入宿主机日志采集和告警。
