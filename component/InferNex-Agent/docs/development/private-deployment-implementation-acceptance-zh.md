# 私域部署前三个实现 PR 的可执行验收规范

本文把[私域部署架构设计](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/architecture/private-deployment-evolution-zh.md)和[两阶段验收补充](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/development/private-deployment-acceptance-zh.md)拆成前三个可独立合入的实现增量。范围仅包括 `Environment` 登记、`InventorySnapshot`、Docker／Kubernetes 只读发现、持久存储、CLI／MCP 读接口和显式本地记录写入口；不部署容器、不写 Kubernetes 对象、不宣称 PD 或 GPU／NPU 可用。

本文保留编码前冻结的验收合同；2026-10-06 首批实现已进入演进分支，实际测试以各包的 `_test.go` 为准，表中的用例可由等价测试组合覆盖。合同清单本身不是现场验收结果；Linux 原生文件系统测试与客户硬件测试须分别提供证据。实现计划可调整包名，但须在编码前同步修改本页，不能合入后用口头解释替代失败的门禁。

## 1. 现有可复用边界

| 现有实现 | 本轮复用方式 | 不能推导的能力 |
| --- | --- | --- |
| `internal/kubeops` | 复用已有只读 Reader、client-go fake、每页最多 300 项、Secret／敏感字段脱敏和 partial warning 语义 | 不是新的领域模型，不证明 Docker、部署写入或硬件支持 |
| `internal/configversion` | 复用 0700 状态目录、0600 文件、临时目录写入→fsync→rename→父目录 fsync、损坏摘要拒绝和未发布目录不可见的模式 | 现有 ConfigVersion schema 不能冒充新的 DomainRecordStore |
| `internal/diagnosticexec` | 复用固定操作集合、无任意 shell 字符串、30 秒默认超时、stdout 256 KiB 和受限 stderr 的模式 | 只读 Docker 发现必须有独立受限接口，不能向模型开放任意 `docker` 子命令 |

Go 模块现有 `testing`、`httptest`、`client-go` fake discovery/dynamic/metadata/controller-runtime client、`t.TempDir` 和 race detector 足以完成前三个 PR。不得为了这些测试新增 GPU/NPU mock SDK，也不得把伪造设备名、pause 容器或 Docker metadata 当成硬件证明。

当前 `go.mod` 要求 Go 1.24.5，Linux CI 使用 Go 1.25.x 并运行 `go test -race ./...` 与 `go vet ./...`；Pi 已有 Node 原生 test runner 和 TypeScript typecheck。Go 依赖中没有 Docker SDK；PR2 固定使用标准库 `net/http` 实现 Engine API 窄 client，不增加 SDK，也不保留 Docker CLI 生产路径。

## 2. 三个 PR 与拟议文件

| PR | 生产文件（拟议） | 测试与 fixture（拟议） | 合入后新增能力 |
| --- | --- | --- | --- |
| PR1 领域记录 | `internal/domain/types.go`、`reference.go`、`canonical.go`、`validate.go`、`redact.go` | `internal/domain/*_test.go`；`internal/domain/testdata/*.json` | 严格处理 `Environment` 与 `InventorySnapshot` 两种记录；其他领域实体只内嵌于 snapshot；无存储、发现或服务注册 |
| PR2 只读发现 | `internal/adapters/dockerdiscovery/{client,discover,normalize}.go`、`internal/adapters/kubernetesdiscovery/{reader,normalize}.go` | 两包的 `*_test.go` 与 `testdata/`；扩展 `internal/kubeops/reader_test.go` 仅限通用读取回归 | 通过 Docker Engine API 或现有 kubeops Reader 生成带完整度、来源和未知项的 snapshot preview；不持久化 |
| PR3 存储和入口 | `internal/domainstore/{store,filesystem}.go`、`cmd/infernex-agent/private_inventory.go`；扩展 `main.go`、`internal/mcpserver/server.go`、`pi/host-tools.ts` | `internal/domainstore/*_test.go`、`cmd/infernex-agent/private_inventory_test.go`、`internal/mcpserver/server_test.go`、`pi/host-tools.test.ts` | 登记、原子保存、list/get/verify；CLI 与受信 stdio MCP 的发现预览和显式保存入口 |

