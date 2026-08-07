# InferNex Agent 安装模式与高级选项

## 默认产品形态

V1 只有一个默认形态：在能操作 InferNex 的 Linux 管理节点上运行静态 Agent 二进制，
由 systemd 保持运行，使用当前 kubeconfig 调用现有 Kubernetes 和 InferNex 组件接口。

```text
用户 / SSH / Web
       ↓
管理节点上的 InferNex Agent
       ↓ 当前 kubeconfig + typed tools
Kubernetes API / InferNex Bridge / 已有组件接口
```

管理节点可以是 master、bootstrap、运维跳板机或普通集群节点；这不会产生不同安装包。
默认安装不创建 Agent Pod、Controller、CRD、ServiceAccount 或 RBAC。

## 普通安装

在线：

```bash
curl -fsSL https://raw.githubusercontent.com/lsjfy-open-com/infernex-agent/main/component/InferNex-Agent/scripts/install.sh | sudo bash
```

离线：按 CPU 架构下载唯一的 `infernex-agent-<版本>-linux-<架构>.tar.gz`，校验、解压
后运行 `sudo ./install.sh`。详情见[离线安装](offline-install-zh.md)。

安装器发现顺序为：显式 `--admin-kubeconfig`、调用 sudo 的用户 kubeconfig、root
kubeconfig、`/etc/kubernetes/admin.conf`、k3s kubeconfig。它会把当前上下文展开为只对
root 可读的 systemd 运行配置。若 kubeconfig 依赖外部 `exec` 凭据插件，则使用自包含
admin.conf，或选择下述 hardened identity。

安装器创建一个空的 `infernex-agent-workspace` Namespace，供后续被批准的新服务隔离
使用；不会在安装阶段创建 Agent 或推理 Pod。

## Hardened identity（可选）

```bash
sudo ./install.sh --hardened-identity
```

该选项使用安装时的管理员 kubeconfig，一次性创建专用 ServiceAccount、namespace-scoped
RBAC 和独立 kubeconfig。适用于不允许 systemd 长期保存管理员凭据的环境。新增业务
namespace 后需要重新执行安装器以扩充 allowlist。

这是一项安全部署策略，不是另一个产品模式，也不需要下载不同的包。

## 其他安装参数

```text
--admin-kubeconfig FILE       指定发现用 kubeconfig
--dashboard-listen-address A  Dashboard 地址，默认 127.0.0.1:8081
--skip-model-setup            暂不配置模型接口
--non-interactive             CI/自动化安装
```

`create-kubeconfig.sh`、`install-host.sh` 等底层脚本仅用于审计、CI、恢复或精细定制，
普通用户无需逐项填写其中参数。

## 集群内 Helm（高级、非默认）

仓库仍保留 Helm Chart 和带镜像归档的 Kubernetes 离线 Bundle，用于确实要求
Kubernetes 管理 Agent 生命周期的环境。它会创建 Deployment、ServiceAccount 和 RBAC，
安装复杂度和升级边界都不同，因此不作为普通 Release 资产，也不应让 V1 用户选择。

只有满足以下明确条件时才考虑它：

- 禁止在管理节点运行 systemd 服务；
- 运维平台强制所有常驻组件使用 Helm/GitOps；
- 已设计集群内模型接口、凭据、持久状态、Dashboard 暴露与 NetworkPolicy。

高级 Bundle 可从 CI Artifact 构建或取得，入口为 `scripts/offline/build-bundle.sh` 和
归档内 `bin/install-agent.sh`。它与默认 Linux Agent 使用同一个核心二进制和安全工具，
但不是普通用户安装路径。

## 更新、回退和卸载

使用新版本包重新执行 `sudo ./install.sh` 即可升级；模型配置会保留。安装器会先在
`/var/lib/infernex-agent/backups/install-*` 保存带校验和的恢复点，安装或健康检查失败
时自动恢复。

手工恢复：

```bash
sudo /opt/infernex-agent/bin/restore-host-install.sh \
  --backup-dir /var/lib/infernex-agent/backups/install-<时间> --confirm
```

卸载脚本默认保留凭据、状态和恢复点；只有确认不再需要恢复时才使用 purge 参数。

## 设计依据

默认路径参考了 kubectl-ai、K8sGPT 和 HolmesGPT 的共同做法：本地 CLI 使用现有
kubeconfig，通过工具集和 runbook 驱动 Agentic 探索；kagent 这类 Controller/CRD
方案只作为更复杂的集群内形态参考。来源和取舍见
[工具集与知识库设计](toolsets-and-knowledge-zh.md)。
