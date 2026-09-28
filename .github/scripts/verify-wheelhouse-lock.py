from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path


LOCK_SCHEMA = "wow-sidecar.wheelhouse-lock.v1"
MANIFEST_SCHEMA = "wow-sidecar.wheelhouse.v1"


def sha256_bytes(raw: bytes) -> str:
    return "sha256:" + hashlib.sha256(raw).hexdigest()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("lock", type=Path)
    parser.add_argument("wheelhouse", type=Path)
    args = parser.parse_args()

    lock_path = args.lock.resolve()
    wheelhouse = args.wheelhouse.resolve()
    lock = json.loads(lock_path.read_text(encoding="utf-8"))
    manifest_raw = (wheelhouse / "manifest.json").read_bytes()
    manifest = json.loads(manifest_raw.decode("utf-8"))

    if set(lock) != {"schema", "source_revision", "target", "candidate", "wheels"}:
        raise SystemExit("wheelhouse lock fields are invalid")
    if lock["schema"] != LOCK_SCHEMA:
        raise SystemExit("unsupported wheelhouse lock schema")
    if set(lock["candidate"]) != {
        "workflow_run_id",
        "artifact_id",
        "artifact_name",
        "manifest_digest",
    }:
        raise SystemExit("candidate provenance fields are invalid")
    if set(lock["target"]) != {
        "implementation",
        "python",
        "python_abi",
        "machine",
        "glibc_floor",
    }:
        raise SystemExit("target fields are invalid")
    if manifest.get("schema") != MANIFEST_SCHEMA:
        raise SystemExit("candidate manifest schema mismatch")
    if manifest.get("source_revision") != lock["source_revision"]:
        raise SystemExit("candidate source revision mismatch")
    if sha256_bytes(manifest_raw) != lock["candidate"]["manifest_digest"]:
        raise SystemExit("candidate manifest digest mismatch")
    if manifest.get("wheels") != lock["wheels"]:
        raise SystemExit("candidate wheel set differs from admitted lock")

    expected = {item["filename"]: item["sha256"] for item in lock["wheels"]}
    present = {
        path.name: sha256_bytes(path.read_bytes())
        for path in wheelhouse.iterdir()
        if path.is_file() and not path.is_symlink() and path.name.endswith(".whl")
    }
    if present != expected:
        raise SystemExit("candidate artifact bytes differ from admitted lock")

    print(json.dumps({
        "schema": LOCK_SCHEMA,
        "source_revision": lock["source_revision"],
        "workflow_run_id": lock["candidate"]["workflow_run_id"],
        "artifact_id": lock["candidate"]["artifact_id"],
        "artifact_name": lock["candidate"]["artifact_name"],
        "manifest_digest": lock["candidate"]["manifest_digest"],
        "wheel_count": len(lock["wheels"]),
        "target": lock["target"],
        "verified": True,
    }, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
