# Host 权限切换与跨节点网络诊断

本页描述 develop 新增能力，尚未包含在 alpha.13 安装包中。入口为 Pi TUI：

```bash
sudo /opt/infernex-agent/bin/tui.sh
```

## `/mode_change`

```text
/mode_change status
/mode_change root
/mode_change normal
```

每次打开或恢复 TUI 默认 normal；root 不跨会话保存。切换先执行 `id -u` 验证，失败保持原模式；
运行中的本机命令结束后才能切换。状态栏常驻显示模式，工具输出包含本机目标 UID、工作目录、
命令、退出码、超时/取消及截断状态。

| 模式 | 本机执行身份 | 工作目录 |
| --- | --- | --- |
| normal | `infernex-agent`，清理补充组并按账户重建、清空 capabilities、开启 no-new-privileges | 该用户的 home，默认 `/var/lib/infernex-agent` |
| root | root；必须从 root 启动的 TUI 切换 | 启动目录或 `--workspace` |

自定义服务用户时，使用 `tui.sh --host-user <user>`。Linux 需要 `getent`、`setpriv`、`bash`。
normal 模式不会在执行失败后偷偷回退为 root；用户不具备 root 启动条件时也不能仅靠 slash 命令提权。

TUI 控制进程仍由启动用户运行，模式约束的是本机工具命令的 OS 身份；这不是把整个控制进程变成
一个抵御恶意扩展的隔离沙箱。Pi 内置文件/bash 工具被拦截，本机文件读写、搜索、SSH 和 kubectl
统一经过 `infernex_host_exec`，避免 normal 模式绕过 UID 切换。每条命令展示完整预览并由本地终端批准。
大的输出进入现有 Artifact Store 分页读取，不直接灌入上下文。不会把模型密钥和 `BASH_ENV` 传入命令。

后台 `infernex-agent.service` 继续使用原服务账户，执行模式 detect/diagnose/modify 等和 Kubernetes
RBAC 不随此命令改变。Pod exec 仍使用容器自身用户；SSH 对端用户由 SSH 配置/目标指定，不能将本机
root 当成对端 root。normal 模式不能从 TUI 新发起 `host-root` helper 请求；已运行的后台采集任务
不因切换自动停止，可显式停止对应任务。

## SSH 和通用网络工具

`infernex_host_exec` 可以执行经批准的 SSH、kubectl exec、文件采集及现场安装的命令。
root 模式的 SSH 使用 root 的 HOME/密钥和 known_hosts；normal 使用服务用户的 HOME。
后台 MCP 的 SSH 别名配置仍是另一条路径，不因 TUI 切换而改写。

`infernex_network_probe` 提供以下结构化调用，可指定 `sshTarget` 在对端运行：

| probe | 工具 | 作用 |
| --- | --- | --- |
| addresses / routes | ip | 地址、路由 |
| sockets / tcp-counters | ss / nstat | 连接汇总、TCP 计数 |
| rdma-links / rdma-counters | rdma | RDMA 链路和统计 |
| dns | getent ahosts | 目标机器上的名称解析 |
| ping / traceroute | ping / traceroute | 有界连通性和路径探测 |
| tcp-connect | nc | 指定端口建连 |
| ethtool-stats | ethtool -S | 网卡统计，需指定 interface |
| iperf-client | iperf3 | 10 秒吞吐测试，产生负载；对端需已有服务 |

这是对系统工具的集成调用，不会偷偷安装软件。依赖缺失、权限不足、SSH 认证失败均返回真实 stderr。
SSH 使用 BatchMode 和严格主机密钥检查；首次接入需要配置好可信 known_hosts。root 模式也不绕过这一点。

后台 MCP 同时新增固定 `network-addresses`、`network-routes`、`network-sockets`、
`network-tcp-counters`、`rdma-links` 和 `rdma-counters` 探针，可走 local/pod/已配置 SSH/host-root。
CollectorRun 增加 TCP 和 RDMA 计数持续采样 Profile。未配置 SSH 时目录不再误报 SSH 通道可用。

## HCCN/PFC 背压证据

`infernex_sample_pfc` 指定 deviceIds、samples、intervalSeconds，可选 sshTarget；每轮保留 UTC 时间、
设备 ID 和完整 `hccn_tool -i ID -stat -g` 输出，默认两次、间隔 10 秒。任一设备失败会返回非零退出码。
不同驱动的字段不同，不按未经验证的固定列裁剪。长时、多 Pod 采样继续使用
[CollectorRun](collector-runs-zh.md) 的 hccn-pfc-stats。

分析需要比较同一设备/端口的计数增量，并关联对端、时间窗、丢包/重传、吞吐和 HCCL 延迟。
累计 PFC 非零不等于当前存在背压；计数清零、设备重启或身份变化应分段，不能相减后当作负增量。

## HCCL 实际测试

`infernex_run_hccl_test` 已能调用本机安装的 `mpirun` 和显式 HCCL 测试二进制：

- 支持 all_reduce_test、all_gather_test、broadcast_test、reduce_scatter_test；
- 必填 executable、ranks、devicesPerNode；多节点必须提供 hostfile；
- 可提供 CANN setupScript，先 source 再启动 MPI；不会假设非交互 shell 已加载 CANN 环境；
- 默认数据 8 KiB–64 MiB，可设置至 1 GiB；默认 300 秒，最长 600 秒；
- root 模式为 Open MPI 添加 `--allow-run-as-root`，normal 不添加；
- 参数预览和执行批准独立于只读采集；测试消耗 NPU 和网络资源。

示例输入：

```json
{"executable":"/usr/local/Ascend/ascend-toolkit/latest/tools/hccl_test/bin/all_reduce_test","ranks":16,"devicesPerNode":8,"hostfile":"/etc/hccl/hostfile","setupScript":"/usr/local/Ascend/ascend-toolkit/set_env.sh","timeoutSeconds":300}
```

必须以现场安装的实际路径为准，先做版本和工具可用性检查。CLI 依据
[Ascend 官方 HCCL 示例](https://gitee.com/ascend/cann-hccl/blob/3ad6148295fd0180d9492433548ba799d4b94661/README.md)，
不保证不同厂商 MPI 或其他版本参数兼容。当前没有自动安装 CANN/HCCL、自动分配空闲卡或持久调度 benchmark。

取消/超时终止本机进程组；跨节点 MPI 是否完整清理取决于 launcher，重试前必须检查对端 rank 已退出。
命令退出成功只代表进程成功，带宽、正确性和现场性能目标还要根据输出验收。硬件实测需在 Ascend 实验环境完成。

## Pod plog

[外部 plog 采集](plog-capture-zh.md) 原有路径保留，无需 SSH；需要 diagnose 或更高执行模式和目标
namespace 的 `create pods/exec` 权限。TUI 中获准的 kubectl exec 可进一步检查自定义路径、容器工具和
文件权限。不要因为固定采集根找不到文件就判断“Agent 无法进入 Pod”。
