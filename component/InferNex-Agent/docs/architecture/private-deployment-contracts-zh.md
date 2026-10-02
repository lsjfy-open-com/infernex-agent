# 私域环境发现：首批实现合同

状态：2026-10-02，供编码实施的 v1 合同，尚未实现。本文细化[总体设计](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/architecture/private-deployment-evolution-zh.md)，只冻结登记、只读发现、清单与本地记录。部署写入和能力自演进仍按后续门禁交付。实现与本文冲突时，须同时评审合同和测试的变更。

## 1. 模型先落到两个持久对象

不一次实现所有概念对象。首批只持久化 `Environment` 和 `InventorySnapshot`；组件、资源、配置来源、管理归属、拓扑声明与证据作为快照中的类型化实体。后续拆出独立记录时通过新 schema 和显式转换迁移，不让一个通用 map 代替领域模型。

| 公共字段 | v1 约束 |
| --- | --- |
| `schemaVersion` | 固定 `private-deployment/v1`；未知版本拒绝 |
| `kind` | 仅 `Environment`、`InventorySnapshot`；按 kind 严格解码 |
| `tenantScope` | 登记的本地管理域 ID；来自受信运行配置/已认证主体，不能由模型自由指定 |
| `id` | Environment 为登记时产生的 UUID；Snapshot 为每次扫描产生的新 UUID；不用路径或 IP 作 ID |
| `revision` | 正整数。Environment 修订递增，以 expectedRevision 做比较并更新；Snapshot 恒为 1，不可修改 |
| `createdAt` | RFC 3339 UTC，固定纳秒格式；生成前规范化，已签摘要记录读取时不能改写 |
| `digest` | `sha256:` 加 64 位小写十六进制；计算规则见第 2 节 |
| `spec` | 按 kind 定义的结构体，所有层级拒绝未知字段、重复键和尾随 JSON；不允许自由扩展执行参数 |

完整 RecordRef 为 `{tenantScope, kind, id, revision}`。Environment 的 spec 包含 `runtime`（docker/kubernetes）、`endpointRef`、`credentialRef`（可缺省）、`identityFingerprint`、`allowedNamespaces`、`networkPolicy`（offline/approved-online）、`enabled`。端点/凭据引用只在本机受保护配置中解析；工具不能提交 socket 路径、URL 或 kubeconfig 内容。指纹是登记所确认的目标身份，不是单纯 hostname；指纹变化使发现失效，不能自动接管新目标。

Snapshot 的 spec 包含完整 `environmentRef`、`collectorVersion`、`rulesetVersion`、`startedAt`、`finishedAt`、`coverage[]`、`entities[]`、`relations[]`、`issues[]`、`completeness`。Environment 在一次扫描中固定修订；保存前若登记修订或授权已变，则返回 conflict，需重新扫描。

| 内嵌结构 | 必需字段和语义 |
| --- | --- |
| Entity | `entityId`、`entityKind`、`identity`、`facts[]`；kind 为 host/node/container/workload/component/configuration/model-reference/endpoint/topology-declaration/ownership |
| Fact | `field`、白名单类型化 `value`（未知时省略）、`status`、`observedAt`、`source`；status 为 observed/declared/inferred/unknown/conflict |
| Source | 采集器、已登记目标引用、源对象 UID/ID 与版本、字段路径；不保存任意原始响应正文 |
| Relation | `type`、`fromEntityId`、`toEntityId`、证据引用和状态；首批仅同一快照内部关系 |
| Coverage | 请求的资源种类/namespace、读取数量、结束状态、截断原因；缺权限不是数量零 |
| Issue | `code`、`stage`、受影响对象引用、`retryable`、有界脱敏说明；不带原始服务端报错正文 |

实体稳定身份采用总体设计的 K8s UID、Docker container ID 等规则；`entityId` 为这些规范身份字段的摘要。引用具体观察时用 `{snapshotRef, entityId}`，不能仅用 entityId 暗指“最新”。同名重建是新实体；同一实体跨扫描变化保留多次观察。首批不自动拼接跨环境 PD 拓扑，只展示声明和待确认关联。

配置、模型和引擎规则只解析白名单字段，原始命令、环境变量值、任意 labels 和日志均不落清单。未知参数记录键名/源位置和 unknown；可能敏感的键名也脱敏。权重只记已授权的挂载引用及版本声明，不扫描文件或读取权重。设备请求能被观察，不代表设备健康或剩余显存已知。管理标签只能成为 declared 归属；获得管理工具元数据及对象关系证据后才成为 observed，仍不等于获得写权限。

## 2. 规范化、校验和限额