`internal/domain` 不得直接或间接依赖 Bridge 类型；至少用依赖图检查保证该包不导入 `gitcode.com/openFuyao/InferNex/api/v1alpha1`、`internal/observer`、`internal/deployer`、`internal/remediator` 或 `internal/experiment`。Docker discovery 和 DomainRecordStore 也不得导入这些包。Kubernetes discovery 只依赖一个窄的只读接口和领域类型；Bridge 兼容转换以后另做适配器。

## 3. PR1：领域记录测试矩阵

PR1 只冻结两个顶层 kind：

- `Environment` 是人工登记的接入配置。`revision` 必须是正整数；每次修改使用 compare-and-swap（CAS）从旧 revision 递增，不能覆盖并发修改。
- `InventorySnapshot` 表示一次独立扫描。每次扫描产生新的 UUID `id`，`revision` 恒为 `1`，发布后不可变；再次扫描不能改旧 snapshot 或复用其 ID。

实体的 `entityKind` 只允许合同冻结的小写值：`host`、`node`、`container`、`workload`、`component`、`configuration`、`model-reference`、`endpoint`、`topology-declaration`、`ownership`。Fact、Source、Relation、Coverage 和 Issue 是 snapshot 内嵌结构，不是实体 kind，也不注册成通用顶层记录。文档中的宽泛名称 Component、Configuration、Ownership、ServingTopology、Observation/Evidence 必须按上述 v1 kind/结构映射，不能直接变成额外顶层 kind。顶层完整引用为 `{tenantScope, kind, id, revision}`；实体引用为 `{snapshotRef: <完整顶层引用>, entityId}`。preview 不领取持久 revision，也不通过“下一个 revision”竞争；保存的是同一 preview digest 对应的新不可变 snapshot。

共同头部至少冻结 `schemaVersion`、`kind`、`id`、`revision`、`tenantScope`、`createdAt`、`digest`，snapshot 另绑定完整 `environmentRef`。摘要遵循架构文档的 UTF-8 JSON 规范化规则并排除自身 `digest`。解码器必须拒绝未知字段和尾随 JSON。Environment、Entity identity、Fact tagged union、Source、Relation、Coverage 和 Issue 只接受[实现合同第 5、6 节](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/architecture/private-deployment-contracts-zh.md)冻结的字段、枚举和按 `entityKind` 划分的 Fact.field/type 组合；fixture 不得用自由 map 绕过白名单。

