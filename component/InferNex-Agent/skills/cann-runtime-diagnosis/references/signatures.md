# CANN symptom signatures

## Initialization and version failures

Signals include `aclInit`, `aclrtSetDevice`, missing shared objects, incompatible runtime/driver, or failure before model loading. Verify environment setup and the complete version tuple on every node before changing application parameters.

## Asynchronous device failure and 507014

Signals include `AclrtSynchronizeDeviceWithTimeout`, `507014`, `aicore timeout`, `fftsplus`, or a kernel task error. The synchronization call may only surface an earlier asynchronous failure. Locate the first device/plog error and correlate the preceding operator, stream and rank activity. Candidate classes include invalid operator input/shape, rank divergence causing a communication wait, invalid KV/block index, device error, or a genuinely long-running task.

## HCCL timeout or hang

Signals include `HCCL_CONNECT_TIMEOUT`, `HCCL_EXEC_TIMEOUT`, `AllReduce`, `AllToAll`, or peers waiting indefinitely. First check whether one rank crashed earlier or entered a different collective sequence. Then check group/rank construction, interface/IP selection, cross-node connectivity, port state, version consistency, and buffer/resource limits. Increasing a timeout is only a bounded experiment after reachability and rank symmetry are demonstrated.

## NPU memory pressure

Signals include `out of memory`, allocation failure, or abrupt failure near KV-cache/model initialization. Account for weights, KV cache, activations, communication buffers and retained tensors. Compare all ranks: asymmetric allocation often indicates mapping or workload imbalance. Reduce one dimension at a time and verify that memory is released between attempts.

## Shape/operator constraint

Signals include shape mismatch, unsupported dtype/layout, or device task failure reproducible for a specific request. Record the minimal input metadata without sensitive payloads and isolate the first operator. Do not attribute every device timeout to hardware.

## Unknown or suspected hardware error

Preserve Host/Device evidence and hardware health information. Mark the conclusion as suspected until device-level evidence supports it. Avoid repeated destructive retries that erase the original scene.
