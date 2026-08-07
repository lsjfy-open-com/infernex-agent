# InferNex Agent 离线安装

## 先说结论：下载哪个文件

Release 中只需要选择管理服务器的 CPU 架构：

| `uname -m` | 下载文件 |
| --- | --- |
| `x86_64` | `infernex-agent-<版本>-linux-amd64.tar.gz` |
| `aarch64` | `infernex-agent-<版本>-linux-arm64.tar.gz` |

同时下载同名 `.sha256` 文件。不要下载裸二进制、容器镜像归档或 Kubernetes
测试包；这些只作为 CI/高级集成产物存在，不是 V1 用户安装入口。

所谓 master、引导节点、管理节点或“宿主机”没有不同的软件形态：只要这台 Linux
服务器上的 `kubectl` 已经能访问目标 InferNex 集群，就安装同一个 Agent 包。

## 安装前提

- Linux amd64 或 arm64；
- `kubectl` 已安装，当前 context 可以访问目标集群；
- 集群中已经安装 InferNex 与 InferNex Bridge；
- 服务器具有 systemd；
- 准备一个支持 OpenAI `chat/completions` 和 tool calls 的模型接口。

不需要 Go、Python、Node.js、Docker、Helm 或编译器。Agent 包不包含、也不会下载
vLLM-Ascend、Mooncake、CANN、模型权重或新的推理镜像。

## 三条命令

把 `.tar.gz` 和 `.sha256` 上传到管理服务器后执行：

```bash
sha256sum --check infernex-agent-<版本>-linux-<架构>.tar.gz.sha256
tar -xzf infernex-agent-<版本>-linux-<架构>.tar.gz
cd infernex-agent-<版本>-linux-<架构>
sudo ./install.sh
```

安装器会自动：

1. 校验包内所有文件；
2. 发现当前用户、root、kubeadm 或 k3s 的可用 kubeconfig；
3. 读取当前 context 并生成受保护的本地副本；
4. 检查 InferNex CRD、Bridge profile 和已有服务；
5. 安装静态二进制与本地 systemd 服务；
6. 启动持续扫描和回环地址 Dashboard；
7. 只询问模型 Base URL、model ID 和可选 API Key；
8. 测试模型 tool-calling 兼容性。

默认不会在 Kubernetes 中安装 Agent Pod、Controller、CRD、ServiceAccount 或
RBAC。Agent 使用运维人员当前的 kubectl 身份，实际权限由现有 kubeconfig 和
Kubernetes RBAC 决定；模型本身看不到该凭据。

## 使用

```bash
sudo infernex-agent doctor
sudo infernex-agent chat
```

示例问题：

```text
扫描当前 InferNex 环境，说明有哪些模型服务和异常。
分析 qwen 服务最近的中断，关联各节点日志和 Event。
基于当前 Ready 的稳定配置部署一个验证实例，并持续观察到成功或回退。
```

Dashboard 默认地址为 `127.0.0.1:8081`。通过 XShell/SSH 转发访问：

```bash
ssh -L 8081:127.0.0.1:8081 <管理服务器>
```

## 只有两种高级情况需要额外处理

### 独立最小权限身份

组织不允许长期服务使用当前运维身份时：

```bash
sudo ./install.sh --hardened-identity
```

安装器才会创建专用 ServiceAccount、namespace-scoped RBAC 和独立 kubeconfig。

### 集群内 Pod 运行

只有明确要求 Agent 由 Kubernetes 自身调度、高可用或统一托管时，才使用 Helm
Chart。该模式见[安装与运行模式高级手册](install-and-modes-zh.md)，不属于默认
Release 下载流程。

## 升级与回退

下载新版本同架构包并再次执行 `sudo ./install.sh`。安装器在覆盖二进制、配置、
凭据和 systemd unit 前创建恢复点；安装失败会恢复旧版本。Agent 发起的部署另有
持久化 `changeId` 和 Ready/Degraded/超时回退机制。

详见[变更保护、备份与回退](change-safety-zh.md)。
