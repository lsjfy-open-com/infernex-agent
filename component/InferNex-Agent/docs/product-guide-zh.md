# InferNex Agent 产品使用说明

## 1. 产品定位

InferNex Agent 是 InferNex 推理集群的管理面 Agent。它面向平台运维、
推理服务管理员和 SRE，持续收集 `InferNexService` 状态、受管工作负载、
Pod 和 Kubernetes Event，将证据归一化为可查询工具、只读 Dashboard 和
诊断快照。

Agent 不在推理请求数据路径中，不替代 InferNex Bridge、PD Orchestrator、
Hermes、Eagle-Eye、NPU 驱动或模型服务。所有服务编排仍由现有 InferNex
API 和控制器完成。

当前产品版本提供两种部署形态：

| 形态 | 推荐场景 | 运行方式 | 集群身份 |
| --- | --- | --- | --- |
| 宿主机模式 | 固定 master、引导节点或管理机 | 非 root systemd 服务 | 专用 kubeconfig |
| 集群内模式 | Kubernetes 原生运维 | Helm Deployment | 投射的 ServiceAccount Token |

对于 openEuler A2 管理节点，推荐宿主机模式。A2 是加速设备/服务器产品，
安装包由主机 CPU 架构决定：

| `uname -m` | 使用产物 |
| --- | --- |
| `aarch64` | `infernex-agent-host-offline-*-linux-arm64.tar.gz` |
| `x86_64` | `infernex-agent-host-offline-*-linux-amd64.tar.gz` |

## 2. 核心功能

| 功能 | 默认状态 | 说明 |
| --- | --- | --- |
| 多命名空间持续扫描 | 开启 | 只扫描显式允许的命名空间 |
| 确定性问题分类 | 开启 | 不依赖大模型 |
| MCP 只读工具 | 开启 | 查询服务、拓扑和事件 |
| Web Dashboard / JSON 快照 | 开启 | 展示归一化运维证据 |
| OpenAI 兼容模型分析 | 关闭 | 可选的诊断建议，不参与控制决策 |
| 固定目录部署工具 | 关闭 | 仅创建/删除受 Agent 所有权保护的 `InferNexService` |
| 安装前备份和部署失败回退 | 写能力启用时强制 | 持久变更记录、超时/Degraded 自动撤销本次新建 |
| 受控自动恢复 | 关闭 | 多重显式授权后创建新的恢复服务 |

模型不是产品启动条件。未配置模型时，扫描、规则诊断、MCP、Dashboard 和
JSON API 均正常工作。模型只在服务存在问题时接收受限证据并产生建议。

MCP 工具契约：

| 工具 | 类型 | 功能 |
| --- | --- | --- |
| `infernex_list_services` | 只读 | 列出命名空间中的服务和 readiness 摘要 |
| `infernex_inspect_service` | 只读 | 查看一个服务的权威状态和组件摘要 |
| `infernex_get_topology` | 只读 | 查看受管工作负载、Pod 和副本状态 |
| `infernex_get_events` | 只读 | 查看与服务及其受管对象相关的近期 Event |
| `infernex_deploy_model` | 可选写 | 从编译内置目录创建受控 `InferNexService` |
| `infernex_delete_model` | 可选写 | 删除带匹配 Agent 所有权的目录服务 |
| `infernex_get_change` | 可选只读 | 查询部署提交、失败和自动回退状态 |

自动恢复属于持续 supervisor 能力，不向 MCP 暴露通用恢复写工具。

## 3. 上线前需要确定的参数

安装前必须确定：

1. 需要观察的命名空间列表；
2. Kubernetes apiserver 可达地址和一次性管理员 kubeconfig；
3. Dashboard 是否仅本机访问，或绑定哪一个管理网 IP；
4. 是否启用部署工具或自动恢复；
5. 是否现在接入诊断模型。

第 5 项可以安装后再配置。建议先以无模型、只读模式完成集群身份和
Dashboard 验收，再接入内网模型。

## 4. openEuler aarch64 快速安装

从 Release 下载 `linux-arm64` 宿主机包及对应 `.sha256` 文件，传入内网后：