| 测试名 | fixture／输入 | 断言 |
| --- | --- | --- |
| `TestDecodeValidDomainRecords` | `schema-v1-environment.json`、`schema-v1-snapshot-docker-aggregate.json` | 两个 kind 严格解码；只接受固定纳秒格式的 RFC 3339 UTC，读取时不改写时间；重复规范化字节和 digest 完全相同；其他实体只以内嵌形式出现 |
| `TestDecodeRejectsUnknownFieldAndTrailingValue` | `environment-unknown-field.json`；有效 JSON 后拼第二个值 | 返回字段名或 trailing-value 错误，不产生部分记录 |
| `TestVerifyRejectsSchemaAndDigestTamper` | `record-wrong-schema.json`、`record-tampered-digest.json`；修改已摘要字段 | 未支持 schema/kind、摘要不符、空 id、Environment revision≤0、snapshot revision≠1 分别拒绝 |
| `TestCanonicalizationGoldenVectorsAndStrictNumbers` | 中文、控制字符、HTML、U+2028/U+2029、对象键乱序、数组原序、缺失/null、根/嵌套 digest、整数边界 | 黄金字节和 digest 固定；重复键（含嵌套）、无效 Unicode、小数、指数整数如 `1e3` 均拒绝；±9007199254740991 通过、再越 1 拒绝；只排除根级 digest |
| `TestReferencesStayInsideTenantScope` | `refs-same-scope.json`、`refs-cross-tenant.json`、`refs-missing-revision.json` | environment/snapshot 同租户完整引用通过；跨租户、缺 kind/id/revision 拒绝；entity ref 缺 snapshotRef 或 entityId、指向另一 snapshot 的 entity 均拒绝 |
| `TestEmbeddedEntityAndRelationIntegrity` | 重复 entityId、未知 entityKind、relation 指向缺失实体、重复 canonical relation key、重复 coverage key | 全部严格拒绝；不得留下悬空关系或排序 tie；Fact/Source/Coverage/Issue 不能伪装成 Entity |
| `TestTypedEntityWhitelistAndEnums` | 每种 entityKind 的合法 identity 和 Fact.field/value；再逐项加入错 runtime identity、未知 field、错 tagged-union 值字段、未知 relation/status/completeness/issue code | 合同白名单正例通过；字段、类型或枚举任一不匹配都在摘要或持久化前拒绝，不接受任意扩展 map |
| `TestSecretValuesNeverEnterDomainJSON` | snapshot 内嵌 configuration 含 `DO_NOT_LEAK_TOKEN_7E31`、口令、私钥和带凭据 URL | 验证拒绝原始 secret 字段；只允许合同白名单中的脱敏事实和本机受保护引用；序列化输出、错误和 digest 输入回显均不含标记值 |
| `TestEnvironmentAuthorizationScope` | 固定状态目录拥有者 UID/scope；核心接口另注入不同 Principal 或越出已登记 host/cluster 范围 | CLI/stdio 只从受信本机运行身份取得 scope，请求无 tenant 参数；纯 scope matcher 在读取或扫描前拒绝注入的越界主体，且不据此宣称多租户服务已交付 |
| `TestFourCombinationFixturesMapToEmbeddedEntities` | `schema-v1-snapshot-{docker,k8s}-{aggregate,pd}.json` | 四个 fixture 均为 schema v1 snapshot；概念 topology/ownership/component 分别映射为 `topology-declaration`、`ownership`、`component` 实体，观察证据映射为 Fact/Source；fixture 只证明数据形状，不证明框架支持或运行成功 |
| `TestValidationBudgets` | 对每个导出上限构造 `N` 与 `N+1`：单记录字节、关系数、字符串长度、引用数 | `N` 通过，`N+1` 在分配或持久化前拒绝；错误有界且不回显完整输入 |
| `TestDomainHasNoBridgeImports` | 检查 `internal/domain` 的完整 Go dependency graph | 禁止上述 Bridge/旧控制器包直接或间接出现；测试自身也不能用 Bridge fixture |

PR1/PR2 固定上限为：每次扫描最多 300 个内嵌实体；每个 Docker 原始响应最多 2 MiB，Kubernetes 则限制 `kubeops.Reader` 解码后的单个白名单投影页为 2 MiB，不宣称限制 client-go 解码前的传输；单个规范实体最多 64 KiB；完整 snapshot 最多 8 MiB；每实体最多 128 个 facts；每快照最多 1200 个 relations；普通字符串最多 4096 字节；issues 最多 100 条且每条说明最多 512 字节，超出时记录 `omittedIssueCount`；扫描 timeout 30 秒。PR3 工具分页默认 20、最大 100，单次工具响应总计最多 256 KiB，超过时减少完整条目且保持合法 JSON。所有值定义为共享包常量并测试 `N` 与 `N+1`，不能只在 CLI 层限制或靠上下文窗口截断。首批 bundle 不支持附件。

PR1 出口：上述测试全部通过。架构样例[private-deployment-model.json](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/architecture/examples/private-deployment-model.json)是 `design/v0` 评审视图，不要求也不得通过生产 schema v1 解码器；生产测试只读取新增 schema v1 fixtures，不实现任意 example-only 展开器。