摘要算法名固定 `pd-json-v1`：UTF-8，无 BOM、空白或尾随换行；对象键按 UTF-8 字节序排列；数组保留顺序；缺失与 null 不同。字符串不做 Unicode 归一化，仅转义双引号、反斜杠及 U+0000–001F（统一小写 `\u00xx`）；其他字符直接写 UTF-8，包括 HTML 字符和 U+2028/U+2029。数值仅允许十进制整数，范围 ±9007199254740991，零写为 `0`；拒绝小数、指数形式、NaN、重复键和无效 Unicode。计量值用规范单位字符串。只排除根级 `digest`，嵌套字段不能借同名规则被排除。

采集器在摘要前按 entityId、关系的 type/from/to 和 coverage 的 kind/namespace 排序；上述键重复一律拒绝。facts 按 field/source 对象规范字节/status 排序，同键多种值保留为 conflict 并用规范 value 字节打破平局；完全重复项合并。issues 按 code/stage/对象引用/说明规范字节排序，完全重复项合并。具有业务顺序的数组保持原序。entityId 为 eid:sha256: 加 identity 对象的 pd-json-v1 摘要，不包含 facts 或时间。先校验/脱敏、再计算摘要；读取时重新算摘要并检查格式，不能把读时“修好”的记录当原记录。PR1 必须提供中文、控制字符、HTML、空值、整数边界和键乱序的黄金字节及摘要向量；不能直接假设标准库默认 JSON 输出等于此合同。

| 上限 | 默认及硬边界 |
| --- | --- |
| 扫描时长 | 默认及首批最大 30 秒；每次请求继承同一 context deadline |
| 扫描实体 | 最多 300；超限为 partial，明确未覆盖范围 |
| Docker 原始响应 / K8s 投影页 | 2 MiB。Docker 在读取阶段限制；K8s 对 Reader 返回后的白名单投影 JSON 限制，不宣称限制 client-go 解码前的原始流；原文不持久化 |
| 单规范实体 / 单快照 | 64 KiB / 8 MiB（UTF-8 字节）；单实体超限省略并记 issue，快照无法满足上限则不发布 |
| 每实体 facts / 每快照 relations | 128 / 1200；超限显式 partial |
| 普通字符串 / issues | 4096 字节 / 最多 100 条、每条说明最多 512 字节；另记录 omittedIssueCount |
| 工具分页 | 默认 20、最多 100 项；整个 JSON 响应最多 256 KiB，优先减少条数，禁止破坏 JSON 的截断 |
| 预览缓存 | 同主体最多 4 个、全服务最多 16 个快照，每个 8 MiB，TTL 5 分钟；满时拒绝新预览，不驱逐正在保存的项 |

取消返回 canceled，不交付可保存预览。超时可返回已完成部分的 partial 预览，必须标明 timeout；全环境无法访问为 failed，不能构造 complete 空清单。只有请求范围内所有读取完整结束才是 complete，它也不意味着整个集群被穷尽、全局原子一致或业务健康。

## 3. 发现与权限边界

拟议接口位于 `internal/adapters`，不暴露任意 shell 或 runtime 写 API：

```go
type Discoverer interface {
    Discover(ctx context.Context, env domain.Environment,
        req DiscoverRequest) (domain.InventorySnapshot, error)
}
// req 只有已授权资源种类/namespace 子集；客户端不能扩张 env 的范围。
// Principal 由入口认证/本机运行配置解析，在调用 Discover 前检查。
```

