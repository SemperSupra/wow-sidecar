from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path

SCHEMA = "wow-sidecar.wheelhouse.v1"


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return "sha256:" + digest.hexdigest()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("source_revision")
    parser.add_argument("wheelhouse", type=Path)
    args = parser.parse_args()

    revision = args.source_revision
    if len(revision) != 40 or any(ch not in "0123456789abcdef" for ch in revision):
        raise SystemExit("source revision must be a 40-hex Git SHA")
    wheelhouse = args.wheelhouse.resolve()
    if not wheelhouse.is_dir() or wheelhouse.is_symlink():
        raise SystemExit("wheelhouse must be a regular local directory")

    wheels = sorted(
        path for path in wheelhouse.iterdir()
        if path.is_file() and not path.is_symlink() and path.name.endswith(".whl")
    )
    if not wheels:
        raise SystemExit("wheelhouse contains no wheels")
    if sum(path.name.startswith("wow_sidecar-") for path in wheels) != 1:
        raise SystemExit("wheelhouse must contain exactly one wow_sidecar wheel")

    document = {
        "schema": SCHEMA,
        "source_revision": revision,
        "wheels": [
            {"filename": path.name, "sha256": sha256_file(path)}
            for path in wheels
        ],
    }
    manifest = wheelhouse / "manifest.json"
    manifest.write_text(json.dumps(document, sort_keys=True, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(document, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
