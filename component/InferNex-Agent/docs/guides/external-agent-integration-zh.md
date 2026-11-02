# 外部 Agent 与合作方接入

本页是合作方接入 InferNex Agent 的统一入口。推荐组合是 **Agent Skills 说明调用决策，
MCP 提供实际工具，部署指南负责准备环境**。外部 Agent 继续使用自己的模型和编排框架，
通过 InferNex 获取有范围、有证据的运维能力。

## 当前可交付的范围

本次文档发布前（2026-11-02）核对，发布基线仍为 `0.5.0-alpha.22`；`develop` 为 `43ea036`，
私域演进分支为 `6bf0c0e`。这三个版本标识不能互相替代。

- **已发布**：Kubernetes 环境/工作负载发现、事件与日志、Service 后端检查、CANN/HiXL
  诊断知识和受限诊断委派、同构部署规格只读规划。Bridge 存在且显式启用时另有受控部署、
  实验与回退路径，不能推定为通用 Docker/K8s 部署。
- **分支已实现、未发布**：中立 Environment/InventorySnapshot、Docker/K8s 只读发现、
  本地持久清单、CLI/受信 stdio MCP、固定 scope 令牌认证的私域 Dashboard 和快照差异。
- **仍待实现/验收**：类型化 Docker 与原生 Kubernetes 部署闭环、PD 编排、请求级分流、
  通用自动修复与能力自演进；硬件现场验证不能由软件测试替代。

后续进度只维护在[当前路线图](../development/roadmap-zh.md)；实际可用工具以连接后的
`tools/list` 和返回的 schema 为准。alpha.22 的能力、安装和未验证项见
[发布说明](../releases/v0.5.0-alpha.22-zh.md)。

## 交给合作方的入口

1. 本页：选择接入范围、准备环境并完成联调。
2. [infernex-operations Skill](../../../../skills/infernex-operations/SKILL.md)：
   安装到**调用方 Agent**，负责“什么时候调用、如何调用、需要哪些环境”。
3. [MCP 工具目录](../reference/mcp-tool-catalog-zh.md)：查当前工具与约束；接口参数以运行时 schema 为准。

