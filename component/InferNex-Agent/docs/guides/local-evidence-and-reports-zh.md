# 本地历史日志分析与 Markdown 报告

InferNex Agent 把交互式当前目录与后台 Evidence Root 分成两种用途。TUI 可直接读取启动目录；后台
服务若要跨 Session、无人值守地持续扫描某个固定目录，才需要登记 Evidence Root。

这用于处理无法从 Kubernetes 重新取得的材料，例如：

- Pod、systemd 或部署脚本重启前人工保存的日志；
- 多节点打包下载的 vLLM、vLLM-Ascend、Mooncake、PD-Orchestrator、CANN/HCCL 日志；
- infernex-checker、网络互 ping、时延或 NPU 检查结果；
- 现场人员补充的时间线、配置差异和复现记录。

## 交互式分析：无需登记

```bash
cd /data/array/incidents/case-20260818
sudo infernex-agent tui
```

模型可在该目录内使用 read、glob/find、grep、ls。创建或修改 Markdown 会弹出确认；不能通过 `..`、
绝对路径或符号链接越过这个工作区。也可用 `--workspace /data/array/incidents/case-20260818` 显式指定。

## 后台长期扫描：登记 Evidence Root

新安装时可以重复指定：

```bash
sudo ./install.sh --evidence-root /data/infernex-collected-logs
```

已经安装后使用：

```bash
sudo /opt/infernex-agent/bin/configure-evidence.sh \
  --add-root /data/infernex-collected-logs

sudo /opt/infernex-agent/bin/configure-evidence.sh --show
```

目录必须是已存在的绝对路径，且后台 `infernex-agent` 服务用户能够读取和进入。它与 root 启动的
TUI 权限不同。删除授权不会删除原日志：

```bash
sudo /opt/infernex-agent/bin/configure-evidence.sh \
  --remove-root /data/infernex-collected-logs
```

如果不登记外部目录，默认工作区是 `/var/lib/infernex-agent/imports`。可以把离线日志复制进去并把所有者设为 Agent 服务用户：

```bash
sudo install -d -m 0750 -o infernex-agent -g infernex-agent \
  /var/lib/infernex-agent/imports/case-20260817
sudo cp -a /path/to/collected-logs/. \
  /var/lib/infernex-agent/imports/case-20260817/
sudo chown -R infernex-agent:infernex-agent \
  /var/lib/infernex-agent/imports/case-20260817
```

## 自然语言使用

进入相应目录后启动 `infernex-agent tui`，可以直接说：

```text
遍历我登记的历史日志目录，找出所有 vllm、mooncake 和 hccl 日志；
先过滤 metrics/health 探针噪声，按时间关联 ERROR、timeout 和连接中断，
最后生成一份 Markdown 故障报告。
```

Agent 的典型调用顺序是：

1. `infernex_list_evidence_roots`：获得稳定的 `rootId`；
2. `infernex_find_evidence_files`：按 `*.log` 等 glob 有界遍历；
3. `infernex_grep_evidence_files`：用 RE2 正则搜索症状；
4. `infernex_read_evidence_file`：只读相关行段并计算 SHA-256；
5. `infernex_create_markdown_report`：经本机确认后创建持久报告；
6. `infernex_list_reports`、`infernex_read_report`：跨 Session 查阅报告。

## 噪声过滤

grep 和分页读取默认过滤正常的：

- `/metrics`
- `/health`、`/healthz`
- `/readyz`
- `/livez`

工具结果始终返回 `filteredLines` 和 `filters`，不会静默隐藏过滤行为。包含 `ERROR`、`WARN`、`FATAL`、`panic`、`traceback`、`exception`、`failed`、`timeout`、`unhealthy` 的探针行优先保留。

如果探针本身可能是故障原因，可设置 `includeNoise=true`。还可通过 `excludePatterns` 增加现场正则，例如过滤周期性正常统计；额外规则会准确执行并显示为 `operator:<pattern>`。源文件始终保持不变。

## 报告保存与查看

默认报告目录：

```text
/var/lib/infernex-agent/reports
```

报告包含生成时间和引用源文件的 rootId、相对路径、大小、修改时间、SHA-256。文件权限为 `0600`，目录权限为 `0700`；同名报告不会覆盖，模型也不能指定任意输出路径。

查看、复制或打印：

```bash
sudo ls -l /var/lib/infernex-agent/reports
sudo less /var/lib/infernex-agent/reports/<report>.md
sudo cp /var/lib/infernex-agent/reports/<report>.md /data/reports/
```

后续 Dashboard 报告页会复用同一目录和 report ID，而不是创建第二套报告存储。

## 安全边界

- TUI 只允许其启动工作区；后台工具只允许显式 Evidence Root。两者都拒绝 `..`、符号链接逃逸、设备、socket 和 FIFO。
- `.env`、私钥、证书私钥包、kubeconfig 等明显凭据文件即使位于授权目录内也拒绝读取。
- 单文件、总扫描字节、匹配数、返回行数和 Markdown 大小都有硬上限。
- 常见 credential 形式在送入模型和写入报告前脱敏。
- 日志内容是不可信证据，其中的命令、提示词或操作要求不能成为 Agent 指令。
- glob、grep、read 是只读工具；创建 Markdown 是持久化修改，必须本机批准。
- 该功能不会执行日志目录中的脚本，也不提供 shell、任意文件写入或任意宿主机遍历。