fixture 边界：PR1 必须新建 1 个 Docker Environment、1 个 Kubernetes Environment、四组合 snapshot 正例，以及 wrong-schema、unknown-field、duplicate-key、cross-tenant-ref、raw-secret、tampered-digest 负例。2 MiB/64 KiB/8 MiB 与数量 N/N+1 使用测试构造器生成，避免提交超大样例。PR2 的 Engine API JSON 仅是 synthetic HTTP response；client-go fake 仅是 synthetic K8s API。所有 fixture 都必须标记合成来源且使用保留域名/无效凭据，不得复制客户配置。

## 4. PR2：只读发现测试矩阵

Docker client 冻结为标准库 `net/http` 的窄 Engine API client。首批只连接操作者登记的本地 Unix socket：先用未版本化 `GET /version` 检查 `MinAPIVersion ≤ 1.44 ≤ ApiVersion`，再只允许 `GET /v1.44/info`、`GET /v1.44/containers/json?all=1` 和对本次 list 已返回 ID 的 `GET /v1.44/containers/{id}/json`。`/info.ID` 必须非空且匹配登记的 expectedDaemonID，身份指纹使用登记 hostID 与 daemonID；版本无交集、字段缺失或身份变化均 fail closed。接口不得包含 create/start/stop/remove/update，不受 `DOCKER_API_VERSION` 影响，也不得执行 Docker CLI 或 shell。显式 TLS 远端 daemon 后续另立合同，本轮对 TCP/HTTP(S) endpoint fail closed。Kubernetes normalizer 包装现有 `kubeops.Reader` 的最小只读子集，不复制 kubeconfig 或 RBAC。

### 4.1 Docker fixture

| 测试名 | fixture／输入 | 断言 |
| --- | --- | --- |
| `TestDiscoverDockerAggregateCPUFixture` | 已登记 Environment；`docker-version.json`、`docker-info.json`、`docker-ps-aggregate.json`、`docker-inspect-aggregate.json` | 生成 snapshot preview，内嵌 `container`/`component`/`configuration`/`topology-declaration`/`ownership` 等实体及 Fact/Source；物理 ID 含登记 host+daemon+container ID；Fact 状态只用合同枚举，不把声明或推断提升为 observed，也不声称 framework-supported |
| `TestDiscoverDockerPDDoesNotInferUnsupportedRoles` | `docker-inspect-pd-label-shape.json` 只有 `prefill`/`decode` 名称或标签 | 保留白名单事实；没有精确版本能力规则时不生成已确认 PD relation，相关事实为 unknown 并记录 `unparsed`/`unsupported` issue；不按名称、卡数或 TP 自动转换成 PD |
| `TestDockerDiscoveryRedactsInspectSecrets` | `docker-inspect-secret-env.json` 含标记 token、registry auth、credential URL、敏感 label | 领域 JSON、warning、error、CLI/MCP 结果均不含原值；只保留白名单内的本机受保护引用或已脱敏存在性 |
| `TestDockerEngineClientUsesOnlyAllowedGETs` | Unix listener 记录 method/path；服务端版本交集/无交集/格式错误；list 返回两个 ID；请求未列出 ID、TCP URL 和变更 method/path | 首次仅请求未版本化 `/version`，交集成立后其余路径固定 `/v1.44`；inspect 仅限本次 listed ID；不跟随重定向、代理或环境版本；其他请求在网络调用前拒绝；生产代码无 `os/exec` Docker 路径 |
| `TestDockerDiscoveryIsReadOnlyAndBounded` | 301 个 CPU 容器；单个 raw response 2 MiB 与 2 MiB+1；规范实体 64 KiB+1；恶意容器名 | 扫描最多 300 实体且状态 partial；Docker list 没有游标，结果不得伪造 continue token/server pagination；超限响应或实体有界失败并保留缺口 |
| `TestDockerUnavailableForbiddenAndPartialAreDistinct` | daemon 不可达、socket forbidden、3 个 inspect 中 1 个失败 | 分别编码 unavailable/forbidden/partial，不能返回空环境 complete；成功对象保留，失败对象和原因有界 |
| `TestDockerDiscoveryCancellationAndTimeoutDiffer` | client 阻塞；分别由调用者 cancel 与达到 30 秒合同 deadline | cancel 返回 canceled 且不交付可保存 preview；timeout 有已完成实体时可返回标明 timeout 的 partial preview，无事实时 failed；两者都关闭 body、停止后续 inspect 且无持久化副作用 |
| `TestDockerIdentityChangeRequiresNewEnvironment` | `/info.ID` 为空或与登记 expectedDaemonID 不同；同 hostID 下 daemon identity 改变；同容器名换 ID | 空/不匹配 ID 在发现前失败；不合并旧物理实例；产生 `identity_changed`/`conflict` 并要求显式 environment update，指纹固定为 runtime/hostID/daemonID 的规范摘要，不用 hostname/容器名替代身份 |

