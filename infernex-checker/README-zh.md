
<div align="center">
  <h1 align="center">
      InferNex-Checker
  </h1>

  <p><b>InferNex部署前的环境校验工具，提前发现潜在问题，降低部署失败风险</b></p>

[![Docs](https://img.shields.io/badge/docs-live-brightgreen)](https://gitcode.com/openFuyao/sig-ai-inference/blob/main/docs/zh/ai_inference_infernex/user_guide/ai_inference_infernex_checker.md)
[![License](https://img.shields.io/badge/License-Mulan_PSL_v2-blue.svg)](../LICENSE)

</div>

<hr>

## Overview
InferNex-Checker是针对InferNex部署前的系统化环境校验工具，在`helm install`之前对硬件、Kubernetes集群和业务配置进行全方位检查，有效识别潜在风险，保障部署成功率。

**系统性环境校验**：覆盖硬件层（NPU驱动、固件、资源、网络连通性）、Kubernetes层（CoreDNS、节点状态、资源配额）及业务配置层（存储权限、版本兼容性）等关键环节。

**灵活的执行模式**：支持全量校验或按层级执行（硬件/K8s/业务配置可单独选择），适配不同部署阶段的校验需求。

**结构化校验报告**：提供终端彩色输出和JSON文件双重报告形式，包含详细错误描述、修复建议及资源使用建议，便于问题追踪和自动化处理。

## Quick Start

### Prerequisites

**支持平台**：

| 平台 | 下载链接 |
|------|---------|
| Linux AMD64 | [下载](https://static.openfuyao.cn/openFuyao/infernex/releases/download/latest/bin/linux/amd64/infernex-checker) |
| Linux ARM64 | [下载](https://static.openfuyao.cn/openFuyao/infernex/releases/download/latest/bin/linux/arm64/infernex-checker) |

**软件版本**：

Kubernetes v1.33.0+

**网络要求**：

运行主机可访问Kubernetes API Server

运行主机可通过SSH连接所有目标节点

**权限要求**：

Kubernetes集群访问权限（kubeconfig配置文件）

目标节点SSH登录权限（用户名+密码或密钥）

### Installation

1. 下载二进制文件。

```bash
# 下载二进制文件（以 AMD64 为例）
wget https://static.openfuyao.cn/openFuyao/infernex/releases/download/latest/bin/linux/amd64/infernex-checker

# 添加执行权限
chmod +x infernex-checker

# 验证安装
./infernex-checker --help
```

2. 准备配置文件。

创建`nodes.yaml`，填写目标节点信息：

```yaml
nodes:
  - name: n2                   # 节点名称（需与K8s集群中的节点名一致）
    ip: 192.168.1.10           # 节点IP
    port: 22                   # SSH端口
    user: root                 # SSH用户
    password: "your-password"  # 密码认证（与privateKeyPath二选一）
```

## Usage

**全量校验**：

按硬件层 → K8s层 → 业务配置与环境层顺序执行：

```bash
infernex-checker all --nodes nodes.yaml --values values.yaml --output result.json
```

**参数说明**：
- `--nodes`：节点信息文件路径（必填）
- `--values`：InferNex部署配置文件路径（必填）
- `--kubeconfig`：Kubernetes配置文件路径（可选，默认`~/.kube/config`）
- `--output`：JSON结果文件输出路径（可选）
- `--log`：日志文件路径（可选，默认`./infernex-checker.log`）

**分层校验**：

```bash
# 仅硬件层
infernex-checker hardware --nodes nodes.yaml

# 仅K8s层
infernex-checker k8s --nodes nodes.yaml

# 仅业务配置与环境层
infernex-checker config-env --nodes nodes.yaml --values values.yaml
```

## Output

**终端输出**：

```
=== InferNex Checker ===

[Hardware Layer - Single Node: n2]
  ✅ H-01  NPU driver and firmware installed
  ✅ H-02  Ascend Device Plugin Running and NPU resource registered
  ✅ H-03  NPU model: 910B4
  ℹ️ H-04  NPU available: 4/8
  ✅ H-05  Key host files and directories are complete
  ✅ H-06  hccn.conf configuration is correct
  ✅ H-07  Step 1: NIC TLS switch states are consistent (no_cert)
  ✅ H-07  Step 2: Single-node NIC-to-NIC connectivity is normal

[Hardware Layer - Cross-Node]
  ⏭️ H-08  Only 1 910-series node(s) passed single-node checks, skipping cross-node check (at least 2 required)

[K8s Layer]
  ✅ K-01  CoreDNS Running, service domain names are resolvable
  ✅ K-02  n2 Ready
  ✅ K-03  n2 has no taints
  ℹ️ K-04  n2  Allocatable resources: CPU 256, Memory 1055115824Ki
    -> Please confirm this meets Infernex deployment requirements; if sufficient, consider releasing occupied resources or adjusting workloads

[Config-Env Layer: n2]
  ✅ B-01  /home/llm_cache directory exists and is writable
  ✅ B-02  Driver 25.5.0 is compatible with image v0.13.0

────────────────────────────────
Result: 12 passed, 0 failed, 2 info

ℹ️ Info items:
  H-04 [n2]  NPU available: 4/8
  K-04 [n2]  n2  Allocatable resources: CPU 256, Memory 1055115824Ki

✅ Environment check passed, ready to deploy InferNex
```

**JSON输出**：

结构化JSON文件包含详细的校验结果、错误描述和修复建议，便于自动化处理和问题追踪。使用`--output`参数指定输出路径。
