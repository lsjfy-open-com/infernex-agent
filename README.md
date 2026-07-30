# InferNex Agent

[English](README-en.md) | [简体中文](README-zh.md)

[![InferNex Agent CI](https://github.com/lsjfy-open-com/infernex-agent/actions/workflows/infernex-agent.yaml/badge.svg)](https://github.com/lsjfy-open-com/infernex-agent/actions/workflows/infernex-agent.yaml)
[![License](https://img.shields.io/badge/License-Mulan_PSL_v2-blue.svg)](LICENSE)

This repository keeps the InferNex project structure intact and adds
**InferNex Agent** as a management-plane component. The Agent exposes typed
InferNex domain tools to MCP-compatible runtimes while reusing the existing
`InferNexService` API and InferNex Bridge status.

## Agent v0.1

The first release is deliberately read-only and publishes four tools:

- `infernex_list_services`
- `infernex_inspect_service`
- `infernex_get_topology`
- `infernex_get_events`

The Agent does not expose arbitrary shell or `kubectl` execution. Its default
Helm configuration uses namespace-scoped, read-only RBAC and has no permission
to read Secrets or mutate workloads.

## Documentation

- [Agent overview, local development, and deployment](component/InferNex-Agent/README.md)
- [Architecture and component boundaries](component/InferNex-Agent/docs/architecture.md)
- [InferNex English documentation](README-en.md)
- [InferNex 中文文档](README-zh.md)

## Validation

The repository workflow runs Go race tests and vet, Helm lint/render checks,
builds the Agent image, creates a real Kind cluster, installs the InferNex and
LeaderWorkerSet CRDs, verifies negative RBAC cases, and exercises all four MCP
tools.

No external Kubernetes environment is required for the repository CI.
