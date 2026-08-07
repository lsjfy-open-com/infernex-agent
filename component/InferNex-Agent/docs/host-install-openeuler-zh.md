# openEuler A2 管理节点安装

openEuler A2 的 `aarch64` 管理/master/引导节点使用普通 Linux arm64 Agent 包，
不需要专门的 openEuler 包、容器镜像或编译环境：

```text
infernex-agent-<版本>-linux-arm64.tar.gz
infernex-agent-<版本>-linux-arm64.tar.gz.sha256
```

传入服务器后执行：

```bash
sha256sum --check infernex-agent-*-linux-arm64.tar.gz.sha256
tar -xzf infernex-agent-*-linux-arm64.tar.gz
cd infernex-agent-*-linux-arm64
sudo ./install.sh
```

安装器会发现当前 kubeconfig，并自动区分 Bridge CRD 与 Helm/BKE 形态，安装静态
二进制与 systemd 服务，并只询问 Agent 模型接口。它不使用 NPU 驱动，不拉取或替换
现有 vLLM-Ascend 0.23.0、Mooncake、CANN 镜像和模型权重。

默认不在集群中安装 Agent Pod、Controller、CRD、ServiceAccount 或 RBAC。只有已有
Bridge 时才创建空的 `infernex-agent-workspace`；Helm/BKE 基础兼容模式不修改集群。

## openEuler 检查

```bash
uname -m                       # 应为 aarch64
systemctl --version
kubectl get --raw=/version
kubectl config current-context
kubectl api-resources | grep -i infernex  # 仅用于识别形态；没有输出也不阻止安装
```

若使用 `/etc/kubernetes/admin.conf`：

```bash
sudo ./install.sh --admin-kubeconfig /etc/kubernetes/admin.conf
```

已安装 Bridge，且安全策略不允许 systemd 保存当前管理员身份时：

```bash
sudo ./install.sh --hardened-identity
```

此高级选项才会创建 namespace-scoped ServiceAccount/RBAC。无需下载另一种包。
Helm/BKE 基础兼容模式暂不接受该选项，请先复用当前 kubeconfig 完成安装验证。

## 使用和访问

```bash
sudo infernex-agent setup
sudo infernex-agent chat
sudo systemctl status infernex-agent
sudo journalctl -u infernex-agent -f
```

Dashboard 默认只监听 `127.0.0.1:8081`，通过 XShell/SSH 做本地端口转发：

```bash
ssh -L 8081:127.0.0.1:8081 <A2管理节点>
```

浏览器访问 `http://127.0.0.1:8081/`。

更新、恢复、卸载和安全边界分别见[安装模式](install-and-modes-zh.md)、
[变更保护与回退](change-safety-zh.md)和[安全边界](security-boundaries-zh.md)。
