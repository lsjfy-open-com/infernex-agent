# InferNex Agent 安全与能力边界

## 1. 信任边界

产品包含四个主要信任域：

1. 运维人员和本机 Agent Runtime；
2. InferNex Agent 进程；
3. Kubernetes apiserver 与 InferNex 控制器；
4. 可选的 OpenAI 兼容诊断模型。

Kubernetes 凭据只存在于 Agent 进程和受保护文件中。诊断模型不获得这些
凭据，也不能直接调用 MCP 工具。

## 2. 默认权限

默认只读身份按明确目标命名空间授予：

- `get/list` `InferNexService`；
- `list` Deployment、DaemonSet、LeaderWorkerSet、Pod 和 Event。

默认不允许：

- 读取 Secret；
- 读取 Node；
- 创建 Deployment、Pod、Service 或 Namespace；
- 创建集群级 RBAC；
- 执行 Pod、读取日志或端口转发；
- 任意 patch/delete Kubernetes 对象；
- shell、SSH 或宿主机命令执行。

启用日志诊断时只额外授予目标命名空间 `get pods/log`。采集器仍通过
`infernex.io/owner` 标签限定一个服务，限制 Pod、时间窗、尾部行数和字节数，
只保留匹配的分类证据。Pod 日志可能包含 Prompt、响应和业务数据，因此该权限
默认关闭；常见凭据脱敏不能替代组织的数据分级和访问审计。

长期 systemd 服务不得使用 `/etc/kubernetes/admin.conf`。

## 3. 网络边界

| 端口 | 默认绑定 | 是否自带认证 | 要求 |
| --- | --- | --- | --- |
| 8080 MCP | `127.0.0.1` | 否 | 通常只供本机 Runtime |
| 8081 Dashboard/API | `127.0.0.1` | 否 | 远程开放需 ACL/防火墙/认证代理 |
| Kubernetes API | 出站 | Kubernetes 身份 | 仅到批准 apiserver |
| 模型 API | 可选出站 | 可选 Bearer Key | 仅到批准模型端点 |

不要把 MCP 或 Dashboard 直接绑定公网。Dashboard 是只读界面，但其中包含
集群运行证据，仍属于内部运维信息。

## 4. 凭据边界

宿主机：

- kubeconfig：`/etc/infernex-agent/kubeconfig`，`0600`；
- 模型密钥：`/etc/infernex-agent/openai-api-key`，`0600`；
- 非敏感参数：`/etc/infernex-agent/agent.conf`，`0640`；
- systemd unit 和进程参数只出现凭据文件路径；
- 模型配置查看命令不打印密钥；
- 模型测试通过临时 Header 文件传递密钥，密钥不出现在 curl 进程参数中。

离线包、Helm values、Dashboard 和日志不得包含密钥。宿主机专用
ServiceAccount Token 需要纳入组织凭据轮换和吊销制度；条件允许时优先
使用企业 PKI/OIDC 的等价最小权限 kubeconfig。

## 5. 模型数据边界

发送到模型的是归一化、数量受限的诊断证据。以下数据被排除：

- Kubernetes Token、kubeconfig 和模型 API Key；
- Secret、环境变量和完整 Pod spec；
- Event note 原文；
- 节点名；
- 模型 URI 中的用户名、密码、query 和 fragment；
- 任意原始 Kubernetes 对象遍历能力。

后台 Supervisor 的分析模型输出仅作为文本建议保存到内存快照，不进入自动恢复
或回退条件。交互式对话模型可以提出 typed tool 调用和对象名称，但它不持有集群
凭据；调用仍须通过工具 schema、稳定来源、固定 workspace、RBAC、所有权检查和
本机批准。模型永远不能提供任意镜像、URL、命令、Patch 或 YAML。

## 6. 写能力边界

### 稳定来源部署

显式启用后，Agent 只能在固定 `infernex-agent-workspace` 创建/删除
`InferNexService`，且必须满足：

- 来源是 Agent 刚发现并在执行时重新校验的 Ready 既有服务，或包含完整 engine
  的 Bridge profile；
- 调用者显式 `confirm=true`；
- 对象带 Agent 所有权标签；
- 重名对象的所有权和 spec 必须完全匹配；
- 来源命名空间只有只读权限，写权限仅存在于独立 workspace；
- Agent 不直接创建下游 Deployment 或 Service。

### 自动恢复

恢复需要同时满足：

1. 身份具有命名空间级 create 权限；
2. Agent 全局开关启用；
3. 源服务显式标注允许恢复；
4. 源服务指定精确的恢复 profile；
5. profile 带运维批准标签；
6. Bridge 已观察当前 generation；
7. 连续多轮出现临界问题。

恢复只创建一个新 `InferNexService`，不覆盖源服务、不删除源服务、不切流，
也不创建或修改恢复 profile。

### 渐进实验

实验需要显式启用、命名空间级 `InferNexService create/delete`、日志读取和模板
命名空间 `InferNexServiceConfig get`。输入只能是现有稳定服务、候选名前缀和带
批准标签的 profile 名称；不接受 YAML、镜像、URL、命令或任意 patch。Agent
不修改/删除基线、不切流，回退前必须匹配实验 ID、`changeId` 和所有权。

## 7. 宿主机进程隔离

systemd 服务：

- 使用不可登录的 `infernex-agent` 用户；
- capability 集合为空；
- 启用 `NoNewPrivileges`；
- 保护内核、控制组、主机名、设备和系统目录；
- 根文件系统只读，只有状态目录可写；
- 使用私有临时目录和设备视图；
- 限制地址族和原生系统调用架构。

静态 Agent 不加载 CANN、NPU 驱动或第三方推理插件。

## 8. 已知边界与剩余风险

当前产品已经把安装恢复点和写操作变更事件持久化到受保护文件目录；这不是支持
查询、集中保留和防篡改归档的企业审计数据库。当前产品仍没有内置：

- MCP/Dashboard 用户认证和多租户授权；
- 集中式、防篡改审计数据库；
- 跨集群凭据管理；
- 告警通知渠道；
- 自动流量切换和业务回滚；
- 企业 Secret Manager 集成；
- openEuler/A2 硬件诊断；
- 主动推理请求、SSE/JSON 完整性探针和性能基线比较；

因此生产部署必须由外部网络边界保护入口，并由现有 IAM、堡垒机、日志平台、
告警平台和 InferNex 控制器补齐相应职责。

## 9. 上线安全检查

- [ ] 使用 `aarch64` 对应的 `linux-arm64` 包并验证外层 SHA256；
- [ ] systemd 服务未使用 `admin.conf`；
- [ ] kubeconfig 仅授权目标命名空间；
- [ ] 凭据和配置文件权限符合要求；
- [ ] MCP 保持回环地址或位于认证代理之后；
- [ ] Dashboard 只允许批准管理网 CIDR；
- [ ] 模型端点属于批准内网地址；
- [ ] 未需要写能力时不创建 mutation RBAC；
- [ ] 自动恢复开关、源标注和 profile 批准流程均有责任人；
- [ ] 日志采集和凭据轮换周期已纳入运维制度。
