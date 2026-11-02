#!/usr/bin/env python3
"""Verify committed synthetic fixtures and a fresh deterministic generation."""

from __future__ import annotations

import hashlib
import json
import re
import tempfile
from pathlib import Path

import generate


ROOT = Path(__file__).resolve().parent
EXPECTED_COUNTS = {"dev-case-01": 8, "dev-case-02": 8, "dev-case-03": 5}
HASH = re.compile(r"^[0-9a-f]{64}$")
TIMESTAMP = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$")


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def tree(root: Path) -> dict[str, bytes]:
    return {path.relative_to(root).as_posix(): path.read_bytes() for path in sorted(root.rglob("*")) if path.is_file()}


def verify_record(record: dict, case_id: str, index: int) -> None:
    required = {
        "schema_version", "synthetic", "case_id", "event_id", "observed_at",
        "trace_id", "request_id", "clock", "ingested_at", "source", "rank", "event", "record_sha256",
    }
    assert set(record) == required
    assert record["schema_version"] == generate.SCHEMA_VERSION
    assert record["synthetic"] is True
    assert record["case_id"] == case_id
    assert record["event_id"] == f"{case_id}:{index:03d}"
    assert record["trace_id"] == f"trace-{case_id}"
    assert record["request_id"] == f"request-{case_id}"
    assert record["clock"] == {"basis": "synthetic-synchronized", "uncertainty_ms": 1}
    assert TIMESTAMP.fullmatch(record["observed_at"])
    assert TIMESTAMP.fullmatch(record["ingested_at"])
    assert record["source"] == {
        "kind": "synthetic-fixture", "generator": "generate.py",
        "format": "structured-jsonl", "original_log": False,
    }
    assert set(record["rank"]) == {"global", "local", "peer"}
    assert isinstance(record["rank"]["global"], int)
    assert isinstance(record["rank"]["local"], int)
    assert record["rank"]["peer"] is None or isinstance(record["rank"]["peer"], int)
    assert set(record["event"]) == {"category", "name", "phase", "outcome", "measurement", "attributes"}
    assert record["event"]["measurement"]["unit"] in {"ms", "bytes", "count", "state"}
    assert HASH.fullmatch(record["record_sha256"])
    unsigned = dict(record)
    actual_hash = unsigned.pop("record_sha256")
    assert actual_hash == sha256(generate.canonical(unsigned))


def main() -> None:
    committed_root = ROOT / "generated"
    with tempfile.TemporaryDirectory(prefix="infernex-nextgen-") as temp_dir:
        fresh_root = Path(temp_dir) / "generated"
        generate.generate(fresh_root)
        assert tree(fresh_root) == tree(committed_root), "generated output differs; run generate.py"

    participant = committed_root / "participant"
    assert not any("truth" in path.name.lower() for path in participant.rglob("*"))
    participant_manifest = json.loads((participant / "manifest.json").read_text())
    assert participant_manifest["contains_evaluator_truth"] is False
    assert participant_manifest["synthetic"] is True
    participant_bytes = b"".join(path.read_bytes() for path in participant.rglob("*") if path.is_file())
    for evaluator_only_term in (b"submit-blocked", b"network-delay", b"insufficient-evidence", b"injection"):
        assert evaluator_only_term not in participant_bytes

    for case_id, expected_count in EXPECTED_COUNTS.items():
        case_dir = participant / "cases" / case_id
        metadata = json.loads((case_dir / "metadata.json").read_text())
        records = [json.loads(line) for line in (case_dir / "events.jsonl").read_text().splitlines()]
        assert metadata["synthetic"] is True
        assert metadata["event_count"] == expected_count == len(records)
        assert set(metadata["proposal_ids"]).issubset({f"A{i}" for i in range(1, 8)} | {f"B{i}" for i in range(1, 12)})
        for index, record in enumerate(records, 1):
            verify_record(record, case_id, index)

    evaluator = committed_root / "evaluator"
    truths = [json.loads(line) for line in (evaluator / "truth.jsonl").read_text().splitlines()]
    assert {item["case_id"] for item in truths} == set(EXPECTED_COUNTS)
    for item in truths:
        actual_hash = item["truth_sha256"]
        unsigned = dict(item)
        unsigned.pop("truth_sha256")
        assert actual_hash == sha256(generate.canonical(unsigned))

    source_manifest = json.loads((committed_root / "SOURCE-MANIFEST.json").read_text())
    assert source_manifest["synthetic"] is True
    for entry in source_manifest["generated"]:
        path = committed_root / entry["path"]
        assert path.stat().st_size == entry["bytes"]
        assert sha256(path.read_bytes()) == entry["sha256"]
    assert not any(path.suffix == ".log" for path in committed_root.rglob("*"))
    print("PASS: deterministic synthetic fixtures, counts, separation, schema fields, and hashes verified")


if __name__ == "__main__":
    main()
