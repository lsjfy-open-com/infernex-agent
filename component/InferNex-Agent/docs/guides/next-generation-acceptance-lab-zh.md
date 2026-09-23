# 下一代验收实验室 starter kit

本文配套[下一代课题简介](https://github.com/lsjfy-open-com/infernex-agent/blob/develop/component/InferNex-Agent/docs/architecture/next-generation-topic-and-acceptance-zh.md)和[环境、数据与量化验收细则](https://github.com/lsjfy-open-com/infernex-agent/blob/develop/component/InferNex-Agent/docs/development/next-generation-acceptance-spec-zh.md)，给出出题方可复现的 CPU Kind 基线、三组合成开发数据、评估者真值和记录模板。它是 **starter kit，不是完整验收交付**。CPU pause workload 只验证对象发现、管理权线索、数据格式和评估流程，不提供推理服务，不能证明 GPU/NPU、HCCL/RDMA、Mooncake、交换机、吞吐、SLO 修复或生产恢复。

本次在没有 Docker 的本机只运行 Python 生成与自检；没有创建 Kind 集群、没有 `kubectl apply`、没有安装 Helm release。任何 live 结果须由实际执行者按模板另行记录，不能把本指南或合成数据记为实测通过。

## 1. 交付边界与用例矩阵

| starter 内容 | 对应提案 | 可以验证 | 不能验证 |
| --- | --- | --- | --- |
| Native、真实最小 Helm release、Helm 元数据 mock 的 CPU 拓扑 | A2、B1、B3 的开发前置 | inventory 中三类对象/管理线索是否可区分 | B3 完整计划→批准→业务验证→恢复；真实推理流量 |
| `dev-case-01`，评估者真值 `submit-blocked` | A7、B9 开发集 | 提交等待、传输、远端完成三段结构及边界结论 | 真实 HCCL/Mooncake 软件故障或修复 |
| `dev-case-02`，评估者真值 `network-delay` | A7、B9 开发集 | 受控传输延迟的结构化判定 | 真实网卡、RDMA、交换机故障或修复 |
| `dev-case-03`，评估者真值 `insufficient-evidence` | A7、B9 开发集 | 缺证据时停止归因 | 任一真实根因 |
| participant/evaluator 分包与记录模板 | B9、B11 的流程前置 | 文件清单、hash、公开开发集的数据泄漏自检 | 同仓目录级隔离；正式盲测独立性 |

三组数据都在公开仓库，只能算开发集。正式 B9/B11 盲测必须由独立评估者在另一个账号或执行环境保管真值和注入控制，另外生成未公开变体；不得把 `generated/evaluator`、真值故障名称、注入参数或案例答案复制进 participant bundle、Agent 工作区、Skill、模型提示或运行轨迹。评分方法与通过阈值应提前公开，具体答案保持隔离。同仓的两个目录便于审阅打包规则，本身不构成安全隔离。

当前没有准备好 B3 的真实客户 CRD/API 合同用例，也没有 B4/B9 所需 NVIDIA、Ascend、网络设备和交换机现场。B5–B8、B10 仍需要真实服务、流量、批准、恢复和界面产物。本 starter kit 不能据此宣称所有 B 类用例可运行或已经通过。

## 2. 文件和数据合同

实验目录为 `component/InferNex-Agent/test/acceptance/next-generation/`：

- `kind.yaml`：单节点 CPU Kind 配置；节点镜像由创建命令按 digest 固定。
- `manifests/topology.yaml`：test-only namespace、Native workload 和只带 Helm 形状元数据的 mock。mock 没有 Helm release record，专门测试“标签不是管理权证明”。
- `helm/inventory-fixture/`：真实最小 Helm Chart，产生独立 Deployment/Service 和 release record。
- `generate.py`、`schemas/`：仅用 Python 标准库生成带 schema、来源、时间戳、rank、trace/request ID、时钟不确定度、单位和逐记录 SHA-256 的 JSONL。
- `generated/participant/`：可交给被测方的中性 ID 数据和 participant manifest。
- `generated/evaluator/`：原因、预期分类和禁止结论；正式执行时移出被测方可读环境。
- `generated/SOURCE-MANIFEST.json`：生成器/schema 与全部生成文件的字节数、SHA-256、synthetic 标记和限制。
- `templates/`：测试结果记录和请求方真实脱敏证据清单。

JSONL 不是 HCCL plog 格式，`source.kind` 固定为 `synthetic-fixture`，`source.original_log=false`，每条记录都含 `synthetic=true`。这些字段不得在报告中去掉，也不得把合成延迟写成真实设备测量或诊断证明。

## 3. 无集群的确定性检查

要求 Python 3.9 或更新版本；脚本只使用标准库。以下命令从仓库根目录运行：

```bash
cd component/InferNex-Agent/test/acceptance/next-generation
python3 selfcheck.py
fresh_dir="$(mktemp -d)"
python3 generate.py --output "${fresh_dir}/generated"
diff -ru generated "${fresh_dir}/generated"
```

`generate.py` 对已存在的 `--output` 路径直接拒绝，避免清除调用者文件。`selfcheck.py` 在临时目录重新生成并逐字节比较，同时检查 8/8/5 条事件计数、participant/evaluator 分离、字段、时间格式、单位和 hash。若要更新仓库基准，先生成到新的空目录，review diff 后再用安全的文件操作替换；不要把 evaluator 文件打进 participant 包。

## 4. 可选 CPU Kind 实验

版本以[当前 GitHub Actions workflow](https://github.com/lsjfy-open-com/infernex-agent/blob/develop/.github/workflows/infernex-agent.yaml)为依据：Kind `v0.31.0`；node image `kindest/node:v1.35.0@sha256:452d707d4862f52530247495d180205e029056831160e22870e37e3f6c1ac31f`；Helm `v4.2.0`；inventory image 与[现有 e2e fixture](https://github.com/lsjfy-open-com/infernex-agent/blob/develop/component/InferNex-Agent/test/e2e/fixtures.yaml)一致，为 `registry.k8s.io/pause:3.10`。workflow 没有显式固定 Docker 与 kubectl 客户端版本，执行者必须保存 `docker version` 和 `kubectl version --client -o yaml` 输出，不能补写一个并不存在的 pin。

建议起步机为独立 Linux amd64 VM，4 vCPU、8GiB 内存、40GiB 可用磁盘；这是 CPU inventory 实验的准备建议，尚未经本机实测，不代表硬件推理容量。Host Agent 的 systemd 安装也在该 VM 内进行，不在生产管理机上创建测试集群。使用 arm64 时另行核验工具和镜像架构并留结果。

前置条件：Docker daemon 可用、Kind/Helm 为上述版本、kubectl 客户端与 Kubernetes 1.35 保持支持的版本偏差、目标机器能取得两个镜像，或者已按下一节离线导入。出题方须提供实际工具安装包、版本和 SHA-256 清单；离线环境还需预装 Python、Agent 安装包及内网模型入口。正式发题时冻结该清单，不仅给出“安装最新版”的要求。建议先记录：

```bash
docker version
kind version
kubectl version --client -o yaml
helm version
```

创建和安装命令如下。脚本对每个 kubectl/Helm 操作显式指定 `kind-infernex-nextgen-acceptance` context；apply 前如果同名 namespace 已存在，会先要求 `infernex.io/acceptance=test-only`，避免覆盖普通 namespace。

```bash
cd component/InferNex-Agent/test/acceptance/next-generation
export KIND_NODE_IMAGE='kindest/node:v1.35.0@sha256:452d707d4862f52530247495d180205e029056831160e22870e37e3f6c1ac31f'
kind create cluster --name infernex-nextgen-acceptance --config kind.yaml --image "${KIND_NODE_IMAGE}" --wait 120s
./scripts/lab.sh apply
./scripts/lab.sh check
kubectl --context kind-infernex-nextgen-acceptance -n infernex-nextgen-acceptance get deployment,service -o wide
helm status acceptance-inventory --kube-context kind-infernex-nextgen-acceptance --namespace infernex-nextgen-acceptance
```

预期 inventory 有三类：`native-inventory` 是 Native；`helm-release-inventory` 有真实 release record；`helm-metadata-mock` 只有标签/注解。pause 容器不监听 Service 的 placeholder port，因此 Ready 或 Service 存在不能当作业务请求成功。

被测 Agent 在同一隔离 VM 安装并使用这个测试集群的 kubeconfig，步骤见[Host 安装指南](https://github.com/lsjfy-open-com/infernex-agent/blob/develop/component/InferNex-Agent/docs/guides/host-install-openeuler-zh.md)。测试前核对其当前上下文和允许扫描的 namespace，不能把另一集群的 Dashboard 当成本实验结果。在 TUI 中要求 Agent 调用现有 `k8s_discover_api_resources`、`k8s_list_workloads`，查询 `infernex-nextgen-acceptance`，并保存工具返回和工具调用时间。当前 alpha.19 只作为只读基线；“识别真正的 Helm 拥有者并拒绝 mock 推断”是 B1/B3 的目标检查，不能因为 fixture 已创建就声称当前产品已具备管理权判定。

## 5. 离线镜像准备

在授权联网准备机上拉取与导出固定输入：

```bash
export KIND_NODE_IMAGE='kindest/node:v1.35.0@sha256:452d707d4862f52530247495d180205e029056831160e22870e37e3f6c1ac31f'
docker pull "${KIND_NODE_IMAGE}"
docker pull registry.k8s.io/pause:3.10
docker save -o infernex-nextgen-kind-images.tar "${KIND_NODE_IMAGE}" registry.k8s.io/pause:3.10
sha256sum infernex-nextgen-kind-images.tar > infernex-nextgen-kind-images.tar.sha256
```

通过批准介质带入离线实验机，先核验和导入，再创建集群并加载 pause image：

```bash
sha256sum --check infernex-nextgen-kind-images.tar.sha256
docker load -i infernex-nextgen-kind-images.tar
export KIND_NODE_IMAGE='kindest/node:v1.35.0@sha256:452d707d4862f52530247495d180205e029056831160e22870e37e3f6c1ac31f'
kind create cluster --name infernex-nextgen-acceptance --config kind.yaml --image "${KIND_NODE_IMAGE}" --wait 120s
kind load docker-image registry.k8s.io/pause:3.10 --name infernex-nextgen-acceptance
./scripts/lab.sh apply
```

这个 fixture 不创建 NetworkPolicy。即使另加 NetworkPolicy，也不能单凭它声称互联网完全阻断，因为结果取决于 CNI、DNS、Host 网络、代理、镜像拉取和控制面路径。正式 E-O 应由环境方实施出口控制，并用网络审计记录外网连接成功数、DNS/HTTP 测试、例外路径和时间窗。

## 6. 清理与恢复边界

以下命令会删除 test-only namespace、其中对象和 Helm release，之后删除整个 disposable Kind 集群。只可用于本指南创建的本地测试集群，绝不能指向生产或共享集群。脚本会核对固定 context 与 namespace 标签；查询失败会退出，不会当成“namespace 不存在”。

```bash
cd component/InferNex-Agent/test/acceptance/next-generation
./scripts/lab.sh cleanup
kind delete cluster --name infernex-nextgen-acceptance
```

若安全检查拒绝，不要改标签或跳过检查来强制删除；先人工核对 kubeconfig、cluster identity、namespace UID 和资源所有者，并把异常记录为未清理。

## 7. 真实现场补齐方式

真实硬件表必须由环境拥有者填写，任何未知值保持“未知”：环境 ID/访问窗口；节点与 CPU 架构；GPU/NPU 厂商、型号、数量、拓扑和资源键；驱动、固件、runtime、通信库及采集依据；NIC/RDMA/交换机型号、端口映射、MTU、PFC/ECN 配置及采集权限；Kubernetes、device plugin/DRA、调度约束；镜像/Chart/权重 digest；模型与负载；存储；互联网出口和内网模型入口；批准人；恢复负责人。没有实机和版本证据时不得从资源名、镜像 tag 或样例日志猜填。

请求方按[`requester-real-log-checklist-zh.md`](https://github.com/lsjfy-open-com/infernex-agent/blob/develop/component/InferNex-Agent/test/acceptance/next-generation/templates/requester-real-log-checklist-zh.md)提供脱敏日志、分段时间、rank 映射、两端区间计数和来源 hash。每次执行复制[`test-result-record-zh.md`](https://github.com/lsjfy-open-com/infernex-agent/blob/develop/component/InferNex-Agent/test/acceptance/next-generation/templates/test-result-record-zh.md)，分开标记 synthetic、real-cluster、real-hardware；缺条件填“未测”或“证据不足”。