```bash
uname -m

sha256sum --check \
  infernex-agent-host-offline-0.3.0-linux-arm64.tar.gz.sha256
tar -xzf infernex-agent-host-offline-0.3.0-linux-arm64.tar.gz
cd infernex-agent-host-offline-0.3.0-linux-arm64
```

一次性使用管理员身份创建专用、命名空间级运行身份：

```bash
sudo ./bin/create-kubeconfig.sh \
  --admin-kubeconfig /etc/kubernetes/admin.conf \
  --target-namespace model-a \
  --target-namespace model-b \
  --output /root/infernex-agent-host.kubeconfig
```

安装 systemd 服务。以下示例仅让 Dashboard 绑定管理网 IP，MCP 仍保持
`127.0.0.1:8080`：

```bash
sudo ./bin/install-host.sh \
  --kubeconfig /root/infernex-agent-host.kubeconfig \
  --scan-namespace model-a \
  --scan-namespace model-b \
  --dashboard-listen-address 10.20.0.10:8081
```

如只允许本机访问，省略 `--dashboard-listen-address`。

## 5. 模型配置时机

三种方式均受支持：

1. **不配置模型**：推荐作为首次安装和故障隔离基线；
2. **安装时配置**：向 `install-host.sh` 同时传入模型端点和模型名；
3. **安装后配置**：使用已安装的配置命令，可换模、轮换密钥、测试或禁用。

安装后配置示例：

```bash
sudo /opt/infernex-agent/bin/configure-model.sh \
  --base-url http://10.20.0.30:8000/v1 \
  --model ops-diagnostic-model \
  --api-key-file /root/infernex-openai.key \
  --timeout 60s \
  --test \
  --show
```

`--test` 会发送一次很小的 `chat/completions` 请求，可能产生少量模型
Token。完整说明见 [模型配置手册](model-configuration-zh.md)。

## 6. 访问入口

默认宿主机端口：

| 入口 | 默认地址 | 用途 |
| --- | --- | --- |
| MCP | `http://127.0.0.1:8080/mcp` | 供本机 Agent Runtime / kubectl-ai 使用 |
| MCP 健康检查 | `http://127.0.0.1:8080/readyz` | 服务探活 |
| Dashboard | `http://127.0.0.1:8081/` | 运维展示 |
| 快照 API | `http://127.0.0.1:8081/api/v1/snapshot` | 只读 JSON 证据 |

MCP 和 Dashboard 本身不是认证边界。跨主机开放时必须使用管理网 ACL、
防火墙或带身份认证的反向代理。

## 7. 日常操作

```bash
sudo systemctl status infernex-agent --no-pager
sudo journalctl -u infernex-agent -f

sudo /opt/infernex-agent/bin/configure-model.sh --show

curl --fail http://127.0.0.1:8080/readyz
curl --fail http://127.0.0.1:8081/readyz
curl --fail http://127.0.0.1:8081/api/v1/snapshot
```

宿主机配置位置：

```text
/opt/infernex-agent/bin/       二进制、启动器和模型配置工具
/etc/infernex-agent/agent.conf 非敏感有效参数，一行一个参数
/etc/infernex-agent/kubeconfig 专用集群身份，0600
/etc/infernex-agent/openai-api-key  可选模型密钥，0600
/var/lib/infernex-agent/       服务工作目录
```

## 8. 产品验收

上线前至少验证：

1. systemd 单元为 `active`；
2. MCP 和 Dashboard 的 `/readyz` 返回成功；
3. 专用 kubeconfig 只能访问批准命名空间；
4. Dashboard 只从批准管理网可达；
5. 未启用写能力时不存在 mutation RBAC；
6. 配置模型时 `configure-model.sh --test` 成功；
7. 断开模型端点后，规则扫描和 Dashboard 仍持续工作；
8. 凭据文件权限、备份、轮换和卸载策略符合内部要求。

## 9. 文档地图

- [产品设计](product-design-zh.md)
- [变更保护、备份与回退](change-safety-zh.md)
- [模型配置](model-configuration-zh.md)
- [安全与能力边界](security-boundaries-zh.md)
- [运维手册](operations-runbook-zh.md)
- [openEuler 宿主机安装](host-install-openeuler-zh.md)
- [集群内/离线安装](offline-install-zh.md)
- [英文架构说明](architecture.md)
