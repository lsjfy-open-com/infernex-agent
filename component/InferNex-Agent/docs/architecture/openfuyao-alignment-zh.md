# openFuyao v26.06 对齐基线

本文记录 InferNex Agent 设计所依据的 openFuyao 官方部署与推理文档，防止后续实现再次
把某一种可选部署形态误当成整个产品。

## 已核对的官方资料

- [Linux 后端安装业务集群](https://gitcode.com/openFuyao/sig-installation/blob/openFuyao-v26.06/docs/zh/installation_guide/service_cluster_deployment_guide_%28linux_backend%29.md)
- [InferNex 主 Chart 使用指南](https://gitcode.com/openFuyao/sig-ai-inference/blob/openFuyao-v26.06/docs/zh/ai_inference_infernex/user_guide/ai_inference_infernex.md)
- [InferNex Bridge 使用指南](https://gitcode.com/openFuyao/sig-ai-inference/blob/openFuyao-v26.06/docs/zh/ai_inference_infernex/user_guide/ai_inference_infernex_bridge.md)
- [infernex-checker 使用指南](https://gitcode.com/openFuyao/sig-ai-inference/blob/openFuyao-v26.06/docs/zh/ai_inference_infernex/user_guide/ai_inference_infernex_checker.md)
- [ResourceScalingGroup 使用指南](https://gitcode.com/openFuyao/sig-ai-inference/blob/openFuyao-v26.06/docs/zh/ai_inference_elastic_scaling_system/ai_inference_resourcescalinggroup/user_guide/ai_inference_resourcescalinggroup.md)

## 对 Agent 设计的直接约束

1. 引导 K3s、管理集群和业务集群是不同的 Kubernetes API 视角；同一台服务器可以同时
   承载多个角色。一个 kubeconfig 只代表当前选中的一个 API Server，不能因为 Agent
   安装在 master/引导节点就推断它正在观察业务集群。
2. BKECluster、BKENode、bkeagent 和 Cluster API 是集群生命周期权威。Agent 第一版只
   识别这些能力和证据，不另造节点安装控制器。
3. InferNex 主 Chart 是正常的模型服务部署入口，可产生 Deployment、StatefulSet、
   LeaderWorkerSet、Pod、Service、Gateway/HTTPRoute 和弹性伸缩资源。Bridge/KServe 是
   可选入口，缺少 InferNexService CRD 不能被解释为“没有安装 InferNex”。
4. 应用生命周期以 Helm release/history/values/manifest 和 Kubernetes 实际资源为证据。
   未来任何自动 upgrade/rollback 都必须先保存安装前状态、展示变更、取得批准并验证
   Ready；第一版尚不向模型开放 Helm 写操作。
5. NPU 驱动/固件、device-plugin、HCCS/RoCE/RDMA、CoreDNS、模型路径和 Driver/CANN
   兼容性已有 `infernex-checker`，Agent 应调用和结构化其报告，而不是重写一套检测器。
6. openFuyao 的日志、Event、Prometheus/ServiceMonitor、Eagle-Eye 和 EvalScope 各有
   权威数据源。Agent 的职责是关联、解释、形成报告和闭环，而不是替换这些组件。

## 当前候选版已经落地

- 输出当前 API Server、可见 openFuyao 命名空间、集群角色和 BKE/LWS/Gateway/Bridge/
  KServe/RSG/ServiceMonitor 能力；
- 汇总节点架构、OS、Kubelet、NPU/GPU 可分配资源和 Pod 健康；
- 列出原生 Deployment、StatefulSet、DaemonSet、LeaderWorkerSet、Pod、Service 及 Helm
  关联，读取有界 Event、current/previous Pod 日志和 Helm Release 元数据；
- 无 Bridge 时只向模型发布通用工具；确认 Bridge 存在时才增加 InferNexService 工具；
- 安装默认不创建 Agent Pod/CRD/RBAC，不修改现有业务资源。

## 后续接口，不在当前候选版冒充完成

- `infernex-checker` 的受控执行、输入发现和结构化报告；
- Gateway/HTTPRoute、ResourceScalingGroup/ElasticScaler/Tidal 的专属状态适配器；
- Helm values/history/manifest 快照、差异预览、批准式 install/upgrade/rollback；
- 推理 warmup、EvalScope 单轮/多轮评测和可视化报告；
- Prometheus、Loki/Eagle-Eye 的指标与跨节点时间线关联。

这些能力优先复用官方 CLI、CRD 和 API；不会通过开放任意 shell、任意 YAML 或任意
Kubernetes patch 来制造“通用能力”。