这些 JSON 全部标记 `synthetic=true`、`hardwareEvidence=false`，只含普通 CPU 容器。不得添加虚构 GPU/NPU inventory 来让分支“覆盖硬件”。

### 4.2 Kubernetes fixture

| 测试名 | fixture／输入 | 断言 |
| --- | --- | --- |
| `TestDiscoverKubernetesNativeAndHelmOwnership` | client-go fake：Native Deployment/Service、真实 Helm metadata、只有 Helm 形状标签的 mock | Native、Helm 和 unknown manager 分开；标签 mock 不获得 Helm Ownership；identity 精确使用登记 clusterID、namespace、apiVersion、resourceKind、uid，不新增 GVK 自由字段 |
| `TestKubernetesPartialRBACStaysPartial` | Deployment 可读、Pod forbidden、Service list 超时、Secret metadata 可读 | 输出保留可见事实，完整度为 partial；forbidden、timeout、not-found 分开；缺失对象不解释为零副本/零资源 |
| `TestKubernetesSecretsAndLiveYAMLDoNotLeak` | Secret data/stringData、env value、command、volume Secret、标记 token | 复用 kubeops 脱敏后再递归检查领域 JSON；任何层均不含标记值，配置来源仅为引用 |
| `TestKubernetesPaginationAndLimitsPropagate` | 301 个对象、API 返回的 continue token、解码后白名单投影页 2 MiB/2 MiB+1 和 64 KiB entity 边界 | 不静默丢项；opaque cursor 是 32 字节加密随机的服务端句柄，绑定主体、environment revision、cluster/GVR/namespace/selectors，TTL 5 分钟、同主体 4 个/全服务 16 个，重启失效；服务端保存原 token，换 scope、过期或重启后重用须拒绝；继续读取生成新 snapshot 并引用前一份，不拼成跨扫描 complete；不声称限制 client-go 原始传输，也不伪造成 Docker pagination |
| `TestKubernetesCancellationAndUIDReplacement` | list 中取消；同名对象读取前后 UID/resourceVersion 变化 | 取消不发布完整结果；UID 变化产生冲突/新物理身份，不把两次观察拼成原子快照 |

PR2 出口：所有生产发现接口在类型层只读；固定 fixture 上结果确定、排序稳定、无 secret；partial/cancel/limit 测试通过。可选的本机 Docker daemon smoke 只能补充 `real-docker` 证据，不是合入门槛，也不得启动、停止或删除现有容器。

## 5. PR3：存储、重启、CLI 与 MCP 测试矩阵

CLI 冻结为 `infernex-agent private-inventory init --state-dir ABS --scope ID`、`environment register --input FILE`、`environment update --id UUID --expected-revision N --input FILE`、`environment show --id UUID`，以及 `discover|record|list|show|verify`。init 创建并核对 0700 状态目录、固定拥有者 UID/scope 的 `scope.json`，已存在时不覆盖；register 生成 UUID/revision=1，并把 0600 connection 配置与公开 Environment 放在同一事务；update 以 expectedRevision 做 CAS，show 不输出真实路径或凭据。`discover` 默认只向 stdout 输出 preview，只有显式 `discover --save` 才保存同一次扫描结果并在发布点前复核 Environment revision/scope；CLI 的 `record --input FILE` 只允许受限、本地普通文件中的严格 schema v1、已脱敏、无附件离线 fixture，仍执行 scope、大小和摘要校验。

