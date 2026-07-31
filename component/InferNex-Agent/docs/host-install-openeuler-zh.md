# openEuler 管理/引导节点宿主机部署

本文说明如何把 InferNex Agent 作为普通 Linux systemd 服务安装到
Kubernetes master 或集群引导节点，而不是安装为集群内 Pod。

## 1. A2 与 openEuler 是否需要单独版本

Atlas A2 是 NPU/服务器产品系列，不能仅凭 “A2” 判断管理节点 CPU 架构。
先在目标节点执行：

```bash
uname -m
cat /etc/os-release
```

选择关系如下：

| `uname -m` | Agent 包 |
|---|---|
| `x86_64` | `linux-amd64` |
| `aarch64` | `linux-arm64` |

Agent 是 `CGO_ENABLED=0` 的静态 Go 二进制，不依赖 openEuler 的 glibc
版本，也不链接 CANN、NPU 驱动或算子库。它只通过 Kubernetes API 和
可选的内网 OpenAI 接口工作，因此不需要 “openEuler 专用 Agent 代码”；
需要的是与 CPU 架构匹配的宿主机包和 systemd 安装方式。

## 2. 宿主机模式与集群内模式

| 项目 | 集群内 Helm | master/引导节点 systemd |
|---|---|---|
| 运行位置 | Kubernetes Pod | 普通 Linux 进程 |
| 集群身份 | 投射的 ServiceAccount Token | 专用 kubeconfig |
| 生命周期 | Deployment/Helm | systemd |
| Dashboard | Service/NodePort | 主机监听地址与防火墙 |
| 适合场景 | Kubernetes 原生运维 | 固定运维节点、引导节点、离线管理面 |
| InferNex 编排 | 继续由 Bridge 完成 | 继续由 Bridge 完成 |

两种模式使用完全相同的 Agent 核心和权限边界。宿主机模式不是绕过
Kubernetes：Agent 仍通过 apiserver 读取 `InferNexService`、Pod、Event
和受管工作负载，并且只在显式授权时创建受约束的恢复服务。

不建议同一批命名空间同时运行两个自动恢复实例，否则虽然创建操作具备
幂等保护，仍会产生重复扫描和诊断调用。迁移时应先关闭旧实例的自动恢复。

## 3. 宿主机离线包

联网 Linux 构建机需要 Go 1.24+、Bash、`tar` 和 `sha256sum`：

```bash
cd component/InferNex-Agent

./scripts/offline/build-host-bundle.sh \
  --version 0.3.0 \
  --architecture arm64 \
  --output-dir ./dist
```

输出：

```text
infernex-agent-host-offline-0.3.0-linux-arm64.tar.gz
infernex-agent-host-offline-0.3.0-linux-arm64.tar.gz.sha256
```

已有 CI 交叉编译二进制时可复用：

```bash
./scripts/offline/build-host-bundle.sh \
  --version 0.3.0 \
  --architecture arm64 \
  --binary /secure/build/infernex-agent-linux-arm64 \
  --output-dir ./dist
```

宿主机包只含静态二进制、systemd 安装工具、最小权限 kubeconfig
生成器、验证工具、文档和校验和，不包含容器镜像。

## 4. 传入内网并校验

```bash
sha256sum --check \
  infernex-agent-host-offline-0.3.0-linux-arm64.tar.gz.sha256

tar -xzf infernex-agent-host-offline-0.3.0-linux-arm64.tar.gz
cd infernex-agent-host-offline-0.3.0-linux-arm64

./bin/verify-host.sh --help
```

包内安装器还会再次检查 `SHA256SUMS` 和主机 CPU 架构。

## 5. 一次性创建专用集群身份

不要让 systemd 服务长期读取 `/etc/kubernetes/admin.conf`。该文件通常
具有远超 Agent 所需范围的权限。

使用管理员 kubeconfig 一次性创建 namespace-scoped ServiceAccount、
Role/RoleBinding 和独立运行 kubeconfig：

```bash
sudo ./bin/create-kubeconfig.sh \
  --admin-kubeconfig /etc/kubernetes/admin.conf \
  --target-namespace model-a \
  --target-namespace model-b \
  --output /root/infernex-agent-host.kubeconfig
```

生成身份只能在指定命名空间：

- `get/list` InferNexService；
- `list` Deployment、DaemonSet、LeaderWorkerSet、Pod 和 Event。

如需受控自动恢复，在创建身份时增加：

```bash
--enable-recovery \
--recovery-template-namespace infernex-bridge-system
```

这只增加：

- 目标命名空间创建 `InferNexService`；
- 恢复模板命名空间读取指定 `InferNexServiceConfig`。

如需固定 catalog 部署/删除工具，使用 `--enable-deployment`。不要授予
cluster-admin，也不需要读取 Secret、Node 或创建原始 Deployment。

生成 kubeconfig 内含长期 ServiceAccount Token，权限虽然已收窄，仍应：

- 文件权限保持 `0600`；
- 只通过受控介质传递；
- 纳入组织凭据轮换和吊销制度；
- 条件允许时改用企业 PKI/OIDC 提供的等价最小权限 kubeconfig。

## 6. 在 openEuler 上安装 systemd 服务

### 6.1 仅本机访问

默认 MCP 和 Dashboard 都只绑定 loopback：

```bash
sudo ./bin/install-host.sh \
  --kubeconfig /root/infernex-agent-host.kubeconfig \
  --scan-namespace model-a \
  --scan-namespace model-b
```

本机访问：

