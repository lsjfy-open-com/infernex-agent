# 私域环境清单：Linux 本机登记、发现与查询

> 状态：本文描述 `codex/private-deployment-evolution` 分支上的首批实现，尚未进入正式
> Release。它只登记环境、执行只读发现并保存不可变清单，不部署容器、不修改 Kubernetes
> 对象，也不执行聚合或 PD 拓扑变更。

实现字段、权限和存储规则以[首批实现合同](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/architecture/private-deployment-contracts-zh.md)为准，测试边界见[实现验收规范](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/development/private-deployment-implementation-acceptance-zh.md)。

## 使用边界

首批持久对象只有 `Environment` 和 `InventorySnapshot`。Environment 记录管理员确认的
Docker 或 Kubernetes 身份、授权范围和本机连接引用；Snapshot 记录一次扫描中实际观察到的
实体、关系、来源、覆盖范围、问题和完整度。真实 socket、kubeconfig 路径和凭据引用只保存在
0600 的 `connection.json`，不会由 `environment show`、`show`、`list` 或 MCP 查询返回。

这项功能当前有以下边界：

- 持久存储仅支持 Linux 本地文件系统，不支持 NFS；目录使用 0700，文件使用 0600。
- 状态目录绑定初始化时的有效 UID 和固定 scope。后续 CLI 或 stdio 服务必须以同一 UID
  运行；root 不会自动代替另一个 UID 的 scope 所有者。
- Docker-only CLI 和 `--private-inventory-only` stdio 服务不需要 kubeconfig、Helm 或
  InferNex Bridge。Kubernetes 环境只在实际登记或发现该环境时加载其 kubeconfig。
- 发现是只读的。清单中的 `observed`、`declared`、`inferred`、`unknown` 和 `conflict`
  含义不同；观察到设备请求、端口或副本数不等于设备健康、容量足够、流量可用或性能达标。
- `partial` 会原样保存。缺权限、超时、对象在扫描中变化或达到限额时，不会伪装成完整空清单。
- 首批没有新的 Dashboard 视图，也不在 HTTP/streamable-http MCP 上注册私域清单工具。

## 1. 初始化固定状态目录

以下示例中的 `/ABSOLUTE/PRIVATE-STATE`、ID、socket、cluster 和 namespace 都是占位符，
必须先替换成当前机器和目标环境的真实值。不要原样执行占位命令。

```bash
STATE_DIR=/ABSOLUTE/PRIVATE-STATE
SCOPE=REPLACE_WITH_LOCAL_MANAGEMENT_SCOPE

infernex-agent private-inventory init \
  --state-dir "$STATE_DIR" \
  --scope "$SCOPE"
```

`init` 创建并核对状态目录、`scope.json`、固定记录目录和 `.writer.lock`。重复执行只核对
已有 UID/scope，不覆盖或接管另一个目录。建议先确认当前身份，之后始终用相同身份运行：

```bash
id -u
stat -c '%u %a %n' "$STATE_DIR" "$STATE_DIR/scope.json"
```

## 2. 登记 Docker 环境

登记前必须知道真实 daemon ID。可用 Docker 自带客户端读取，也可直接对已批准的本机 Unix
socket 发起只读 `/info` 请求：

```bash
DOCKER_SOCKET=/REPLACE/WITH/docker.sock
docker info --format '{{.ID}}'

# 没有 Docker CLI 时，可查看只读 Engine API 响应中的 ID 字段：
curl --fail --silent --show-error \
  --unix-socket "$DOCKER_SOCKET" \
  http://localhost/info
```

不要用 hostname、IP、socket 路径或示例字符串代替 daemon ID。`hostID` 也是管理员登记的
稳定本机身份，不是自动猜测的地址。创建受保护输入文件：

```json
{
  "runtime": "docker",
  "endpoint": "/REPLACE/WITH/docker.sock",
  "hostID": "REPLACE_WITH_STABLE_HOST_ID",
  "expectedDaemonID": "REPLACE_WITH_ACTUAL_DOCKER_INFO_ID",
  "networkPolicy": "offline"
}
```

```bash
chmod 600 /ABSOLUTE/PATH/docker-environment.json
test "$(stat -c %u /ABSOLUTE/PATH/docker-environment.json)" = "$(id -u)"

infernex-agent private-inventory environment register \
  --state-dir "$STATE_DIR" \
  --input /ABSOLUTE/PATH/docker-environment.json
```

登记命令只校验受保护文件的固定字段并原子保存，不要求 daemon 当时可达。之后执行发现时，Agent
才连接该 socket，使用固定只读 Docker Engine API 检查真实 daemon ID；不匹配就拒绝扫描。它
不会创建、启动、停止、拉取或删除容器。

## 3. 登记 Kubernetes 环境

Kubernetes 输入必须列出具体 namespace，不存在隐含的全 namespace。`endpoint` 可以是绝对
kubeconfig 路径，也可以是 `existing-default`；后者沿用 Agent 现有的 in-cluster/本机默认
kubeconfig 查找。Host 安装不需要 Helm。