MCP 的 discover/list/get/verify 均为只读。discover 返回短期 preview handle、snapshot digest、主体、Environment revision 和作用域绑定信息，不接受调用方指定持久 ID/revision。单独的 `infernex_record_domain_inventory` 是 `local-record` 非破坏本地写，只能提交同一主体、同一 tenant/environment 授权作用域、未变化的 Environment revision、TTL 未过期的 `previewHandle + digest`；不得接收任意文件路径或 snapshot 正文。同一 handle 与 digest 重试按 snapshot 幂等处理，过期、主体变化、scope 变化、Environment revision 变化或 digest 变化均拒绝。

| 测试名 | 输入／故障 | 断言 |
| --- | --- | --- |
| `TestStorePublishVerifyAndRestart` | `t.TempDir` 0700；保存 Environment 和 snapshot；新建 Store 实例模拟重启 | 文件 0600；list/get/verify 内容与 digest 相同；snapshot revision 恒1且不可变；重启无需内存索引恢复；bundle 无附件 |
| `TestStoreRejectsTamperUnknownFieldAndCrossScopeRef` | 改 record 字节/digest；加入未声明额外文件；向 record.json 加未知字段；跨 tenant 引用 | verify 全部失败且不返回可信记录；List 不把损坏记录当正常结果，须报告具体 corrupt ID |
| `TestStorePublishIsAtomicAndIgnoresPending` | Linux 本地文件系统；在 flock、每个写/Sync、`renameat2(RENAME_NOREPLACE)`、父目录 Sync 注入失败；预建 `.domain-pending-*`；目标 ID 已存在 | 固定 0600 `.writer.lock` 排他锁覆盖 revision 校验至父目录 Sync，等待最多 5 秒且可取消；rename 前 final 不可见；不支持 no-replace 时返回 unsupported，绝不退化成覆盖式 `os.Rename`；rename 后响应/父 Sync 失败返回 committed 或 outcome_unknown 并可按 ID 对账；重启 list 忽略 pending；无半 bundle |
| `TestStoreCancellationAroundPublishPoint` | 分别在临时 record 写入后、原子 rename 前和 rename 后取消 context | 发布点前取消不发布；进入/越过发布点后不得承诺“未写”，只返回 committed/outcome_unknown 并要求按 ID 查询；旧 last-known-good 始终可读 |
| `TestSnapshotConcurrentSameIDIsImmutable` | 两 writer 同 snapshot UUID：相同 digest、不同 digest | 同 digest 幂等成功且只有一个 final；不同 digest 返回 conflict；从不 overwrite、合并或自增 revision |
| `TestEnvironmentCASConcurrentRevision` | 两 writer 都以 Environment revision 3 为前置，各提交 revision 4 | 仅一个 CAS 成功；另一方 conflict 并可读取获胜 revision；禁止跳号、revision≤0 和无前置覆盖 |
| `TestStoreRejectsUnsafeStateDirectoryAndOversize` | 0777 目录、父路径或 `.writer.lock` symlink、非普通文件、300/301 实体、64 KiB/8 MiB 边界 | fail closed；不跟随不受信 symlink；无超限临时残留；错误不含文件内容；附件字段/文件均拒绝 |
| `TestCLIInitAndEnvironmentRegistrationLifecycle` | 当前 UID 下 init；重复 init；合法 Docker/K8s 登记输入；逐项加入未知字段、Docker namespace、相对 socket/远端 URL、内联 kubeconfig、缺 hostID/clusterID/expected fingerprint、未知 networkPolicy；register/update/show；并发 expectedRevision；连接文件写失败 | state dir/scope.json 为 0700/0600 且绑定当前 UID/scope，重复 init 仅核对；严格拒绝登记白名单外或 runtime 不匹配输入；register 生成 UUID/revision=1；update CAS 仅一方成功；Environment 与 connection 原子一致；show/stdout 不泄漏 socket、kubeconfig 或凭据；其他 UID 与 root 冒充固定 scope 均拒绝 |
| `TestCLISeparatesPreviewSaveAndOfflineImport` | 已完成 init/register；fake Docker/K8s reader；运行 discover、discover --save、严格 `record --input`、list/show/verify；未知 flag、symlink/raw-secret 文件 | 默认 preview 不写盘；--save 保存同次扫描且复核 scope/revision；仅已脱敏 schema v1 普通文件可导入；JSON stdout 可严格解码，错误退出非零 |
| `TestMCPAnnotationsScopeAndResponseBudgets` | 以受信 UID 启动带 state-dir 的 stdio；请求越界 environment、页 20/100/101、响应 256 KiB+1、未知字段 | discover/list/get/verify 为 readOnly；save 为 `local-record` 非只读；工具 schema 没有 tenant 参数，scope 在 adapter 调用前拒绝；分页和总响应上限严格生效 |
| `TestPrivateInventoryToolsRegisterOnlyOnTrustedStdio` | stdio + private state-dir、stdio 无 state-dir、streamable-http、HTTP 和 Dashboard 四组启动配置 | 仅配置 state-dir 的 stdio 注册五个新清单工具；未配置 state-dir 保持旧行为；HTTP/streamable-http/Dashboard 均不注册或转发新工具 |
| `TestPreviewHandleSaveBindingAndTTL` | 同/异主体、同/异 scope、Environment revision 变化、正确/错误 digest，fake clock 测 TTL 前后及重放 | 只有同主体同 scope、登记 revision 未变、TTL 内、digest 相同者保存；正文/文件参数 schema 不存在；过期与篡改不落盘；同 digest 重放幂等 |
| `TestPreviewCacheLimitsAndNoUnsafeEviction` | 同主体第 4/5 个、全服务第 16/17 个 8 MiB preview；fake clock 推进 5 分钟；一项正在保存 | 4/16 边界通过，超限拒绝新 preview；过期项失效；不得驱逐正在保存项或把缓存 preview 当持久 snapshot |
| `TestPiLocalRecordApprovalModesAndTrustedSource` | 把精确工具名 `infernex_record_domain_inventory` 分别绑定当前受信 stdio server 与任意其他 MCP server；测试 manual、full、精确 root-risk | 仅把受信 stdio 来源的精确工具名加入 `autonomousLocalTools`；manual 仍弹审批，full 可按 local-record 规则执行；无法证明来源时保持审批；root-risk 只影响当前 Pi 会话且不跨 API，所有模式仍执行 scope/handle/digest 校验 |
| `TestEndToEndPartialDiscoveryPersistsAsPartial` | 部分 RBAC Kubernetes fake 和一个 inspect 失败的 Docker fake | 只有显式 save 才记录；重启后 partial/unknown 与来源仍在；show/list 不改写为 complete |
| `TestDockerOnlyStartupAndToolRegistrationNeverReadsKubeconfig` | init 后只登记 Docker Environment；以 `--transport stdio --private-state-directory ABS --private-inventory-only` 启动；把 Kubernetes config/client factory 设为调用即失败的 spy；列 MCP tools | 服务和 CLI 可在无 kubeconfig 时启动；spy 调用为 0；只注册五个新清单工具，旧 K8s/Bridge 工具不因 nil 依赖伪注册；同一状态目录改用 HTTP/streamable-http/Dashboard 时新工具为 0 |
| `TestKubernetesDefaultRegistrationAndAutoDiscoveryDoNotRegress` | 未配置 Docker；沿用当前 kubeconfig 自动发现 fake/现有测试；分别以旧 stdio 配置及增加 private state-dir 的 stdio 列 MCP tools | 现有 K8s detect/list/read 工具名称、默认配置查找、namespace scope、Secret 脱敏及配置错误语义不变；未配置 state-dir 时不新增领域工具，显式增加后新旧工具并存且不遮蔽旧名 |

