# InferNex-Bridge 技术规格说明文档

## 0 文档性质

本文件为 InferNex-Bridge 的技术规格与责任边界契约，说明本 release 与 KServe 的兼容范围、昇腾全栈推理能力及 KServe 链路上的 Mutating Webhook 适配行为。设计依据见提案 [OFEP-0040-InferNex 接入 KServe 适配层](https://gitcode.com/openFuyao/ofep/blob/main/ofeps/sig-ai-inference/0040-ofep-Infernex%E6%8E%A5%E5%85%A5kserve%E9%80%82%E9%85%8D%E5%B1%82.md)。集群安装、`InferNexService` 入口配置及日常运维见 [InferNex Bridge 用户手册](https://gitcode.com/openFuyao/sig-ai-inference/blob/openFuyao-v26.06/docs/zh/ai_inference_infernex/user_guide/ai_inference_infernex_bridge.md)。

---

## 1 部署规格

本规格说明 InferNex-Bridge 与 KServe 的支持版本，具体如下。


| KServe组件    | 版本     |
| ----------- | ------ |
| LLMISVC 控制器 | 0.17.0 |
| LLMISVC 控制器 | 0.18.0 |
| LLMISVC 控制器 | 0.19.0 |


KServe 侧安装及集群环境前提见 [LLMInferenceService Prerequisites](https://kserve.github.io/website/docs/0.17/install/llmisvc-install#prerequisites)（0.18 / 0.19 与 0.17 一致，替换文档版本号即可）。


| InferNex版本 |
| ---------- |
| 26.6.0     |


InferNex 整体部署规格见 [InferNex 规格 #42](https://gitcode.com/openFuyao/InferNex/issues/42)。默认组件镜像见附录 A。

---

## 2 部署场景

InferNex-Bridge 面向昇腾（Ascend）NPU 全栈推理场景，支持在 `LLMInferenceService`（`infernex.io/runtime=true`）与 `InferNexService`（无 `spec.sourceRef`）双入口下部署 InferNex。不限定具体的 TP/DP 组合、节点分布或编排方式，覆盖聚合（Aggregate）与 PD 分离（Disaggregated）等常见部署形态，用户可按业务需要配置并行规模与拓扑。

对应示例位于 [config/examples/llmisvc](https://gitcode.com/openFuyao/InferNex/tree/release-26.6.0/component/InferNex-Bridge/config/examples/llmisvc) 与 [config/examples/insvc](https://gitcode.com/openFuyao/InferNex/tree/release-26.6.0/component/InferNex-Bridge/config/examples/insvc)。

---

## 3 Mutating Admission Webhook 补丁说明

InferNex-Bridge 在 KServe 链路上注册 Mutating Admission Webhook（类型名：`LLMInferenceService` Mutating）。用户创建或更新带 InferNex 运行时标签的 `LLMInferenceService` 时触发，不修改 `LLMInferenceService` 对象本身，而是对集群中 KServe 预设的 `LLMInferenceServiceConfig` 模板做一次性适配补丁，使推理后端与路由符合 InferNex 运行要求。

### 3.1 触发条件与修改对象

Mutating Admission Webhook 在创建或更新带有 `infernex.io/runtime=true` 标签的 `LLMInferenceService`（v1alpha1/v1alpha2）时起作用。KServe 预设 `LLMInferenceServiceConfig` 须安装在 `kserve` 命名空间（见第 1 节 KServe 安装前提），否则 Webhook 无法完成修改。

实际修改的是已安装在集群中 KServe `LLMInferenceServiceConfig` 预设模板，依次在 `kserve` 命名空间中查找下列 7 个 Config，并进行补丁修改：

- `kserve-config-llm-template`
- `kserve-config-llm-worker-data-parallel`
- `kserve-config-llm-decode-template`
- `kserve-config-llm-decode-worker-data-parallel`
- `kserve-config-llm-prefill-template`
- `kserve-config-llm-prefill-worker-data-parallel`
- `kserve-config-llm-scheduler`

每个 Config 首次修改成功后写入注解 `infernex.io/llm-inference-service-config-mutated=infernex`；已带该注解的 Config 不会再次执行修改。升级 Bridge 后若需重新补丁（例如在增加 scheduler tokenizer 处理逻辑之前已修改过的集群），须删除该注解。

### 3.2 修改内容

补丁对象仍为 3.1 节所列 7 个 `LLMInferenceServiceConfig`。以下摘录自 KServe 预设 `kserve/config/llmisvcconfig/config-llm-decode-template.yaml` 与 `config-llm-scheduler.yaml`（[上游仓库](https://github.com/kserve/kserve/tree/master/config/llmisvcconfig)），展示补丁前后关键字段差异（省略无关字段）。

#### 3.2.1 Workload 类 Config（前 6 个，不含 scheduler）

清空 KServe 预设里的 llm-d initContainer。`capabilities.drop: ALL` 予以保留（不再剥离），使推理 workload 兼容 restricted Pod Security Admission；NPU 设备访问改由 pod 级 `supplementalGroups` 注入（单独实现），不再依赖剥 drop ALL 来获取 DAC_OVERRIDE。

`securityContext.capabilities.drop`：workload 容器的 securityContext 补丁前后保持不变，`drop: [ALL]` 等字段原样保留（`spec.template`、`spec.prefill.template`、`spec.worker`、`spec.prefill.worker` 路径上的容器均不再被改动 securityContext）。

`initContainers`

补丁前（decode 模板 `spec.template.initContainers`）：

```yaml
initContainers:
  - name: llm-d-routing-sidecar
    image: ghcr.io/llm-d/llm-d-routing-sidecar:v0.7.1
    command:
      - /app/pd-sidecar
      - --port=8000
      - --vllm-port=8001
    # ... llm-d PD 路由 sidecar 其余配置
```

补丁后：

```yaml
initContainers: []
```

#### 3.2.2 `kserve-config-llm-scheduler`（`spec.router.scheduler.template`）

去掉 scheduler Pod 里 llm-d 的启动命令、探针和 tokenizer 相关卷挂载，只保留容器骨架和 `tokenizer-tmp`，供 Hermes Router 与 LLMISVC 侧模板覆盖。

`main` 容器

补丁前：

```yaml
- name: main
  image: ghcr.io/llm-d/llm-d-inference-scheduler:v0.7.1
  command:
    - /app/epp
    - --pool-name
    - "{{ ChildName .ObjectMeta.Name `-inference-pool` }}"
    - --grpc-port
    - "9002"
  volumeMounts:
    - name: tls-certs
      mountPath: /var/run/kserve/tls
    - name: tokenizer-uds
      mountPath: /tmp/tokenizer
```

补丁后：

```yaml
- name: main
  image: ghcr.io/llm-d/llm-d-inference-scheduler:v0.7.1
  # command 已移除，由 Hermes 镜像入口与 LLMISVC 侧参数覆盖
  volumeMounts:
    - name: tls-certs
      mountPath: /var/run/kserve/tls
```

`tokenizer` 容器与卷

补丁前：

```yaml
- name: tokenizer
  image: ghcr.io/llm-d/llm-d-uds-tokenizer:v0.7.1
  ports:
    - containerPort: 8082
      name: health
  livenessProbe:
    httpGet:
      path: /healthz
      port: 8082
  volumeMounts:
    - name: tokenizer-tmp
      mountPath: /tmp
    - name: tokenizer-cache
      mountPath: /.cache
    - name: tokenizer-uds
      mountPath: /tmp/tokenizer
volumes:
  - name: tokenizer-uds
    emptyDir: {}
  - name: tokenizer-tmp
    emptyDir: {}
  - name: tokenizer-cache
    emptyDir: {}
```

补丁后：

```yaml
- name: tokenizer
  image: ghcr.io/llm-d/llm-d-uds-tokenizer:v0.7.1
  # 保留容器骨架；移除 llm-d 探针、ports、env 等，供 LLMISVC / Hermes 覆盖
  volumeMounts:
    - name: tokenizer-tmp
      mountPath: /tmp
volumes:
  - name: tokenizer-tmp
    emptyDir: {}
```

### 3.3 影响

#### 3.3.1 安全问题注意

KServe 默认会把推理容器权限压到最低，有利于安全，但 Ascend / Hermes 在 NPU 等环境下无法正常启动。补丁去掉这一限制后，InferNex 推理 Pod 可以正常拉起。相比 KServe 原始预设，Pod 权限约束会略松；若集群有等保、强隔离等合规要求，可在 `LLMInferenceService` 或 `LLMInferenceServiceConfig` 里自行加回合适的安全上下文。

> 后续规划：openFuyao社区已关注到该权限放宽问题，后续版本将通过更精细的能力白名单机制替代当前的方案，在兼容性与安全性之间取得更好平衡。

#### 3.3.2 sidecar 清理说明

KServe 默认会在 decode 等 Workload Pod 里加 llm-d 路由 sidecar（`initContainers`），用于 PD 分离等场景的请求转发。InferNex 不走 llm-d 链路，补丁会清空这些 initContainer。补丁后 KServe 预设不会再自动挂载 llm-d sidecar；若业务仍依赖 LLM-D 的 PD 路由等能力，须在 `LLMInferenceService` 或 `LLMInferenceServiceConfig` 里自行写回 initContainer，并按 3.1 节重新触发补丁。

> 后续规划：已向 KServe 提交 Issue 跟踪该兼容性诉求，后续版本将尽可能避免由 Webhook 修改该模板，改为通过其他机制实现平滑兼容。

#### 3.3.3 scheduler 配置修改说明

KServe 默认的 scheduler Pod 已按 llm-d 配好启动命令、探针和 tokenizer 卷。补丁删掉 llm-d 专用项，只留容器骨架和 `tokenizer-tmp`，路由与调度改由 Hermes Router 及 `LLMInferenceService` 侧 `router.scheduler.template` 决定，不再沿用 llm-d 默认行为。LLMISVC 侧宜显式声明 `tokenizer-tmp`、`tokenizer-cache` 等卷（与 InferNex Chart 示例一致），否则 tokenizer 容器可能因缺少可写目录而启动失败。scheduler Pod 上的 `capabilities.drop: ALL` 同样予以保留（与 Workload 类 Config 一致，均不再剥离 drop ALL）。

> 后续规划：openFuyao社区正在推进 Hermes Router 对 KServe scheduler 模板的原生适配工作，待适配完成后，Webhook 对 scheduler 的修改操作将不再需要，由 Hermes Router 直接覆盖配置。

---

## 附录 A：默认镜像

InferNex `26.6.0` release 随 Bridge Chart 交付的默认组件镜像如下。


| 组件                                     | 版本/标签   | 镜像                                                                 |
| -------------------------------------- | ------- | ------------------------------------------------------------------ |
| vLLM-Ascend                            | v0.18.0 | `hub.oepkgs.net/openfuyao/ascend/vllm-ascend`                      |
| mooncake-master                        | v0.18.0 | `hub.oepkgs.net/openfuyao/ascend/vllm-ascend`                      |
| mooncake-metadata                      | 8.6.1   | `hub.oepkgs.net/openfuyao/redis`                                   |
| hermes-router                          | 26.6.0  | `cr.openfuyao.cn/openfuyao/hermes-router`                          |
| cache-indexer                          | 26.6.0  | `cr.openfuyao.cn/openfuyao/cache-indexer`                          |
| proxy-server                           | 26.6.0  | `cr.openfuyao.cn/openfuyao/proxy-server`                           |
| elastic-scaler                         | 26.6.0  | `cr.openfuyao.cn/openfuyao/elastic-scaler`                         |
| tidal                                  | 26.6.0  | `cr.openfuyao.cn/openfuyao/tidal`                                  |
| resource-scaling-group                 | 26.6.0  | `cr.openfuyao.cn/openfuyao/resource-scaling-group`                 |
| eagle-eye-hardware-monitor             | 26.6.0  | `cr.openfuyao.cn/openfuyao/eagle-eye-hardware-monitor`             |
| eagle-eye-hardware-diagnosis           | 26.6.0  | `cr.openfuyao.cn/openfuyao/eagle-eye-hardware-diagnosis`           |
| eagle-eye-network-performance-exporter | 26.6.0  | `cr.openfuyao.cn/openfuyao/eagle-eye-network-performance-exporter` |


