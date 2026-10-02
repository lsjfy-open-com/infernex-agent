# HiXL layered triage

## 1. Initialization and lifecycle

Confirm device context is set before initialization and that initialize/finalize pairs are respected. Register memory before connection when remote access requires descriptor exchange. During teardown, stop transfers, disconnect peers, deregister memory and then destroy/finalize resources. A server leaving while clients still transfer can make peer errors look like network faults.

## 2. LocalCommRes and backend selection

Record whether resources are automatic or explicit and the configuration version. Verify that protocol and placement match hardware and intended Host/Device path. Unexpected fallback or endpoint-matching logs often mean the expected LocalCommRes was absent, rejected or filtered. Do not delete Host endpoints if the workload actually needs Host communication.

For Atlas A2, establish whether HCCS or RoCE is intended and validate the matching rank/network configuration. For A5/Ascend 950, distinguish UBC/UBoE/UBG and Host versus Device placement; do not apply A5-only EID/UB guidance to A2.

## 3. Endpoint and reachability

Compare both ends: engine/cluster identity, listen address and port, protocol, device, endpoint, EID, network instance and route. Validate physical/link state before adjusting software timeouts. Check port conflicts and firewall/security configuration without disabling controls as a diagnostic shortcut.

## 4. Memory registration

Check memory type, address/length validity, alignment, physical-contiguity requirements where applicable, resource count and device/Host capacity. Excessive registered regions can consume device resources and extend connection time. Ensure the memory remains valid until all remote access completes.

## 5. Connect and transfer

Distinguish connect timeout from transfer timeout. Confirm both endpoints completed initialization, roles are complementary, all required memory was registered and peers agree on resource descriptors. For transfer timeout, determine whether the link is recoverable according to the returned status; some LLM DataDist transfer timeouts require unlink and relink rather than blind retry.

## 6. Performance

Measure setup latency separately from bandwidth/latency after warmup. Keep payload size, transfer direction, concurrency, NUMA/device placement and protocol fixed. Look for small-transfer overhead, Host-memory mapping/alignment, channel limits, oversubscription, topology mismatch and slow peers.