Docker 首版选标准库 `net/http` 的窄 Engine API client，连接显式登记的本机 Unix socket，不新增 Docker SDK，不使用 CLI/shell 双路径。只允许 `GET /version`、`GET /info`、`GET /containers/json?all=1` 和对刚列出的合法 ID 执行 `GET /containers/{id}/json`。首版固定使用 API v1.44：先 GET /version，确认服务端 MinAPIVersion ≤ 1.44 ≤ ApiVersion，再给后三类路径加 /v1.44 前缀。版本缺失、格式错误或无交集返回 unsupported；不能选择服务端最高版本或受 DOCKER_API_VERSION 环境变量影响。/info.ID 必须非空并匹配登记的 daemonID，hostID 来自本机受信登记；身份指纹是规范 {runtime,hostID,daemonID} 的 pd-json-v1 摘要。登记后的 socket 路径不作为身份。参考 [v1.44 API](https://docs.docker.com/reference/api/engine/version/v1.44/)。禁用重定向、代理环境变量和任意 URL；无 exec、logs、pull、create、start、stop、remove。显式 TLS 远端、多主机 SSH 为后续适配增量，不能默认开放明文 TCP。API 版本协商依据 [Docker Engine API 官方说明](https://docs.docker.com/reference/api/engine/)，上线声明须列出实际测试的版本，不能宣称所有 daemon 兼容。

Docker list 没有可依赖的通用服务端分页合同：超限明确 partial，用户缩小登记范围或提高未来版本限额后重扫，不能伪造 continue token。读取 inspect 后投影白名单字段，原始 Env/认证信息立即丢弃，不进入调试日志或模型上下文。daemon 身份不一致、容器在 list/inspect 间消失或重建分别产生 identity_changed / not_found / conflict。

K8s 复用 `internal/kubeops` 的窄只读接口和现有身份配置，不导入 Bridge 控制器。首批默认读取指定 namespace 中的 Deployment、StatefulSet、Pod、Service、EndpointSlice 与 PVC 元数据，以及获授权的 Node 摘要；不读 Secret 正文、Helm release Secret 或任意 CRD 实例。首次扫描在 30 秒/300 实体内消费分页；超出预算保留 partial。继续扫描 token 用 32 字节加密随机 server-side handle 封装，绑定主体、环境修订、GVR/namespace/selectors；TTL 5 分钟，同主体最多 4 个、服务最多 16 个，重启失效、过期返回 cursor_expired，不能自动改成新的一页。继续扫描生成独立 Snapshot，并保留前一快照引用；不拼接为跨扫描原子清单。记录查询分页在不可变快照上另用同样受限 handle。部分 RBAC、API 不存在、超时分别记 forbidden/unsupported/timeout；缺失 Node 权限不把资源预算写成 0。分页与资源版本语义参考 [Kubernetes API 官方说明](https://kubernetes.io/docs/reference/using-api/api-concepts/)，continue 过期不能拼接成完整一致结果。

K8s/Docker API 请求通过受限客户端发出，不依赖模型判定其是否只读。模型可以解释事实或提出待核对关联，不能把 inferred 改为 observed，也不能因客户文档中的指令加载新工具。知识检索、规则生成和能力包晋级接口不在首批注册。

## 4. 保存、入口与兼容

`internal/domainstore` 使用独立、显式配置的私域状态根目录，复用 `configversion` 的安全写入模式而不改写其旧记录。目录 0700、文件 0600；首次建立后校验拥有者及路径，拒绝不可信 symlink/非普通文件。首版支持 Linux 本地文件系统，使用根目录固定 .writer.lock（0600、禁止 symlink）上的 flock 排他锁，按写事务取得，最多等 5 秒且可取消；服务与 CLI 共用该锁，持锁范围覆盖读取当前 revision、校验、写临时文件、发布及父目录 fsync。不能使用 NFS 状态目录。生产多主体访问必须经已认证入口；本机 CLI 使用运行身份固定的 scope。

保存顺序为：作用域/环境修订再校验 → 严格校验与摘要 → 同文件系统私有 pending 目录 → 写记录并 fsync → 写清单并 fsync → 持锁核对目标不存在后发布 rename（Linux renameat2 RENAME_NOREPLACE；不支持时返回 unsupported，不退化为覆盖式 os.Rename） → fsync 父目录 → 返回 RecordRef。只有最终目录可被 list/get 看见。首批无附件、无自动删除、无保留期清理工具；重启忽略 pending，显式维护仅清理本进程拥有且不在写入的 pending。

同 Snapshot ID+digest 重复保存为幂等成功；同 ID 不同 digest 为 conflict。Environment 新 revision 须 CAS 校验旧 revision；记录不可覆盖。磁盘配额不足或 sync 失败返回 storage_error，发布后响应丢失可按 ID 查询，不能改 ID 悄悄重写。写入前取消不发布；一旦进入原子发布点，取消不能承诺“未写”，返回 committed 或 outcome_unknown，调用者通过 ID 对账。list 的索引可重建，索引不是完整性或授权依据；损坏记录显示 corrupt，不作为可信内容返回。

CLI 合同：`infernex-agent private-inventory discover|record|list|show|verify`。discover 默认仅输出脱敏预览，`--save` 明确保存；record `--input` 仅从用户指定普通文件严格导入相同 v1 脱敏快照，不允许将原始 inspect 当包导入；list/show/verify 无写入。环境登记由受保护配置及管理导入流程提供，首批不向模型开放登记或更换目标工具。JSON 写 stdout，诊断写 stderr；验证/权限/存储失败非零退出。

MCP 工具：`infernex_discover_private_environment`、`infernex_list_domain_records`、`infernex_get_domain_record`、`infernex_verify_domain_record` 为 readOnly；`infernex_record_domain_inventory` 为显式本地非破坏写入。保存参数只有 preview handle+digest，必须匹配当前认证主体、环境修订和 TTL，不接受模型拼装正文或服务器文件路径。manual 保留确认；full 按 local-record 低风险规则执行并审计；root risk 也必须通过作用域/完整性检查，且不会成为 MCP/Dashboard 的远端认证方式。服务侧授权与 UI 是否弹窗分别测试。

新增模式必须允许 `private-inventory` 和只启用 Docker 的 stdio 服务在完全没有 kubeconfig 时启动。当前 `serveAgent` 先建立 Kubernetes 配置，PR3 必须把客户端构建移入启用的环境适配器分支；保留原 K8s 默认配置查找与旧工具行为，不把“可选 K8s”改成忽略所有 K8s 配置错误。旧 Bridge 工具仅在其原有依赖可用时注册；新领域包不能通过 nil 的旧 observer 勉强启动。

## 5. 登记与入口的具体形式

首批采用**单本机主体**，不宣称已有多租户服务认证。`private-inventory init --state-dir ABS --scope ID` 创建 0700 状态目录和固定拥有者 UID/scope 的 `scope.json`；已存在时只核对，不覆盖。CLI 和 stdio 子进程必须以该 UID 运行（root 也不自动代替另一个 scope 的拥有者），工具请求没有可改 tenant 的参数。跨 scope 的负例是在核心接口注入不同 Principal 测试，不能据此宣称多租户产品已交付。

新增管理子命令 `private-inventory environment register --input FILE`、`environment update --id UUID --expected-revision N --input FILE` 和 `environment show --id UUID`。register 的输入只有以下固定字段，路径指向本机受保护普通文件；首次登记生成 UUID/revision=1。update 必须 CAS，端点或身份变化显示差异并要求该显式管理命令，发现工具不能调用它。CLI 的 state-dir 可由显式 flag 或固定本地配置提供，不扫描任意目录。

| 登记输入字段 | 约束与用途 |
| --- | --- |
| `runtime` | docker 或 kubernetes |
| `endpoint` | docker 仅绝对 Unix socket 路径；kubernetes 为本机 kubeconfig 路径或明确的 existing-default，后者复用现有查找逻辑 |
| `hostID` / `clusterID` | docker 必填 hostID；kubernetes 必填管理员登记 clusterID。缺失不以 IP 代填 |
| `expectedDaemonID` / `expectedClusterFingerprint` | docker 必填 /info.ID；K8s 为受信登记的 API 端点+CA 摘要。登记者提供并核对；缺失先用现有本机工具确认，不能信任发现自动接受 |
| `namespaces` | K8s 必填非空具体 namespace 列表，无隐含全 namespace；Docker 必须省略 |
| `networkPolicy` | offline 或 approved-online；不因 runtime API 可达而改为联网 |

连接配置存于对应 Environment 修订的私有目录 `records/Environment/<environment-uuid>/<revision>/connection.json`（0600）；公开 Environment 的 endpointRef/credentialRef 均只用 `{id: UUID}`，不放真实路径或密钥。Environment 的 identityFingerprint、spec.hostID/spec.clusterID 和 runtime 对应身份字段来自登记配置；启用状态初始为 true。`credentialRef` 指向本机既有凭据引用；不接受命令行口令、内联 kubeconfig 或远端 URL 注入。Environment 的最终目录固定包含 record.json、connection.json、manifest.json；三者同一 pending 目录一起发布，manifest 校验两文件的字节摘要，连接文件不经 list/get/MCP 返回。endpointRef.id 指向本 Environment UUID，解析使用调用绑定的 Environment revision；K8s credentialRef 同理，Docker 无需该字段。Snapshot 最终目录只有 record.json、manifest.json，任何额外文件均拒绝。这样登记连接和 Environment 修订原子发布，不先改全局连接文件。

MCP 首批只在 `--transport stdio --private-state-directory ABS` 注册新清单工具；`--private-inventory-only` 使用新工具集合，不创建旧 K8s/Bridge observer。旧 stdio K8s 模式可显式增加该 state-dir 注册新工具。未配置 state-dir 保持旧行为；HTTP/streamable-http/Dashboard 首批不注册新清单工具，不接受未经认证的 scope。远端入口在 PR4 前须另交付主体认证、ACL 和 handle 隔离合同，不把监听 localhost 当认证。

PR3 同步修改 `pi/host-tools.ts` 的受信 `autonomousLocalTools` 名单，仅纳入精确名称 `infernex_record_domain_inventory`，且调用当前受信 stdio server 的实现；不能相信任意 MCP server 的同名工具。新增 `host-tools.test.ts` 的来源校验、manual/full/risk、拒绝正文/路径及过期 handle 测试。如果当前路由无法证明工具来源，该工具保持审批并阻止宣称 full 自动保存已验收，须先补来源绑定。

## 6. 类型化实体的最小白名单

所有 identity 对象固定包含 `runtime`、`environmentId`、`entityKind`、`nativeId`；Docker 另含 `hostID`、`daemonID`，K8s 另含 `clusterID`、`namespace`（集群对象为空字符串）、`apiVersion`、`resourceKind`、`uid`。容器 nativeId=完整 container ID；K8s nativeId=UID。派生实体的 nativeId=来源实体 ID + `#` + 固定字段路径（非名称猜测），继承来源身份字段。Environment 修订不进入实体身份，Snapshot 的 environmentRef 保留当次修订；重复 entityId 的不同 identity 拒绝。

Fact.value 是严格 tagged union：`{type:"string",stringValue:"..."}`、`{type:"integer",integerValue:1}`、`{type:"boolean",booleanValue:true}` 或 `{type:"string-list",stringListValue:[...]}`，只能有与 type 对应的一个值字段，unknown 必须省略 value。未知 field 拒绝，不把自由文本作为执行参数透传。首批按下表允许，数值单位不能另加浮点值：

| entityKind | field → type（之外拒绝） |
| --- | --- |
| host / node | name→string，cpuMilli→integer，memoryBytes→integer，os→string，architecture→string，deviceRequests→string-list |
| container / workload | name→string，imageRef→string，imageDigest→string，phase→string，replicasDeclared→integer，replicasObserved→integer，nodeRef→string，ports→string-list，mountRefs→string-list，deviceRequests→string-list |
| component | name→string，version→string，imageDigest→string，recognizerVersion→string |
| configuration | sourceRef→string，format→string，templateRevision→string，parameterNames→string-list，redacted→boolean |
| model-reference | mountRef→string，modelRevision→string，tokenizerRevision→string，precision→string |
| endpoint | serviceRef→string，ports→string-list，readyBackends→integer |
| topology-declaration | mode→string，role→string，tpDeclared→integer；mode/role 未经规则验证只能 declared/inferred |
| ownership | manager→string，ownerRef→string，verification→string；不生成可写许可 |

Source 固定字段为 `collector`、`objectRef`（EntityRef 或已登记 Environment RecordRef）、`objectVersion`（未知省略）、`fieldPath`；禁止存储任意 URL 正文。Fact 的 observedAt 使用公共时间格式。Relation.type 首批为 runsOn/managedBy/configuredBy/dependsOn/routesTo/usesModel；关联失败记 issue，不生成悬空关系。KV 传输边和确认过的 PD 关系留给 PR7，不依据 prefill/decode 容器名构造。

Coverage 固定字段：`resourceKind`、`namespace`、`count`、`state`、`reason`（可省略）；state=complete/partial/forbidden/unsupported/timeout/failed；reason 使用 Issue code。Snapshot.completeness=complete/partial/failed；只有无有效实体且所请求源均失败才 failed，有有效实体的缺口为 partial。Issue.code 首批固定为 forbidden/unavailable/timeout/not_found/unsupported/identity_changed/conflict/limit_exceeded/cursor_expired/unparsed/redacted；stage=connect/list/inspect/normalize；对象引用可省略，不包含非法外部 ID。API 入口/存储错误另用 invalid_argument/unauthorized/not_found/conflict/busy/storage_error/canceled/outcome_unknown，不混入发现事实。

实体间关系的引用必须在同一快照存在；外部数据不可信，不递归解析未知引用。新 field、kind、relation 或枚举若要接入，需要同步 schema/fixture/版本兼容评审。PR1 的正式 JSON Schema 或 Go 验证器从本表机械实现，不能自行引入额外可执行字段。

## 7. 不兼容变更如何进入后续版本

v1 未知字段拒绝意味着新字段必须通过 schema 升版或事先声明的兼容可选字段引入，不能静默接受。旧版本数据读取走显式转换，保留原摘要、源版本和转换记录；不能覆盖旧快照。迁移失败保留原数据并报告，不影响旧 Agent 状态目录。首批没有持久化通用 Plan/Change/KnowledgeRecord，因此不会承诺这些后续 schema 已稳定。

开工依赖、后续写操作门禁见[实现顺序与就绪判定](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/development/private-deployment-implementation-plan-zh.md)，逐项测试见[前三个 PR 验收规范](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/development/private-deployment-implementation-acceptance-zh.md)。
