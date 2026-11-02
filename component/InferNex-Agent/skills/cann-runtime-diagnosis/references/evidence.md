# CANN evidence checklist

Collect only what current permissions expose. The Agent does not gain shell or device privileges from this Skill.

## Environment identity

- Node, Pod/container, restart count, current/previous logs, image name and immutable digest.
- Accelerator model and logical/physical device mapping; `npu-smi info` output when operator-provided.
- Driver, firmware, CANN Toolkit/Kernels, torch-npu, framework and plugin versions on every node.
- Relevant environment variable names and non-secret values, especially rank/network/timeout/logging configuration.
- Model parallel topology: world size, TP/PP/DP/EP, rank-to-node and rank-to-device mapping.

## Timeline

- Preserve the first device/runtime error and at least a bounded interval before it.
- Correlate Host application logs, CANN plog/device logs, Kubernetes events and peer-rank logs.
- Prefer local evidence grep and narrow reads. Keep probe traffic filtered unless it is causal.
- Report timestamps with timezone and note clock skew; do not sort unrelated clocks as if synchronized.

## Safe diagnosis sequence

1. Confirm that all nodes run a compatible software tuple.
2. Find the first failing rank and first low-level error.
3. Determine whether later ranks are computing, waiting in collective communication, or already terminated.
4. Separate deterministic configuration/input failures from intermittent network/device failures.
5. Preserve transient evidence before restart. Restart is a recovery action, not a diagnosis.

For CANN Runtime FAQ navigation and plog/asynchronous-error guidance, consult the pinned sources in `sources.md`.
