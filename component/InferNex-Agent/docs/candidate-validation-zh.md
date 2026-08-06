# InferNex Agent 单二进制候选版本灰度验证

本文定义“先在既有集群验证，验收后再正式打包”的发布流程。候选版本不是 Release，
不会生成新的集群离线 Bundle，也不会重新打包 vLLM、vLLM-Ascend、Mooncake、CANN、
模型权重或已有推理镜像。

若本次改动涉及首次安装和自动发现，请从对应 CI 运行下载完整的
`infernex-agent-host-offline-0.3.0-linux-arm64` Artifact，而不只是裸二进制。校验并
解压后，在既有 InferNex 管理节点执行：

```bash
sudo ./install.sh --skip-model-setup
sudo infernex-agent doctor --config /etc/infernex-agent/agent.conf --skip-model
sudo infernex-agent setup
sudo infernex-agent chat
```

这个候选宿主机包用于验证一键安装、环境发现、专用 RBAC、Agentic 对话和稳定基线
部署；验证通过后才发布新的正式/预发布包。旧 `0.3.0-rc.6` 不包含 `install.sh`，
不能用于验证这一入口。

## 1. 两条相互独立的流水线

| 阶段 | 产物 | 用途 | 保留策略 |
| --- | --- | --- | --- |
| 提交/PR 验证 | `linux-amd64`、`linux-arm64` 静态候选二进制及 SHA256 | 复制到既有管理节点灰度 | GitHub Actions Artifact，14 天 |
| 正式发行 | 裸二进制、宿主机 Bundle、集群 Bundle 及 SHA256 | 验收后的在线/离线交付 | GitHub Release |

候选二进制只有在单元测试、`go vet`、Helm 渲染、kind、真实微型模型推理、宿主机
systemd 安装、模型工具调用、离线重装和回退测试全部通过后才会生成。因此开发人员
不需要在被管理服务器上安装 Go、Python、Node、GCC 或其他编译环境。

## 2. 获取候选二进制

打开仓库的 **Actions → InferNex Agent → 对应提交**。工作流成功后下载与服务器匹配的
Artifact：

- x86_64：`infernex-agent-0.4.0-candidate.<提交>-linux-amd64`；
- aarch64：`infernex-agent-0.4.0-candidate.<提交>-linux-arm64`。

Artifact 内只有二进制及同名 `.sha256`。也可以在联网运维机使用 GitHub CLI 下载，
再通过 XShell/SCP 传到内网管理节点：

```bash
gh run download <RUN_ID> \
  --repo lsjfy-open-com/infernex-agent \
  --name 'infernex-agent-0.4.0-candidate.<提交>-linux-arm64'
```

不要把临时候选 Artifact 当作长期软件源；正式验收记录必须固定工作流 URL、完整提交
SHA 和候选文件 SHA256。

## 3. 变更前检查

以下示例保留 Artifact 中的原始文件名，确保 `.sha256` 内记录的名称可以直接校验：

```bash
cd /root
candidate='infernex-agent-0.4.0-candidate.<提交>-linux-arm64'
sha256sum --check "${candidate}.sha256"
chmod 0755 "$candidate"

./"$candidate" version --json
./"$candidate" candidate verify \
  --file ./"$candidate" \
  --expect-sha256 "$(awk '{print $1}' "${candidate}.sha256")"

sudo ./"$candidate" doctor \
  --config /etc/infernex-agent/agent.conf
```

`candidate verify` 会拒绝以下文件：

- 非当前 CPU 架构的 ELF；
- 包含动态解释器的二进制；
- `CGO_ENABLED` 不是 `0` 的构建；
- 不可执行文件、符号链接、超大文件或 SHA256 不一致的文件；
- 不能返回有效 `version --json` 元数据的文件。

`doctor` 只执行诊断检查：静态构建信息、Kubernetes API、InferNex CRD、批准命名空间
列表权限、当前 Agent 健康接口和已配置模型端点。它不会修改集群。若验证时暂不希望
调用模型，可增加 `--skip-model`；若当前服务尚未启动，可增加 `--skip-local`。

## 4. 原子切换候选版本

首次从 `v0.3.0-rc.6` 进入候选流程时，应直接使用新候选文件执行切换；不要求旧版本
已经包含 `candidate` 子命令：

```bash
sudo ./"$candidate" candidate apply \
  --file ./"$candidate" \
  --expect-sha256 "$(awk '{print $1}' "${candidate}.sha256")" \
  --target /opt/infernex-agent/bin/infernex-agent \
  --state-dir /var/lib/infernex-agent/candidates \
  --service infernex-agent.service \
  --health-url http://127.0.0.1:8080/healthz \
  --timeout 90s
```

该操作只替换 Agent 二进制，复用现有的：

- `/etc/infernex-agent/agent.conf`；
- `/etc/infernex-agent/kubeconfig`；
- `/etc/infernex-agent/openai-api-key`；
- systemd unit、RBAC、批准 profile 和现有 InferNex 工作负载。

切换前，当前二进制会按 SHA256 备份到
`/var/lib/infernex-agent/candidates/`。候选文件通过同目录临时文件写入并原子重命名，
然后重启服务并等待健康接口。若重启或健康检查失败，程序会自动恢复旧二进制、再次
重启并验证旧版本；操作记录和失败原因保留在受保护的 JSON 记录中。

`--no-restart` 只用于受控实验或前台运行测试；使用它时不会执行健康门禁，不能代替
正常灰度验收。

## 5. 灰度观察和人工回退

至少观察两个完整扫描周期，并执行：

```bash
sudo systemctl is-active infernex-agent.service
sudo journalctl -u infernex-agent.service --since '-30 min' --no-pager
curl --fail http://127.0.0.1:8080/readyz
curl --fail http://127.0.0.1:8081/api/v1/snapshot

sudo /opt/infernex-agent/bin/infernex-agent doctor \
  --config /etc/infernex-agent/agent.conf
sudo /opt/infernex-agent/bin/chat.sh --ask '扫描配置的命名空间并总结异常'
```

已成功启动的候选仍可人工恢复到切换前版本：

```bash
sudo /opt/infernex-agent/bin/infernex-agent candidate rollback \
  --target /opt/infernex-agent/bin/infernex-agent \
  --state-dir /var/lib/infernex-agent/candidates \
  --service infernex-agent.service \
  --health-url http://127.0.0.1:8080/healthz \
  --timeout 90s
```

人工回退也会备份当前候选；如果旧版本无法重新健康，程序尝试把候选恢复回来并记录
`rollback_failed`，不会静默报告成功。

## 6. 验收后再发布

候选满足以下条件后，才以同一个提交 SHA 触发正式 Release：

1. 架构、静态链接和 SHA256 校验通过；
2. `doctor` 无阻断失败；
3. 服务重启、MCP、Dashboard 和连续扫描正常；
4. 模型已配置时，Chat Completions 与自然语言只读查询正常；
5. 已拉起的 InferNex 实例、vLLM-Ascend/Mooncake 组件无新增中断或乱码；
6. 未开启的写能力没有获得新增权限；
7. 自动失败回退和人工回退各至少验证一次；
8. 验收记录包含提交 SHA、文件 SHA256、环境版本、观察时间和结论。

正式工作流随后发布两个裸二进制、两个宿主机 Bundle、两个集群 Bundle 及各自校验
文件。只有这一阶段需要重新制作归档；日常开发和现网候选验证不重新打包。
