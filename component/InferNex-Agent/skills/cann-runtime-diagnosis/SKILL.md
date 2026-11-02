---
name: cann-runtime-diagnosis
description: Diagnose CANN Runtime and Ascend NPU inference failures, including initialization/version errors, plog and asynchronous error localization, error 507014, device task failures, OOM, HCCL timeouts, hangs, and cross-rank failures. Use for vLLM-Ascend, PyTorch NPU, CANN Runtime, GE, ACL, HCCL, or NPU service startup/runtime incidents.
---

# CANN Runtime diagnosis

Use an evidence-first workflow. Do not prescribe a restart before preserving transient Host, Device, container, Kubernetes event, and previous-container evidence.

1. Establish the exact layer and timeline: workload/framework, torch-npu or runtime, GE/op, HCCL/HiXL, driver/firmware, hardware.
2. Record node and device identity, hardware generation, driver/firmware/CANN/torch-npu/vLLM-Ascend versions, image digest, rank mapping, and the first failing timestamp.
3. Search logs around the first error rather than the last propagated exception. Correlate all ranks and nodes by timestamp; a waiting rank is often a victim of an earlier peer failure.
4. Classify the symptom using `references/signatures.md`. Read `references/evidence.md` for the minimum evidence set and safe collection guidance.
5. State observations separately from hypotheses. Rank hypotheses and name the evidence that would falsify each one.
6. Recommend the smallest reversible validation. Any configuration change remains subject to InferNex policy, approval, snapshot, verification, and rollback.

Never treat a larger timeout as proof of a network problem. Never infer a faulty NPU solely from a framework-level exception. Do not expose credentials, prompts, model data, memory contents, or complete raw logs in the report.
