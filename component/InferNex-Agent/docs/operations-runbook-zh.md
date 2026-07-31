# InferNex Agent 运维手册

## 1. 服务状态

```bash
sudo systemctl status infernex-agent --no-pager
sudo systemctl is-enabled infernex-agent
sudo systemctl is-active infernex-agent
sudo journalctl -u infernex-agent -f
```

健康检查：

```bash
curl --fail http://127.0.0.1:8080/healthz
curl --fail http://127.0.0.1:8080/readyz
curl --fail http://127.0.0.1:8081/readyz
curl --fail http://127.0.0.1:8081/api/v1/snapshot
```

如果使用非默认监听地址，应替换为实际管理 IP 和端口。

## 2. 配置盘点

```bash
sudo sed -n '1,200p' /etc/infernex-agent/agent.conf
sudo stat -c '%U:%G %a %n' \
  /etc/infernex-agent/agent.conf \
  /etc/infernex-agent/kubeconfig
sudo /opt/infernex-agent/bin/configure-model.sh --show
```

不要输出或复制 `/etc/infernex-agent/openai-api-key` 内容。

## 3. 模型运维

测试当前配置：

```bash
sudo /opt/infernex-agent/bin/configure-model.sh --test --show
```

换模、轮换密钥和禁用方法见
[模型配置手册](model-configuration-zh.md)。

模型故障不会停止规则扫描。确认影响范围时，先检查 Dashboard 快照是否仍在
更新，再查看日志中的模型请求错误。

## 4. Kubernetes 身份验证

```bash
sudo -u infernex-agent kubectl \
  --kubeconfig /etc/infernex-agent/kubeconfig \
  auth can-i list infernexservices.infernex.infernex.io \
  --namespace model-a

sudo -u infernex-agent kubectl \
  --kubeconfig /etc/infernex-agent/kubeconfig \
  get infernexservices.infernex.infernex.io \
  --namespace model-a
```

默认只读安装不应能读取 Secret 或创建 Deployment：

```bash
sudo -u infernex-agent kubectl \
  --kubeconfig /etc/infernex-agent/kubeconfig \
  auth can-i get secrets --namespace model-a

sudo -u infernex-agent kubectl \
  --kubeconfig /etc/infernex-agent/kubeconfig \
  auth can-i create deployments --namespace model-a
```

预期均为 `no`。

## 5. 升级

1. 下载与 `uname -m` 匹配的新宿主机包；
2. 验证外层 SHA256；
3. 解压并查看版本说明；
4. 使用现有专用 kubeconfig 重跑安装器；
5. 验证 systemd、Kubernetes 权限、MCP 和 Dashboard。

```bash
sudo ./bin/install-host.sh \
  --kubeconfig /etc/infernex-agent/kubeconfig \
  --scan-namespace model-a \
  --dashboard-listen-address 10.20.0.10:8081
```

如果升级命令没有传入任何 `--openai-*` 参数，安装器会保留现有模型配置和
API Key。安装器会重启已经运行的服务，使新二进制和配置生效。

## 6. 二进制回退

安装器将上一版本保存为：

```text
/opt/infernex-agent/bin/infernex-agent.previous
```

确认文件存在后：

```bash
sudo systemctl stop infernex-agent
sudo cp -a \
  /opt/infernex-agent/bin/infernex-agent.previous \
  /opt/infernex-agent/bin/infernex-agent
sudo systemctl start infernex-agent
sudo systemctl status infernex-agent --no-pager
```

当前安装器会在覆盖文件前创建带校验和的完整安装恢复点；安装或安装后验证失败
时会自动恢复原二进制、配置、凭据、systemd 状态和 Agent 变更过的集群源资源。
`.previous` 仅作为紧急的单文件人工回退入口。完整流程见
[变更保护、备份与回退](change-safety-zh.md)。

## 7. Kubernetes Token 轮换

使用管理员 kubeconfig 重新运行 `create-kubeconfig.sh`，输出到临时安全路径：

```bash
sudo ./bin/create-kubeconfig.sh \
  --admin-kubeconfig /etc/kubernetes/admin.conf \
  --target-namespace model-a \
  --output /root/infernex-agent-host-new.kubeconfig
```

重新运行安装器替换运行身份。确认新身份正常后，按照组织流程删除或吊销旧的
ServiceAccount Token Secret。不要在未确认新服务健康前删除旧凭据。

## 8. 备份与恢复

安装器会自动纳入恢复点的内容：

- `/etc/infernex-agent/agent.conf`；
- `/etc/infernex-agent/kubeconfig`；
- 可选 `/etc/infernex-agent/openai-api-key`；
- 目标命名空间、RBAC 和恢复 profile 的版本化声明。
- 全部目标命名空间中的 `InferNexService` 源对象；
- 原二进制、启动器、配置工具和 systemd unit。

二进制和脚本应从已校验 Release 重新安装，不应把 `/opt` 目录作为唯一备份。
备份中包含的 kubeconfig/API Key 必须加密并具有访问审计。
默认位置为 `/var/lib/infernex-agent/backups/install-*/`。使用
`infernex-agent cluster-state verify` 定期验证快照校验和。

## 9. 故障处理

| 现象 | 首要检查 | 处理 |
| --- | --- | --- |
| systemd 启动失败 | `journalctl -u infernex-agent -n 100` | 检查配置格式、架构和文件权限 |
| Dashboard 不可达 | `ss -lntp`、firewalld、`readyz` | 检查监听 IP 和来源 ACL |
| 快照为空 | 命名空间配置和 RBAC | 验证 `agent.conf` 与 `kubectl auth can-i` |
| Kubernetes TLS 错误 | kubeconfig CA 和 apiserver 地址 | 重新生成自包含 kubeconfig |
| 模型 401/403 | `configure-model.sh --test` | 轮换 Key、检查模型网关策略 |
| 模型超时 | 网络、模型负载、timeout | 调整端点容量或超时 |
| 没有模型分析 | 服务是否存在问题 | 健康服务不会调用模型 |
| 重复恢复服务 | 是否运行多个恢复实例 | 一个命名空间只保留一个恢复控制实例 |

## 10. 建议监控项

当前版本提供健康端点、快照和日志，建议外部监控至少采集：

- systemd active 状态和进程重启次数；
- MCP/Dashboard `readyz`；
- 快照更新时间；
- 临界问题服务数量；
- 模型请求错误和超时日志；
- 自动恢复尝试、拒绝和创建结果；
- kubeconfig/API Key 轮换到期时间；
- 宿主机磁盘、内存和网络可达性。

## 11. 诊断信息采集

```bash
sudo systemctl status infernex-agent --no-pager
sudo journalctl -u infernex-agent --since -30min --no-pager
sudo systemd-analyze security infernex-agent.service
curl --fail http://127.0.0.1:8081/api/v1/snapshot
```

分享诊断包前必须删除 Token、API Key、内部地址和业务敏感服务名称。

## 12. 卸载

保留凭据和 Kubernetes RBAC：

```bash
sudo ./bin/uninstall-host.sh
```

同时删除宿主机凭据和系统用户：

```bash
sudo ./bin/uninstall-host.sh --purge-credentials --purge-state --purge-user
```

默认卸载还会保留 `/var/lib/infernex-agent` 中的变更记录和恢复点；只有
`--purge-state` 会删除。卸载器不会删除 InferNex 工作负载、ServiceAccount、Role/RoleBinding 或 Token
Secret。确认没有其他 Agent 使用后，再由 Kubernetes 管理员清理这些对象。
