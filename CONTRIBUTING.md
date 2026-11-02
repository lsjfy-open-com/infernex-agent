# 贡献与协作

InferNex Agent 按**自动部署**和**自动运维（包含故障处理）**两条业务主线演进，共享 Agent Core。
开始改动前请参考[模块边界和 PR 演进计划](component/InferNex-Agent/docs/architecture/kubernetes-first-zh.md)，
分支以[分支与发布](component/InferNex-Agent/docs/development/branches-and-releases-zh.md)为准。功能状态以[工具目录](component/InferNex-Agent/docs/reference/mcp-tool-catalog-zh.md)和
[路线图](component/InferNex-Agent/docs/development/roadmap-zh.md)为准。

## 范围与标签

每个 PR 选择一个主要范围；跨模块改动可补充相关范围。以下为建议的 GitHub 标签；标签尚未配置时，
在 PR 正文写明同名范围即可。

| 范围标签 | 负责内容 | 新分支前缀 |
| --- | --- | --- |
| `area/deployment` | 推理栈与模型服务的计划、受控变更、候选实验、验收与回退 | `deploy/` |
| `area/operations` | 健康观察、故障触发、取证、诊断、恢复候选及恢复验证 | `ops/` |
| `area/core` | 集群身份与发现、工具契约、权限、证据、变更日志及配置版本基础 | `core/` |
| `area/traffic` | 服务入口、请求级分流、摘流排空与流量验收 | `traffic/` |
| `area/adapter` | InferNex、Helm、客户平台的能力与执行映射 | `adapter/` |
| `area/experience` | Pi/classic、MCP 与 Dashboard 入口，以及安装、打包和交付 | `experience/` |

共享能力只维护一份；入口复用 Go Core 的工具与权限约束。诊断建议、创建恢复候选、控制面 Ready 和
服务恢复验收是不同结果，描述与验证时应明确区分。

## 一个 PR，一个可验收行为

PR 先说明具体触发条件，以及改动前后的行为，再给出验收方法。实现一个行为所必需的代码、测试和
文档可以跨包；无关功能、批量重命名、额外权限和交付改造应拆开。修复已有路径不代表整条部署或运维
闭环已经完成，计划能力也不能写成已实现。

使用[PR 模板](.github/pull_request_template.md)，写明主要范围、base、依赖和未验证项。
普通 PR 从当前 `develop` 建立；依赖尚未合入的 PR 时，可以使用堆叠分支：

- base 指向直接依赖的分支，正文链接依赖 PR，注明验收和合入顺序。
- 每一层都应能在声明的 base 上独立编译和验证；不要让评审 diff 重复包含整条依赖链。
- 下层合入后，更新上层 base，并重新检查 diff 与受影响验证。共享分支历史的重写应先与使用者协调。

现有 Draft 集成候选用于保留和验证累积实现，不是已验收的发布基线。沿用各候选 PR 和
[候选验收清单](component/InferNex-Agent/docs/development/candidate-validation-zh.md)声明的合入、发版门槛；
合入某个修复或文档 PR，不意味着这些门槛已经满足。

## 验证、权限与恢复

验证与改动范围匹配，并在 PR 中记录实际运行的命令、结果及环境：

- Go 行为变化覆盖有意义的正常与失败场景；竞态、崩溃恢复或回退修复应有相应回归。
  Agent 模块的整体检查在 `component/InferNex-Agent/` 中运行 `go test -race ./...` 和 `go vet ./...`。
- Pi、脚本和 Chart 变化运行对应的现有检查；文档改动检查内容、链接和 `git diff --check`，无需增加空洞测试。
- 涉及权限或写能力时，说明新增 API verb、对象/命名空间、宿主机访问、凭据或执行通道；没有变化写“无”。
  写操作说明批准边界、目标身份、前置检查、失败回退及回退失败的处理方式。
- 简述代码/配置升级后的撤销方式和持久状态兼容性；没有运行时影响写“无”。
  Kind、openEuler/Ascend A2、真实模型服务等未验证环境明确列出，不用本地 fake client 测试替代现场验收。

优先复用现有 CI 和测试工具。不要为了填写模板引入新的检查框架。

## 分支与清理

新分支建议使用上表前缀加简短英文主题，例如 `deploy/rollback-identity`、`ops/incident-evidence`。
一个分支对应一个 PR；已有 `agent/`、`feat/` 或其他前缀的分支无需仅为命名调整历史。

合入后，先确认没有打开的 PR、堆叠依赖或仍需保留的候选工作引用该分支，再清理远端分支。
普通 merge 可用 `git merge-base --is-ancestor <branch> origin/develop` 验证提交已被保留；
squash/rebase 合入则核对已合并 PR 及最终内容，不能只看提交祖先关系。清理旧候选时，记录其被哪个
保留分支或提交完整替代，先处理关联 PR 的关闭或替代说明。不要删除 `main`、发布标签或含未保留工作的分支。
