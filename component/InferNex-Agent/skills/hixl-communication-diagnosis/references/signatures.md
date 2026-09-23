# HiXL and LLM DataDist signatures

## `TryMatchRoceEndpoints: Failed to find matched ROCE endpoints`

Verify that RoCE is actually intended. Inspect the loaded LocalCommRes, protocol filters, placement, device/IP pairing and both endpoints. On an intended UB path, the presence of a RoCE matching attempt can indicate wrong backend selection or missing effective UB resources.

## `channel connect timeout` or `Endpoint CreateChannel failed`

Check peer process/listener, endpoint/EID/network-instance agreement, route and link state on both nodes, then port/firewall constraints. Confirm clocks before building a cross-node timeline. Only after reachability is demonstrated should connection timeout/retry parameters be tested.

## `HcclCommPrepare` with `wait socket establish timeout`

Match the same communication identifier on both endpoints and compare their request-submission timestamps and configured timeout. In PD startup, one side may enter link preparation later than the other; prove this from both timelines before treating a longer timeout as the remedy. Also compare the selected path on both ends: HCCS/RoCE mismatch cannot be repaired by waiting longer.

## `TIMEOUT`, `LLM_WAIT_PROCESS_TIMEOUT`, or `LLM_TIMEOUT`

Identify the API phase. A connect timeout, metadata wait and data-transfer timeout have different recovery semantics. For transfer-related LLM DataDist timeout, treat the link as suspect and verify the documented unlink/relink flow; do not indefinitely repeat the same request.

## `LLM_NOT_YET_LINK` / `LLM_ALREADY_LINK` / `LLM_LINK_FAILED`

Validate client/server roles, target cluster identity, link state machine, concurrent operations and network reachability. Use per-cluster results rather than the aggregate return alone.

## `LLM_KV_CACHE_NOT_EXIST`

Check whether the remote request/cache lifecycle completed, whether the cache was already pulled/freed, and whether cache identifiers/block metadata match. This is not automatically a transport failure.

## RegisterMem failure, OOM, or slow Host transfer

Check address/length constraints, registration count and size, lifetime, Host memory characteristics and version-specific limits. For relevant A5 Host paths, verify page/alignment and large-page guidance against the deployed HDK/CANN version instead of assuming it applies to A2.

## Failure during disconnect/finalize

Look for in-flight transfers, clients still linked to a server, memory deregistered too early, concurrent lifecycle calls, or a peer process that exited first. Correlate the earliest peer shutdown event.
