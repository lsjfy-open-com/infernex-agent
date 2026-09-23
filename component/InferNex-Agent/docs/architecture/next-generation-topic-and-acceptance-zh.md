# 课题简介：面向异构 Kubernetes 推理集群的跨环境自动部署 Agent

研究目标：面向不同客户的推理部署环境，构建能够识别环境、规划实例规格、自动生成并执行部署方案、验证上线效果的 Agent，并支持部署后的扩缩、持续优化、版本升级与回退。故障分析服务于部署失败处理和上线保障。

本文用于课题介绍与对外引用，只说明背景、痛点、期望结果和验收类别。环境规格、案例输入、日志数据、测试步骤及量化门槛见独立的[环境与两阶段验收细则](https://github.com/lsjfy-open-com/infernex-agent/blob/develop/component/InferNex-Agent/docs/development/next-generation-acceptance-spec-zh.md)。

## 1. 背景信息

企业推理服务运行在不同的 Kubernetes 平台、推理框架和 GPU／NPU 硬件上，需要将模型、可用资源与服务等级目标（SLO）转换为可运行、可验收的部署。参考[智谱基础设施 Agent 案例](https://z.ai/blog/glm-built-its-inference-infrastructure)与 [DeepSeek Harness](https://deepseek.com/harness/en/) 的能力组织思路，本课题研究跨环境自动部署，以及部署后的扩缩、性能优化、版本升级与回退。

## 2. 痛点描述

客户环境差异大，部署前需要人工识别平台、核对依赖并选择实例规格；模板、参数和启动步骤分散，换环境往往要重复适配。容器启动后，又缺少对真实请求、流量分布和性能达标的统一验收。联网与离线物料交付不一致，配置、镜像、外部权重引用及运行快照缺少关联，也使升级难以复现、回退目标不清晰。

## 3. 期望结果与建议

构建以轻量环境模型和可复用能力包为基础的部署 Agent，完成环境识别、规格规划、模板生成、部署执行和上线验收，并支持联网／离线制品管理、Git＋快照版本记录及受控回退。建议在约定测试集上，自主部署与升级成功率均 ≥95%；同模型、同资源和同负载下，相对健康部署基线，有效吞吐下降 <20%，首 token 延迟（TTFT）与后续每 token 平均耗时（TPOT）的 p95 增幅各 <20%，且满足业务 SLO。人工修复不计自主成功，影响集群的操作须获批准；样本、耗时、质量和恢复门槛按[验收细则](https://github.com/lsjfy-open-com/infernex-agent/blob/develop/component/InferNex-Agent/docs/development/next-generation-acceptance-spec-zh.md)执行。

## 4. 验收类别与阶段

验收分为两个阶段：**第一阶段验证部署基础与受管发布，第二阶段验证跨环境自动部署与生命周期管理**。阶段之间沿用可比较的环境、数据和证据格式，以实际测试记录说明能力提升。

| 验收类别 | 第一阶段重点 | 第二阶段重点 |
| --- | --- | --- |
| 环境识别与适配 | 安装接入、权限边界、资源与配置发现 | 多种管理框架识别、管理归属判断与未知环境处理 |
| 部署、资源与流量 | 实例和资源状态可观察，受管候选可验证 | 异构规格规划、跨平台部署、真实分流与摘流恢复 |
| 上线验收与 SLO | 请求检查、基础对照与部署失败采证 | 业务成功率、性能达标、部署后优化与退化回退 |
| 升级、补丁与版本回退 | 变更记录、配置版本和状态快照可追溯 | 联网／离线制品一致校验、组合版本恢复与业务恢复验证 |
| 能力构建、复用与治理 | 工具和知识有明确来源及权限边界 | 新适配能力构建、独立测试、版本复用与子任务约束 |
| 可视化、交付与可复现性 | 状态、配置来源、报告和证据可理解 | 完整实验和版本过程可查看，环境及验收结果可重现 |

核心指标包括部署成功率、首次部署耗时、人工介入次数、跨环境适配成本、SLO 达标率和升级回退成功率；识别准确性、执行边界与证据完整性作为支撑要求。上述核心目标对应的完整样本要求、统计口径、阈值和不适用条件统一维护在[验收细则](https://github.com/lsjfy-open-com/infernex-agent/blob/develop/component/InferNex-Agent/docs/development/next-generation-acceptance-spec-zh.md)，不以演示效果替代量化验收。

## 5. 出题方配套与交付要求

出题方应提供可复现的环境搭建指南、固定版本与资源清单、部署需求与模板样例、公开练习案例、脱敏部署日志、独立保管的验收案例及判定依据，并明确联网范围、模型服务、硬件和实验预算。涉及真实加速卡或网络设备的指标，应提供相应设备与数据；合成日志和普通容器实验只能验证其覆盖的逻辑，不能替代真实硬件效果。

承接方交付可运行组件、领域模型与适配契约、能力包及测试、补丁与恢复记录、可视化结果和逐项验收报告。对于尚未提供的环境或数据，应明确记录缺口，不能将“未测”记为“通过”。

引用入口：

- [环境与两阶段验收细则：环境矩阵、案例、量化门槛、证据与判定规则](https://github.com/lsjfy-open-com/infernex-agent/blob/develop/component/InferNex-Agent/docs/development/next-generation-acceptance-spec-zh.md)
- [实验室搭建与案例数据指南：准备、运行、校验和清理](https://github.com/lsjfy-open-com/infernex-agent/blob/develop/component/InferNex-Agent/docs/guides/next-generation-acceptance-lab-zh.md)