Store 可参考 `configversion` 的发布算法，但首批明确限 Linux 本地文件系统，并实现 5 秒可取消 flock 与 `renameat2(RENAME_NOREPLACE)`；不支持该原语或使用 NFS 时 fail closed。load 必须使用 `DisallowUnknownFields`，校验无附件的完整 bundle 摘要、固定文件列表和作用域后才返回。Environment 更新由锁和 CAS revision 串行化；InventorySnapshot 永不更新。preview handle store 与持久 store 分开，采用 TTL 5 分钟和 4/16 限额；重启后 preview 失效，不能把未发布 preview 恢复成 snapshot。K8s cursor 另用 32 字节加密随机服务端句柄，并按同样的 TTL、主体/服务限额和重启失效规则管理。

## 6. 可运行命令与未来目标

以下命令今天即可从 `component/InferNex-Agent` 运行，验证现有边界；它们不代表前三个 PR 已实现：

```bash
go test -race ./internal/kubeops ./internal/configversion ./internal/diagnosticexec
go test -race ./...
go vet ./...
cd test/acceptance/next-generation
python3 selfcheck.py
```

以下命令已可在实现分支运行；其中 Linux 原生文件系统测试需要 Linux 执行，其他平台的交叉编译不能代替运行：

```bash
go test -race ./internal/domain
go test -race ./internal/adapters/dockerdiscovery ./internal/adapters/kubernetesdiscovery
go test -race ./internal/domainstore ./cmd/infernex-agent ./internal/mcpserver
if go list -deps ./internal/domain | \
  rg '^gitcode.com/openFuyao/InferNex/(api/v1alpha1|component/InferNex-Agent/internal/(observer|deployer|remediator|experiment))$'; then exit 1; fi
npm --prefix pi test
npm --prefix pi run typecheck
go test -race ./...
go vet ./...
```