接入 Skill 使用 [Agent Skills 标准](https://agentskills.io/specification)的
`name` / `description` frontmatter、`SKILL.md` 和按需读取的 `references/*.md`。
传输使用 [MCP](https://modelcontextprotocol.io/docs/develop/connect-local-servers)。
Skill 格式不代表各客户端的安装目录或 MCP 配置文件格式完全相同。

```text
skills/infernex-operations/
├── SKILL.md
└── references/
    ├── connection.md   # 环境、端点、认证和连接方式
    └── workflows.md    # 调用示例、结果和错误处理
```

完整复制该目录到调用方支持的 Skill 搜索路径，并按其客户端规范登记 MCP 连接。
不要只复制 `SKILL.md`；两个参考文件可离线读取，不依赖整个源码仓库。
客户端若不支持自动发现 Skill，可在任务上下文显式加载该文件，并保留参考文件访问能力。
加载 Skill 不会自动安装 InferNex、建立 MCP 连接或授予集群权限。

仓库现有的两个 [CANN/HiXL 诊断 Skill](skills-and-cann-hixl-zh.md)位于
`component/InferNex-Agent/skills/`，随运行时离线包安装，由 MCP 渐进读取；它们提供领域知识。
根目录 `skills/` 是调用方接入包，不加入运行时内置 Skill 目录，不需要通过
`configure-skills.sh` 安装到服务端。这样可以独立交付接入说明，避免出现两套诊断知识副本。

## 选择连接方式

### 合作方诊断服务：优先受限诊断委派

已有 diagnose 模式的 Host 安装可提供 `http://127.0.0.1:18082/mcp`，使用独立 Bearer token、
namespace scope 与并发预算。运维人员向合作方配置受保护的凭据引用，不把 token 或
kubeconfig 写进 Skill、示例、工单或仓库。跨主机通过批准的安全隧道或认证网关连接。

连接后先完成 MCP 初始化和 `tools/list`，再调用
`infernex_get_diagnostic_delegate_contract`，据实时 scope/channel/budget 开始诊断。
受限端点不提供通用资源读取、部署/恢复/实验或任意 Shell。
详见[诊断委派联调指南](../development/diagnostic-subagent-development-guide-zh.md)。

### 可信管理节点上的通用 Agent：stdio

同机 Agent 可启动 `infernex-agent serve --transport stdio`，但需显式配置运行身份、
kubeconfig 和状态目录，确认没有继承已启用写能力的 Host 配置。
可复制的最小参数见 [Skill 连接参考](../../../../skills/infernex-operations/references/connection.md)。
权限来自服务进程的操作系统身份与 Kubernetes RBAC；`--scan-namespaces` 不能代替
通用主端点的 RBAC 租户隔离。

主 `:8080/mcp` 是 Streamable HTTP MCP，**没有内置调用方认证**。不要把它作为现成的
公网或多租户服务。若合作范围需要通用远程 MCP，应由服务提供方落实认证、TLS、隔离的
实例/凭据/状态目录及调用审计后再开放；诊断端点的 token 和私域 Dashboard token 都不能
为主 `/mcp` 增加认证。

### 私域 Docker/K8s 清单：开发分支的受信本机 stdio

使用私域演进分支的固定提交 `6bf0c0ef9981889e6ad7a8d3946ba0360ed6587e` 构建，
或使用对应单独交付的分支二进制；不能让 alpha.22 用户照抄开发分支命令。
需要 Linux 本地文件系统、固定 UID/scope，以及管理员预先登记并核实的环境身份。
Docker-only 不需要 kubeconfig、Helm、Bridge；Kubernetes 登记才需要对应的 kubeconfig/RBAC。

按[私域清单指南](https://github.com/lsjfy-open-com/infernex-agent/blob/6bf0c0ef9981889e6ad7a8d3946ba0360ed6587e/component/InferNex-Agent/docs/guides/private-inventory-zh.md)完成登记，再使用
`--private-inventory-only --private-state-directory /ABSOLUTE/PRIVATE-STATE` 启动 stdio。
HTTP MCP 和诊断委派不注册私域清单工具；[私域 Dashboard](https://github.com/lsjfy-open-com/infernex-agent/blob/6bf0c0ef9981889e6ad7a8d3946ba0360ed6587e/component/InferNex-Agent/docs/guides/private-inventory-dashboard-zh.md)
是单独的只读展示/查询入口，不能用作远程 MCP 或部署执行接口。

## 环境与模型由谁准备

- 服务提供方：按[安装指南](offline-install-zh.md)准备 Linux amd64/arm64 管理节点、受限
  kubeconfig、目标 API 网络可达性、所选模式的状态目录和凭据。完整 Host 包自带运行所需
  二进制，用户无需另装 Go/Node；源码构建遵循仓库 Go 与 Pi 构建要求。
- 调用方：提供可用的 MCP 客户端、模型/编排框架、Skill 加载方式及任务目标。外部 Agent
  使用已有模型即可；纯 MCP 工具服务不要求再配置一套服务端 LLM。InferNex 自带 TUI 或
  supervisor 模型分析使用模型时，另按[模型配置](model-configuration-zh.md)准备。
- 专项能力：Ascend 历史日志诊断可使用授权 Evidence Root 中的既有证据；目标设备主动探针
  与现场采集才需要对应硬件/驱动/CANN。Pod exec、SSH 和采集通道按实时 contract 配置。
  通用读取不要求这些条件，缺失时应降级并报告证据缺口。

## 最小联调与交付

先用合作方的真实客户端完成一次只读工作负载诊断：初始化 → 工具发现 → 委派 contract
（若使用该端点）→ 指定 namespace 工作负载 → 有界事件/日志 → 带证据和覆盖限制的结论。
再检查错误 token、越界 namespace、缺少工具、截断/分页与进程重连的处理。
保存报告、持续采集和保存清单分别验证任务授权与状态写入，不能把“连接成功”当成全部验收。

固定交付双方认可的 **服务端版本/提交 + Skill 所属提交 + 客户端版本 + 接入方式 + 验收记录**。
从固定源码版本复制 Skill，附仓库 LICENSE；若打包归档，提供 SHA256。私域分支需要写明完整
commit 和“未发布”状态；不要把浮动分支链接当成不可变交付版本。后续发布包可携带接入包，
当前 alpha.22 包不会因仓库新增 Skill 而自动包含它。

维护时只保留这一条入口链：README/文档索引 → 本页 → Skill / 工具参考 / 对应部署指南。
接口变更同步工具实现与接入示例，环境变更同步连接参考，阶段状态同步路线图；不要为每个
合作方复制一份完整工具目录。合作方专有信息放在其部署配置中，避免写入通用 Skill。