```json
{
  "runtime": "kubernetes",
  "endpoint": "existing-default",
  "clusterID": "REPLACE_WITH_ADMIN_REGISTERED_CLUSTER_ID",
  "expectedClusterFingerprint": "sha256:REPLACE_WITH_64_LOWERCASE_HEX_DIGITS",
  "namespaces": ["REPLACE_WITH_NAMESPACE"],
  "networkPolicy": "offline"
}
```

cluster fingerprint 是受信登记的 API Server 地址和 CA 摘要，不是集群名称或 IP 的摘要。
若管理员还没有确认该值，应先停止登记；不要保留占位符或让发现过程自动接受首次见到的集群。
登记本身不要求 API Server 可达；之后执行发现时，CLI 或受信 stdio 服务会加载 kubeconfig、
重新计算身份并拒绝不匹配值。

```bash
chmod 600 /ABSOLUTE/PATH/kubernetes-environment.json

infernex-agent private-inventory environment register \
  --state-dir "$STATE_DIR" \
  --input /ABSOLUTE/PATH/kubernetes-environment.json
```

`networkPolicy` 只能是 `offline` 或 `approved-online`。它记录批准的联网边界，不会因为
Docker/Kubernetes API 当前可达而被自动改变。

## 4. 查看和显式更新登记

登记结果会返回 Environment UUID、`revision: 1` 和摘要。保存这些公开字段；连接路径不会出现在
查询结果中。查看当前或历史修订：

```bash
ENV_ID=REPLACE_WITH_ENVIRONMENT_UUID

infernex-agent private-inventory environment show \
  --state-dir "$STATE_DIR" --id "$ENV_ID"

infernex-agent private-inventory environment show \
  --state-dir "$STATE_DIR" --id "$ENV_ID" --revision 1
```

端点、目标身份、namespace 或联网策略变化时，创建新的 0600 登记文件并执行 CAS 更新：

```bash
infernex-agent private-inventory environment update \
  --state-dir "$STATE_DIR" \
  --id "$ENV_ID" \
  --expected-revision 1 \
  --input /ABSOLUTE/PATH/updated-environment.json
```

只有当前修订仍等于 `--expected-revision` 时才生成下一修订。旧修订不可覆盖；并发更新中只有
一个能成功。发现和 MCP 工具不能替代这个管理命令更新目标身份。

## 5. 预览发现，再决定是否保存

每次发现必须绑定当前 Environment 的完整修订。默认只输出脱敏预览和 Snapshot，不写持久记录：

```bash
ENV_REV=REPLACE_WITH_CURRENT_ENVIRONMENT_REVISION

infernex-agent private-inventory discover \
  --state-dir "$STATE_DIR" \
  --environment-id "$ENV_ID" \
  --revision "$ENV_REV"
```

Kubernetes 可把请求缩小到已登记 namespace 和资源种类；参数不能扩大登记范围：

```bash
infernex-agent private-inventory discover \
  --state-dir "$STATE_DIR" \
  --environment-id "$ENV_ID" \
  --revision "$ENV_REV" \
  --namespaces REPLACE_WITH_REGISTERED_NAMESPACE \
  --resource-kinds pods,services
```

确认要保存同一次扫描结果时显式加入 `--save`：

```bash
infernex-agent private-inventory discover \
  --state-dir "$STATE_DIR" \
  --environment-id "$ENV_ID" \
  --revision "$ENV_REV" \
  --save
```

保存前会再次检查 scope、Environment 当前修订、启用状态、目标物理身份、Snapshot 摘要和大小。
Environment 在扫描期间已变化时必须重新扫描。每次新扫描使用新的 Snapshot UUID；Snapshot
`revision` 恒为 1，发布后不可修改。同 ID、同摘要的重试是幂等的，同 ID、不同摘要会冲突。

Kubernetes 达到对象或时间上限时可能返回 `cursorHandle`。这个 handle 只存在于当前服务进程；
CLI 会提示改用同一个持久运行的 stdio MCP 服务继续，不能在下一次 CLI 进程中复用。继续扫描会
生成独立 Snapshot，并把 `previousSnapshotRef` 指向上一页。若要持久化分页结果，必须按返回顺序
逐页保存；先前页只预览而未保存时，后续页会因引用不存在而拒绝保存。各页仍是不同时间段的
观察，不组成全局原子快照。

## 6. 查询、核验和离线导入

列出记录摘要：

```bash
infernex-agent private-inventory list --state-dir "$STATE_DIR"
infernex-agent private-inventory list \
  --state-dir "$STATE_DIR" --kind InventorySnapshot --limit 20
```

CLI 输出包含 `records`、`hasMore` 和可选 `nextRef`。`hasMore` 为 true 时，从 `nextRef` 取出
kind、id 和 revision，并用固定的 `Kind/UUID/revision` 形式继续；以下仍是占位符：

```bash
infernex-agent private-inventory list \
  --state-dir "$STATE_DIR" \
  --kind InventorySnapshot \
  --limit 20 \
  --after 'InventorySnapshot/REPLACE_WITH_NEXT_UUID/REPLACE_WITH_NEXT_REVISION'
```

