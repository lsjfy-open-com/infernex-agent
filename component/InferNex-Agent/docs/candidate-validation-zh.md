# InferNex Agent 候选版本验收

## 当前状态

新统一安装流程仍在 Draft PR 中，必须先在 A2 既有 InferNex 集群验收，之后才能合并并
发布新 Release。公开 `0.3.0-rc.6` 是旧版，不能用来验证新的一键安装入口。首个支持
无 Bridge CRD 安装的候选版本是 `0.4.0-rc.2`。

CI 为每个架构生成一个默认 Agent Artifact：

```text
infernex-agent-<版本>-linux-amd64
infernex-agent-<版本>-linux-arm64
```

Artifact 内含同名 `.tar.gz` 和 `.sha256`。CI 还可能保留 Kubernetes 高级 Bundle 供
开发测试，但它不会进入普通 Release，也不是 A2 管理节点验收对象。

## A2 验收步骤

1. 从 Draft PR 最新成功的 workflow 下载 arm64 Agent Artifact；
2. 传入 A2 管理节点并校验 SHA256；
3. 解压后运行 `sudo ./install.sh`；
4. 确认脚本自动发现 kubeconfig、InferNex CRD、Bridge profile 和现有实例；
5. 配置内网 OpenAI 兼容模型接口，确认 tool calling 测试通过；
6. 运行 `sudo infernex-agent chat`，先做只读全环境扫描；
7. 批准一个从稳定来源创建的测试实例，观察 Ready/Degraded、事件和日志；
8. 制造或等待一次失败，确认只撤销本次带相同 `changeId` 的新资源；
9. 检查 Dashboard、systemd 重启恢复、变更记录和安装前恢复点；
10. 验收通过后再允许 workflow 发布 prerelease，并合并 Draft PR。

## 必须通过的证据

- 安装时没有创建 Agent Pod、Controller 或 CRD；
- 默认路径没有创建 ServiceAccount/RBAC，且 `/etc/infernex-agent/kubeconfig` 权限受限；
- 安装阶段只有一个空的 `infernex-agent-workspace` Namespace；
- Agent 能扫描现有 InferNexService 和关联工作负载；
- 所有写工具都要求本机 `yes`，任意 YAML/shell 不可用；
- 稳定来源校验、Ready 观察、失败回退和重启续跑均有记录；
- 模型 API Key 不出现在 unit 参数、日志或普通配置中；
- 断开外网后静态二进制、扫描、Dashboard 和回退仍正常。
- 删除/不存在 Bridge CRD 时安装仍成功，doctor 显示 `generic-kubernetes` 警告，且
  不创建 workspace 或启用 Bridge 专属写工具。

## 发布门禁

正式 Release 只上传四个文件：amd64/arm64 的 Agent 归档及各自 SHA256。Release notes
必须说明按 CPU 架构选择，不再并列展示宿主机/集群包。未经上述既有集群验收，不发布
新 Release、不把 PR 从 Draft 改为 Ready、也不合并。
