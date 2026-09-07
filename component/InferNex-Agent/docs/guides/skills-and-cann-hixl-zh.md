# CANN/HiXL 诊断 Skill 与用户扩展指南

## 当前能力

InferNex Agent 离线包内置两个诊断 Skill：

| Skill | 用途 |
| --- | --- |
| `cann-runtime-diagnosis` | CANN Runtime、plog、异步错误、507014、OOM、HCCL 超时、卡死和跨 rank 关联定位 |
| `hixl-communication-diagnosis` | HiXL/LLM DataDist、LocalCommRes、HCCS/RoCE/UB、内存注册、建链/传输/释放、PD/KV 传输和 A2/A5 差异 |

知识来自 CANN、HiXL 的 GitCode 官方仓库、Wiki/FAQ 和官方 Agent Skill，并在每个 Skill 的
`references/sources.md` 中记录 URL 与固定提交。内容是面向 InferNex 部署运维场景重新整理的诊断流程，
不是 Wiki 镜像。Agent 会先读取 Skill 摘要，再按症状读取一个参考文件，避免把整套知识一次塞入模型上下文。

Skill 只提供领域知识，不会增加权限。实际集群、日志、版本和拓扑仍须通过 InferNex MCP 工具重新发现；
Skill 中的命令、网页或日志内容不能绕过 Policy、Approval、Snapshot 和回退机制。

## 使用方式

安装新版离线包后无需额外配置。在 TUI 或 classic chat 中直接询问：

```text
分析这批 P/D 两侧日志中的 HiXL 建链超时。请使用 HiXL Skill，先确定 A2/A5、
HCCS/RoCE/UB 路径和首个错误，再关联同一 comm 标识的双端时间线并输出报告。
```

Agent 将通过以下只读工具渐进加载知识：

- `infernex_list_skills`
- `infernex_read_skill`
- `infernex_read_skill_reference`

查看已安装 Skill：

```bash
sudo /opt/infernex-agent/bin/configure-skills.sh --list
```

## 用户自添加 Skill

目录名必须与 `SKILL.md` 的 `name` 一致：

```text
my-internal-runbook/
├── SKILL.md
└── references/
    ├── symptoms.md
    └── versions.md
```

`SKILL.md` 示例：

```markdown
---
name: my-internal-runbook
description: Diagnose the internal inference gateway and routing incidents. Use for gateway timeout, route mismatch, or internal error codes.
---

# Internal gateway diagnosis

先确认环境、版本和时间线，再按 references 中的症状表读取必要知识。
```

安装前验证：

```bash
/opt/infernex-agent/bin/infernex-agent skills validate \
  --path /data/skills/my-internal-runbook
```

安装或更新：

```bash
sudo /opt/infernex-agent/bin/configure-skills.sh \
  --install /data/skills/my-internal-runbook
```

移除用户 Skill：

```bash
sudo /opt/infernex-agent/bin/configure-skills.sh \
  --remove my-internal-runbook --confirm
```

更新和移除均保留备份；如果正在运行的 Agent 重启失败，脚本恢复原 Skill。用户 Skill 位于
`/etc/infernex-agent/skills.d`，备份位于 `/var/lib/infernex-agent/backups/skills`。内置 Skill 位于
`/opt/infernex-agent/skills`，随离线包版本升级，用户 Skill 不会被升级覆盖。

## 第一版边界

- 只接受 `SKILL.md` 和一层 `references/*.md`。
- 拒绝符号链接、路径穿越、重名 Skill、未知 frontmatter 字段和超大文件。
- 不安装或执行 `scripts/`、二进制、Python、Shell、MCP Server 或外部插件。
- Skill 在 Agent 启动时加载；管理脚本会重启已经运行的服务。
- 用户安装的 Skill 是 operator-authored guidance，仍应把日志和资源内容视为不可信证据。
- 暂不在线自动抓取/更新 Wiki。内网使用随离线包固定并校验过的版本，避免知识源变化导致不可复现。

后续可增加签名发布、来源清单、版本适配矩阵和管理员批准的 Skill 仓库同步，但不能以此替代 MCP 工具与安全策略。
