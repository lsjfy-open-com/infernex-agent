# InferNex Agent

[English](README.md) | 简体中文

InferNex Agent 是运行在 InferNex 管理节点上的本地 AI 运维 Agent。它和
`kubectl-ai`、K8sGPT 的 CLI 形态一样，使用当前 `kubectl` 上下文探索集群；它不会
为了运行自身而在 Kubernetes 中安装 Agent Pod、Controller 或新 CRD。

用户用自然语言描述目标，Agent 负责：

```text
理解意图 → 自动发现 → 选择工具 → 执行只读探索 → 形成计划
         → 本机确认写操作 → 验证结果 → 诊断/回退 → 输出证据
```

它首先复用 openFuyao 的 BKE/Cluster API、Kubernetes、Helm 和应用管理方式，再按实际
部署入口复用 InferNex 主 Chart 或可选的 InferNex Bridge/KServe，以及
vLLM/vLLM-Ascend、Mooncake、Hermes、PD Orchestrator、Eagle-Eye 和
infernex-checker；不建立第二套推理编排器。

## 安装：只选 CPU 架构

正常用户只使用一个发行包：

```text
infernex-agent-<版本>-linux-amd64.tar.gz  # x86_64
infernex-agent-<版本>-linux-arm64.tar.gz  # aarch64
```

这里没有“宿主机包”和“集群包”的选择。管理节点、master 节点、引导节点或普通
Linux 运维机，只要当前 `kubectl` 能访问 InferNex 集群，使用的都是同一个包。

联网安装：

```bash
curl -fsSL https://raw.githubusercontent.com/lsjfy-open-com/infernex-agent/main/component/InferNex-Agent/scripts/install.sh | sudo bash
```

离线安装：

```bash
sha256sum --check infernex-agent-*-linux-*.tar.gz.sha256
tar -xzf infernex-agent-*-linux-*.tar.gz
cd infernex-agent-*-linux-*
sudo ./install.sh
```

安装器自动完成架构识别、kubeconfig/当前 context 检测、Bridge CRD 或 Helm/BKE
形态识别、静态二进制安装和 systemd 常驻服务配置。默认复用当前
kubectl 身份，不创建 ServiceAccount/RBAC；有合规隔离要求时才使用高级选项
`--hardened-identity`。

没有 Bridge CRD 不再导致安装失败：Agent 会进入不修改集群的 Kubernetes/Helm 模式。
该模式已经能够识别当前 kubeconfig 指向的 openFuyao 引导/管理/业务集群角色，列出
Helm Release、Deployment/StatefulSet/DaemonSet/LWS、Pod、Service，查询 Event 和
经过限长、脱敏的 current/previous Pod 日志。Bridge 专属观察和写工具都不会在此模式
发布给模型，避免 Agent 在不存在 InferNexService 的集群里反复调用无效工具。

唯一需要人工提供的是 Agent 模型接口：OpenAI 兼容 Base URL、真实 model ID 和
可选 API Key。安装完成后：

```bash
sudo infernex-agent chat
```

## Agent 如何探索

Agent 的知识库描述 InferNex 组件关系、常见故障模式、稳定变更方法和安全边界；
实时事实始终通过工具获取，而不是让模型猜测：

- openFuyao/BKE 能力、当前集群角色、Kubernetes/LWS 工作负载和 Helm Release；
- Kubernetes Event，以及明确 Pod/容器的限长、脱敏日志；
- 检测到 Bridge 后的 InferNex CRD、status 和专属拓扑；
- Bridge 已有配置和 Ready 的稳定服务；
- 后续按稳定接口接入 infernex-checker、Eagle-Eye、Prometheus 和 EvalScope；
- 本地批准后的受限写工具，以及对应验证和回退工具。

模型不持有 kubeconfig，也不能直接执行任意 Shell、YAML、镜像或 Patch。第一版的
部署只复用实际 Ready 的稳定服务或管理员已有的完整 profile；缺少基线时，Agent
会说明缺口，不凭空生成生产配置。

## Web 与持续扫描

本地 systemd 服务持续扫描，Dashboard 默认只监听 `127.0.0.1:8081`：

```bash
ssh -L 8081:127.0.0.1:8081 <管理节点>
```

然后访问 `http://127.0.0.1:8081/`。

## 文档

- [产品使用指南](docs/product-guide-zh.md)
- [离线安装](docs/offline-install-zh.md)
- [工具集与知识库设计](docs/toolsets-and-knowledge-zh.md)
- [openFuyao v26.06 对齐基线](docs/openfuyao-alignment-zh.md)
- [产品设计与边界](docs/product-design-zh.md)
- [变更保护与回退](docs/change-safety-zh.md)
- [安全边界](docs/security-boundaries-zh.md)
- [候选版本验证](docs/candidate-validation-zh.md)

Helm/Pod 安装保留给确实需要 Kubernetes 原生托管 Agent 的团队，属于高级模式，
不出现在 V1 默认 Release 下载项中。