```text
MCP:       http://127.0.0.1:8080/mcp
Dashboard: http://127.0.0.1:8081/
```

这适合 kubectl-ai、Codex runtime 或其他 Agent Runtime 也运行在同一
管理节点的场景。

### 6.2 向内网运维终端开放 Dashboard

建议绑定管理网卡的具体 IP，而不是全部网卡：

```bash
sudo ./bin/install-host.sh \
  --kubeconfig /root/infernex-agent-host.kubeconfig \
  --scan-namespace model-a \
  --dashboard-listen-address 10.20.0.10:8081
```

然后仅允许运维网段访问。openEuler 使用 firewalld 时示例为：

```bash
sudo firewall-cmd --permanent \
  --add-rich-rule='rule family=ipv4 source address=10.20.0.0/16 port port=8081 protocol=tcp accept'
sudo firewall-cmd --reload
```

Dashboard 是只读页面，但不是认证边界。不要直接绑定公网地址，也不要对
`0.0.0.0/0` 放行。MCP 默认仍保持 `127.0.0.1:8080`；只有远程 Agent
Runtime 确有需要并已有额外认证代理时，才显式修改 `--listen-address`。

安装器不会自动修改防火墙、SELinux 策略、SSH 或系统网络配置。

## 7. 接入内网模型

不需要模型建议时不配置即可，规则诊断、持续扫描和 Dashboard 仍正常。

需要时：

```bash
sudo ./bin/install-host.sh \
  --kubeconfig /root/infernex-agent-host.kubeconfig \
  --scan-namespace model-a \
  --dashboard-listen-address 10.20.0.10:8081 \
  --openai-base-url http://10.20.0.30:8000/v1 \
  --openai-model ops-diagnostic-model \
  --openai-api-key-file /root/infernex-openai.key
```

API Key 会复制到 `/etc/infernex-agent/openai-api-key`，归属专用
`infernex-agent` 用户、权限 `0600`。Key 不进入 systemd unit、进程参数、
Bundle 或 Dashboard。

## 8. 启用受控恢复

创建 kubeconfig 和安装服务时都要显式启用：

```bash
sudo ./bin/install-host.sh \
  --kubeconfig /root/infernex-agent-host.kubeconfig \
  --scan-namespace model-a \
  --enable-recovery \
  --recovery-template-namespace infernex-bridge-system
```

此外仍要求：

1. 恢复 `InferNexServiceConfig` 带
   `agent.infernex.io/approved-recovery-profile=true`；
2. 源服务带 `agent.infernex.io/auto-recovery=true` 和精确 profile 名；
3. 连续多次临界扫描，且 Bridge 已追上 `observedGeneration`。

Agent 只创建引用批准模板的新 `InferNexService`。InferNex Bridge 继续
负责 PD/聚合工作负载、Service、路由和状态调和。

## 9. 验证和日常运维

```bash
sudo systemctl status infernex-agent --no-pager
sudo journalctl -u infernex-agent -f

sudo ./bin/verify-host.sh \
  --kubeconfig /etc/infernex-agent/kubeconfig \
  --target-namespace model-a
```

检查监听地址：

```bash
sudo ss -lntp | grep infernex-agent
```

安装器使用以下固定目录：

```text
/opt/infernex-agent/bin/       二进制和只读启动脚本
/etc/infernex-agent/           kubeconfig 和可选 API Key
/var/lib/infernex-agent/       systemd 工作目录
/etc/systemd/system/           infernex-agent.service
```

systemd unit 启用了非 root 用户、只读系统目录、空 capability 集、
`NoNewPrivileges`、私有临时目录和内核保护。openEuler 启用 SELinux 时，
安装器会执行 `restorecon`；如组织使用自定义强制策略，应由安全团队审阅
对应 AVC 日志，而不是关闭 SELinux。

## 10. 更新、回退和凭据轮换

新版本解压后重新运行 `install-host.sh` 即可更新。安装器会把上一个二进制
保存在：

```text
/opt/infernex-agent/bin/infernex-agent.previous
```

手工回退：

```bash
sudo systemctl stop infernex-agent
sudo cp /opt/infernex-agent/bin/infernex-agent.previous \
  /opt/infernex-agent/bin/infernex-agent
sudo restorecon -F /opt/infernex-agent/bin/infernex-agent 2>/dev/null || true
sudo systemctl start infernex-agent
```

轮换 ServiceAccount Token：

```bash
sudo ./bin/create-kubeconfig.sh \
  --admin-kubeconfig /etc/kubernetes/admin.conf \
  --target-namespace model-a \
  --output /root/infernex-agent-host.kubeconfig \
  --rotate-token \
  --force

sudo ./bin/install-host.sh \
  --kubeconfig /root/infernex-agent-host.kubeconfig \
  --scan-namespace model-a
```

## 11. 卸载

```bash
sudo ./bin/uninstall-host.sh
```

默认保留 `/etc/infernex-agent` 凭据和 Kubernetes RBAC，避免误删仍被其他
实例使用的身份。确认不再需要后：

```bash
sudo ./bin/uninstall-host.sh --purge-credentials --purge-user

kubectl -n infernex-system delete \
  serviceaccount/infernex-agent-host \
  secret/infernex-agent-host-token
```

再按实际目标命名空间删除名为 `infernex-agent-host` 和
`infernex-agent-host-mutation` 的 Role/RoleBinding。卸载不会删除
InferNexService、Bridge、推理 Pod、模型 PVC 或 NPU 环境。
