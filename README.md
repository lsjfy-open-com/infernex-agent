# InferNex Agent

[English](README-en.md) | [简体中文](README-zh.md)

[![InferNex Agent CI](https://github.com/lsjfy-open-com/infernex-agent/actions/workflows/infernex-agent.yaml/badge.svg)](https://github.com/lsjfy-open-com/infernex-agent/actions/workflows/infernex-agent.yaml)
[![License](https://img.shields.io/badge/License-Mulan_PSL_v2-blue.svg)](LICENSE)

This repository keeps the InferNex project structure intact and adds
**InferNex Agent** as a management-plane component. The Agent exposes typed
InferNex domain tools to MCP-compatible runtimes while reusing the existing
`InferNexService` API and InferNex Bridge status.

## Agent v0.2

The default installation remains deliberately read-only and publishes four
observation tools:

- `infernex_list_services`
- `infernex_inspect_service`
- `infernex_get_topology`
- `infernex_get_events`

An explicit, namespace-scoped deployment mode adds two catalog tools:

- `infernex_deploy_model`
- `infernex_delete_model`

The deployment input is limited to a namespace, instance name, fixed catalog
ID, and explicit confirmation. It cannot accept arbitrary images, commands,
URLs, YAML, shell, or `kubectl`. The first catalog entry is a CPU-only
SmolLM2-135M Q4 model for free Kind testing. The Agent creates only the
canonical `InferNexService`; InferNex Bridge remains responsible for the
Deployment, Service, status, and garbage collection.

The default Helm configuration has deployment mode disabled, uses
namespace-scoped read-only RBAC, and has no permission to read Secrets or
mutate workloads.

## Documentation

- [Agent overview, local development, and deployment](component/InferNex-Agent/README.md)
- [Architecture and component boundaries](component/InferNex-Agent/docs/architecture.md)
- [InferNex English documentation](README-en.md)
- [InferNex 中文文档](README-zh.md)

## Validation

The repository workflow runs Go race tests and vet, Helm lint/render checks,
builds the Agent and InferNex Bridge images, creates a real Kind cluster, and
exercises all six MCP tools. It also asks the Agent to deploy the catalog model,
waits for Bridge reconciliation, sends an OpenAI-compatible chat-completion
request to llama.cpp, observes the resulting topology through the Agent, and
deletes the service through the guarded tool.

No external Kubernetes environment is required for the repository CI.
