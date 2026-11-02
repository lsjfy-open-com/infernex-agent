#!/usr/bin/env python3
"""Generate deterministic, explicitly synthetic acceptance evidence.

The output is structured test data. It is not HCCL plog, a capture from a real
cluster, or proof of a diagnosis on real hardware.
"""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
from typing import Any


SCHEMA_VERSION = "infernex.synthetic.acceptance.event/v1"
FIXED_INGESTED_AT = "2026-09-23T08:30:00.000Z"
CASES = {
    "dev-case-01": {
        "title": "开发样例 01",
        "proposal_ids": ["A7", "B9"],
        "base": "2026-09-23T08:00:00",
        "events": [
            (0, 0, 1, "runtime", "request_received", "submit_wait", "ok", 0, "ms", {"queue": "dispatch"}),
            (10, 0, 1, "runtime", "submit_queue_depth", "submit_wait", "blocked", 24, "count", {"limit": 24}),
            (20, 1, 0, "runtime", "worker_ready", "submit_wait", "ok", 1, "state", {"ready": True}),
            (840, 0, 1, "runtime", "submit_wait_elapsed", "submit_wait", "delayed", 840, "ms", {"budget_ms": 100}),
            (845, 0, 1, "transport", "transfer_started", "transfer", "ok", 845, "ms", {"relative_to_request": True}),
            (853, 0, 1, "transport", "payload_transferred", "transfer", "ok", 1048576, "bytes", {"duration_ms": 8}),
            (861, 1, 0, "runtime", "remote_completed", "remote_complete", "ok", 861, "ms", {"relative_to_request": True}),
            (870, 0, 1, "evidence", "counter_snapshot_available", "observation", "ok", 1, "count", {"scope": "fixture"}),
        ],
    },
    "dev-case-02": {
        "title": "开发样例 02",
        "proposal_ids": ["A7", "B9"],
        "base": "2026-09-23T08:05:00",
        "events": [
            (0, 0, 1, "runtime", "request_received", "submit_wait", "ok", 0, "ms", {"queue": "dispatch"}),
            (4, 0, 1, "runtime", "submit_wait_elapsed", "submit_wait", "ok", 4, "ms", {"budget_ms": 100}),
            (5, 0, 1, "transport", "transfer_started", "transfer", "ok", 5, "ms", {"relative_to_request": True}),
            (220, 0, 1, "transport", "transfer_progress", "transfer", "delayed", 220, "ms", {"bytes_completed": 262144}),
            (430, 0, 1, "transport", "transfer_retry", "transfer", "delayed", 3, "count", {"reason": "synthetic_timeout"}),
            (645, 0, 1, "transport", "payload_transferred", "transfer", "delayed", 1048576, "bytes", {"duration_ms": 640}),
            (652, 1, 0, "runtime", "remote_completed", "remote_complete", "ok", 652, "ms", {"relative_to_request": True}),
            (660, 0, 1, "evidence", "device_counter_snapshot", "observation", "missing", None, "state", {"reason": "not_collected"}),
        ],
    },
    "dev-case-03": {
        "title": "开发样例 03",
        "proposal_ids": ["A7", "B9"],
        "base": "2026-09-23T08:10:00",
        "events": [
            (0, 0, 1, "runtime", "request_received", "submit_wait", "ok", 0, "ms", {"queue": "dispatch"}),
            (280, 0, 1, "runtime", "submit_wait_elapsed", "submit_wait", "delayed", 280, "ms", {"budget_ms": 100}),
            (300, 0, 1, "transport", "transfer_segment", "transfer", "missing", None, "state", {"reason": "instrumentation_absent"}),
            (910, 1, 0, "runtime", "remote_completed", "remote_complete", "delayed", 910, "ms", {"relative_to_request": True}),
            (920, 0, 1, "evidence", "peer_and_switch_counters", "observation", "missing", 0, "count", {"reason": "requester_did_not_supply"}),
        ],
    },
}


def canonical(value: Any) -> bytes:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")


def digest(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def event(case_id: str, index: int, spec: tuple[Any, ...], base: str) -> dict[str, Any]:
    offset_ms, rank, peer, category, name, phase, outcome, value, unit, attributes = spec
    record = {
        "schema_version": SCHEMA_VERSION,
        "synthetic": True,
        "case_id": case_id,
        "event_id": f"{case_id}:{index:03d}",
        "trace_id": f"trace-{case_id}",
        "request_id": f"request-{case_id}",
        "observed_at": f"{base}.{offset_ms:03d}Z",
        "ingested_at": FIXED_INGESTED_AT,
        "clock": {"basis": "synthetic-synchronized", "uncertainty_ms": 1},
        "source": {
            "kind": "synthetic-fixture",
            "generator": "generate.py",
            "format": "structured-jsonl",
            "original_log": False,
        },
        "rank": {"global": rank, "local": rank, "peer": peer},
        "event": {
            "category": category,
            "name": name,
            "phase": phase,
            "outcome": outcome,
            "measurement": {"value": value, "unit": unit},
            "attributes": attributes,
        },
    }
    record["record_sha256"] = digest(canonical(record))
    return record


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2).encode("utf-8") + b"\n")