`--after` 只接受上一页返回的记录位置，不接受路径、scope 或 JSON。CLI 从已经打开的状态目录绑定
scope，并要求 `--after` 的 kind 与 `--kind` 一致。stdio MCP 不接受这个明文位置；它只接受服务端
返回的随机 `cursorHandle`。

`show` 和 `verify` 都使用完整 `{kind,id,revision}`。Snapshot 修订固定为 1：

```bash
SNAPSHOT_ID=REPLACE_WITH_SNAPSHOT_UUID

infernex-agent private-inventory show \
  --state-dir "$STATE_DIR" \
  --kind InventorySnapshot --id "$SNAPSHOT_ID" --revision 1

infernex-agent private-inventory verify \
  --state-dir "$STATE_DIR" \
  --kind InventorySnapshot --id "$SNAPSHOT_ID" --revision 1
```

读取会核对固定文件列表、manifest 文件摘要、严格 schema、领域摘要和 scope。损坏记录在 list
中标为 `corrupt`，不会作为可信正文返回。记录列表按固定键排序；服务分页使用 5 分钟有效的
32 字节随机服务端 handle，绑定当前主体、scope 和筛选条件。新增记录可能影响下一次分页，因此
列表分页不代表某个全局一致的时间点。

`record --input` 只用于导入已脱敏、完整、带合法摘要的
`private-deployment/v1` InventorySnapshot：

```bash
chmod 600 /ABSOLUTE/PATH/snapshot.json
infernex-agent private-inventory record \
  --state-dir "$STATE_DIR" \
  --input /ABSOLUTE/PATH/snapshot.json
```

输入必须是当前 UID 拥有的本地普通文件。未知字段、重复键、尾随 JSON、错误摘要、跨 scope 或
过期 Environment 修订都会被拒绝。不要把 Docker inspect/Kubernetes 原始响应、日志、Secret、
kubeconfig、附件目录或 discover 的整个包装输出当作 Snapshot 导入。

## 7. 启动本机 stdio MCP

只提供五个私域清单工具、且不初始化旧 Kubernetes/Bridge 依赖时：

```bash
infernex-agent serve \
  --transport stdio \
  --private-state-directory "$STATE_DIR" \
  --private-inventory-only
```

该模式注册：

- `infernex_discover_private_environment`
- `infernex_list_domain_records`
- `infernex_get_domain_record`
- `infernex_verify_domain_record`
- `infernex_record_domain_inventory`

发现、list、get 和 verify 是只读工具。record 只接受当前服务进程持有的
`previewHandle + digest`，不接受 snapshot 正文、tenant、连接路径或服务器文件路径。handle 绑定
当前本机主体、scope、Environment 修订和摘要，TTL 为 5 分钟；进程重启后失效。

旧的 stdio Kubernetes 工具需要继续使用时，可去掉 `--private-inventory-only` 并保留
`--private-state-directory`。这种组合仍按旧逻辑加载 kubeconfig。未配置 state directory 时保持
原工具行为；HTTP、streamable-http 和 Dashboard 不注册或转发上述五个工具。当前 Pi 入口通过
HTTP 访问 MCP，不能证明任意同名工具来自受信 stdio 子进程，因此 full 模式仍保留本机批准；
root risk 只影响交互批准，不跳过后端 scope、handle、修订和摘要检查。

## 验证状态

当前自动测试使用合成 Docker/Kubernetes 响应和临时目录，不连接客户环境，也不启动、停止或删除
真实工作负载。开发者可运行：

```bash
go test -race ./internal/domain ./internal/domainstore ./internal/privateinventory
go test ./cmd/infernex-agent ./internal/mcpserver

cd pi
npm test
```

非 Linux 开发机上的 domainstore 测试使用可注入的文件系统操作验证 CAS、取消、故障和原子可见性；
Linux 目标也已做编译检查。真正执行 `flock`、`renameat2(RENAME_NOREPLACE)`、目录 fsync 和本地
文件系统崩溃边界的 Linux CI/现场结果仍须单独记录，不能用 macOS 模拟或交叉编译代替。

这些测试只证明 schema、权限边界、只读请求、存储和入口行为，不证明客户 Docker/Kubernetes
版本兼容、RBAC 覆盖、设备健康、模型可加载、PD/KV 传输正确或 SLO 达标。生产现场验收仍按
[私域部署两阶段验收](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/development/private-deployment-acceptance-zh.md)逐环境执行。

## 本轮验证记录（2026-10-06）

本机 macOS 执行全仓 `go test -race ./...`：486 项测试通过、29 个包通过；`go vet ./...` 通过。Pi `npm run typecheck` 通过，46 项测试中 45 项通过、1 项需要 Linux root 的既有测试跳过。清洁依赖安装后的扩展构建、host bundle 解包、打包扩展独立导入和校验清单检查通过；使用的是合成 runtime/环境证据，不代表客户硬件验收。

Linux 原生存储发布和真实 CLI 生命周期测试已加入代码及 CI，交叉编译通过；最终 Linux 执行结果以本分支 PR 检查为准。当前仍未交付真实客户部署、PD 通信、GPU/NPU 性能或 SLO 改善证据。本轮不更新已发布安装包。