CPU Kind 复用[现有 starter kit 命令](https://github.com/lsjfy-open-com/infernex-agent/blob/codex/private-deployment-evolution/component/InferNex-Agent/docs/guides/next-generation-acceptance-lab-zh.md)。前三个 PR 的必选门禁不需要 Docker daemon、Kind、网络、客户配置、模型服务或加速卡；这些 live smoke 只作为分列证据。

## 7. 交付门禁和就绪决策

| PR | 必过门禁 | 就绪决定 |
| --- | --- | --- |
| PR1 | 仅两个 kind；Environment 正整数 revision；snapshot UUID/revision=1/不可变；完整类型白名单与内嵌实体引用；四组合 schema v1 fixture；tamper/unknown/secret/scope/limits/无 Bridge import | **现在可开工**：输入来自公开设计和合成 JSON，无客户依赖；KnowledgeRecord 延后 |
| PR2 | Unix socket `net/http` Engine API v1.44 窄 client；Docker raw/K8s 投影 limits；Docker/K8s fixture；secret 零泄漏；partial RBAC；身份替换；cancel；无 Docker CLI | **现在可开工**：CPU synthetic fixture 足够；真实 daemon/集群只补充 smoke，远程 TLS daemon 延后 |
| PR3 | init 与 Environment register/update/show；Environment CAS；snapshot 不可变幂等/conflict；flock/no-replace 原子重启/pending/cancel；preview/cursor；受信 stdio MCP/Pi scope/分页/审批；Docker-only/K8s 回归 | **在 PR1 schema 与 PR2 Reader 冻结后可开工**，不等待客户硬件 |

每个 PR 描述须列出新文件、测试命令、通过数量、未测 live 项和兼容影响。任一 secret 标记泄漏、跨租户引用成功、partial 被报 complete、Docker 伪造游标、发布点后错误承诺“取消且未写”、snapshot 被覆盖、损坏记录通过、save 接受正文/文件、写工具标为 read-only 或领域包导入 Bridge 都是合入阻断项。

客户尚待提供的引擎、PD 连接器、GPU/NPU、权重、真实 SLO 和私域样例只阻塞后续 Profile、计划、执行和真实硬件验收，不阻塞这三个 PR。实现团队不得把“等待客户”作为无期限占位：缺失项登记责任人、所需字段和最晚进入哪个后续门禁；前三个 PR 以公开 fixture 完成，且文档始终显示这些结果只证明控制逻辑。