def write_jsonl(path: Path, values: list[dict[str, Any]]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(b"".join(canonical(value) + b"\n" for value in values))


def file_entry(root: Path, path: Path) -> dict[str, Any]:
    data = path.read_bytes()
    return {"path": path.relative_to(root).as_posix(), "bytes": len(data), "sha256": digest(data)}


def generate(output: Path) -> None:
    if output.exists():
        raise FileExistsError(f"refusing to replace existing output: {output}")
    participant = output / "participant"
    evaluator = output / "evaluator"
    participant_files: list[Path] = []

    for case_id, definition in CASES.items():
        records = [event(case_id, i + 1, spec, definition["base"]) for i, spec in enumerate(definition["events"])]
        case_dir = participant / "cases" / case_id
        events_path = case_dir / "events.jsonl"
        metadata_path = case_dir / "metadata.json"
        write_jsonl(events_path, records)
        write_json(metadata_path, {
            "case_id": case_id,
            "title": definition["title"],
            "proposal_ids": definition["proposal_ids"],
            "synthetic": True,
            "event_count": len(records),
            "evidence_scope": "CPU/stdlib fixture only; no real cluster or hardware capture",
        })
        participant_files.extend([events_path, metadata_path])

    participant_manifest = {
        "schema_version": "infernex.synthetic.acceptance.participant-manifest/v1",
        "synthetic": True,
        "created_at": FIXED_INGESTED_AT,
        "contains_evaluator_truth": False,
        "files": [file_entry(participant, path) for path in sorted(participant_files)],
    }
    write_json(participant / "manifest.json", participant_manifest)

    truths = [
        {
            "case_id": "dev-case-01",
            "synthetic": True,
            "scenario": "submit-blocked",
            "injection": "bounded delay before transfer submission",
            "expected_classification": "software-submit-path",
            "acceptable_conclusion": "submission wait dominates the synthetic timeline",
            "prohibited_claims": ["real HCCL fault proven", "switch fault proven", "production fix validated"],
        },
        {
            "case_id": "dev-case-02",
            "synthetic": True,
            "scenario": "network-delay",
            "injection": "bounded delay during synthetic transfer",
            "expected_classification": "controlled-network-path-delay",
            "acceptable_conclusion": "transfer phase dominates; physical device cause remains unproven",
            "prohibited_claims": ["real switch fault proven", "real RDMA fault proven", "production fix validated"],
        },
        {
            "case_id": "dev-case-03",
            "synthetic": True,
            "scenario": "insufficient-evidence",
            "injection": "ambiguous end-to-end delay with missing segment and counter evidence",
            "expected_classification": "insufficient-evidence",
            "acceptable_conclusion": "request more evidence and do not attribute a root cause",
            "prohibited_claims": ["software root cause proven", "network root cause proven", "switch root cause proven"],
        },
    ]
    for truth in truths:
        truth["truth_sha256"] = digest(canonical(truth))
    truth_path = evaluator / "truth.jsonl"
    write_jsonl(truth_path, truths)
    write_json(evaluator / "manifest.json", {
        "schema_version": "infernex.synthetic.acceptance.evaluator-manifest/v1",
        "synthetic": True,
        "created_at": FIXED_INGESTED_AT,
        "access": "evaluator-only; do not copy into participant bundle or Agent workspace",
        "files": [file_entry(evaluator, truth_path)],
    })

    repo_dir = Path(__file__).resolve().parent
    source_files = [repo_dir / "generate.py", repo_dir / "schemas" / "synthetic-event.schema.json"]
    generated_files = sorted(path for path in output.rglob("*") if path.is_file())
    write_json(output / "SOURCE-MANIFEST.json", {
        "schema_version": "infernex.synthetic.acceptance.source-manifest/v1",
        "synthetic": True,
        "created_at": FIXED_INGESTED_AT,
        "generator": [
            {"path": path.relative_to(repo_dir).as_posix(), "bytes": len(path.read_bytes()), "sha256": digest(path.read_bytes())}
            for path in source_files
        ],
        "generated": [file_entry(output, path) for path in generated_files],
        "limitations": [
            "No record is copied from HCCL plog or a real cluster.",
            "No record proves a real diagnosis, performance result, or repair.",
            "Evaluator truth must remain outside the participant bundle.",
        ],
    })


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, default=Path(__file__).resolve().parent / "generated")
    args = parser.parse_args()
    generate(args.output.resolve())


if __name__ == "__main__":
    main()
