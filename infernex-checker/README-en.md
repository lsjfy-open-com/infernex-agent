
<div align="center">
  <h1 align="center">
      InferNex-Checker
  </h1>

  <p><b>Pre-deployment environment validation tool for InferNex, identifying potential issues in advance to reduce deployment failure risks</b></p>

[![Docs](https://img.shields.io/badge/docs-live-brightgreen)](https://gitcode.com/openFuyao/sig-ai-inference/blob/main/docs/en/ai_inference_infernex/user_guide/ai_inference_infernex_checker.md)
[![License](https://img.shields.io/badge/License-Mulan_PSL_v2-blue.svg)](../LICENSE)

</div>

<hr>

## Overview
InferNex-Checker is a systematic pre-deployment environment validation tool for InferNex. It performs comprehensive checks on hardware, Kubernetes clusters, and business configurations before `helm install`, effectively identifying potential risks and ensuring deployment success.

**Systematic Environment Validation**: Covers key aspects across the hardware layer (NPU driver, firmware, resources, network connectivity), Kubernetes layer (CoreDNS, node status, resource quotas), and business configuration layer (storage permissions, version compatibility).

**Flexible Execution Modes**: Supports full validation or layer-by-layer execution (hardware/K8s/business configuration can be selected individually), adapting to validation needs at different deployment stages.

**Structured Validation Reports**: Provides dual report formats — terminal colored output and JSON files — including detailed error descriptions, remediation suggestions, and resource usage recommendations, facilitating issue tracking and automated processing.

## Quick Start

### Prerequisites

**Supported Platforms**:

| Platform | Download Link |
|------|---------|
| Linux AMD64 | [Download](https://static.openfuyao.cn/openFuyao/infernex/releases/download/latest/bin/linux/amd64/infernex-checker) |
| Linux ARM64 | [Download](https://static.openfuyao.cn/openFuyao/infernex/releases/download/latest/bin/linux/arm64/infernex-checker) |

**Software Version**:

Kubernetes v1.33.0+

**Network Requirements**:

The host running the tool must be able to access the Kubernetes API Server

The host running the tool must be able to connect to all target nodes via SSH

**Permission Requirements**:

Kubernetes cluster access permissions (kubeconfig configuration file)

Target node SSH login permissions (username + password or key)

### Installation

1. Download the binary file.

```bash
# Download the binary file (AMD64 example)
wget https://static.openfuyao.cn/openFuyao/infernex/releases/download/latest/bin/linux/amd64/infernex-checker

# Add execute permission
chmod +x infernex-checker

# Verify installation
./infernex-checker --help
```

2. Prepare the configuration file.

Create `nodes.yaml` and fill in target node information:

```yaml
nodes:
  - name: n2                   # Node name (must match the node name in the K8s cluster)
    ip: 192.168.1.10           # Node IP
    port: 22                   # SSH port
    user: root                 # SSH user
    password: "your-password"  # Password authentication (choose one between password and privateKeyPath)
```

## Usage

**Full Validation**:

Execute in order: hardware layer → K8s layer → business configuration and environment layer:

```bash
infernex-checker all --nodes nodes.yaml --values values.yaml --output result.json
```

**Parameter Description**:
- `--nodes`: Node information file path (required)
- `--values`: InferNex deployment configuration file path (required)
- `--kubeconfig`: Kubernetes configuration file path (optional, default `~/.kube/config`)
- `--output`: JSON result file output path (optional)
- `--log`: Log file path (optional, default `./infernex-checker.log`)

**Layer-by-layer Validation**:

```bash
# Hardware layer only
infernex-checker hardware --nodes nodes.yaml

# K8s layer only
infernex-checker k8s --nodes nodes.yaml

# Business configuration and environment layer only
infernex-checker config-env --nodes nodes.yaml --values values.yaml
```

## Output

**Terminal Output**:

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

───────────────────────────────
Result: 12 passed, 0 failed, 2 info

ℹ️ Info items:
  H-04 [n2]  NPU available: 4/8
  K-04 [n2]  n2  Allocatable resources: CPU 256, Memory 1055115824Ki

✅ Environment check passed, ready to deploy InferNex
```

**JSON Output**:

The structured JSON file contains detailed validation results, error descriptions, and remediation suggestions, facilitating automated processing and issue tracking. Use the `--output` parameter to specify the output path.
