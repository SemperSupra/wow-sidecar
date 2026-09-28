from __future__ import annotations

import argparse
import json
from pathlib import Path
import zipfile


FIXED_TIME = (1980, 1, 1, 0, 0, 0)


def add_bytes(archive: zipfile.ZipFile, name: str, raw: bytes) -> None:
    info = zipfile.ZipInfo(name, date_time=FIXED_TIME)
    info.compress_type = zipfile.ZIP_STORED
    info.create_system = 3
    info.external_attr = 0o100644 << 16
    archive.writestr(info, raw)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("lock", type=Path)
    parser.add_argument("wheelhouse", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()

    lock_path = args.lock.resolve()
    wheelhouse = args.wheelhouse.resolve()
    output = args.output.resolve()

    lock_raw = lock_path.read_bytes()
    lock = json.loads(lock_raw.decode("utf-8"))
    manifest = wheelhouse / "manifest.json"
    if not manifest.is_file() or manifest.is_symlink():
        raise SystemExit("wheelhouse manifest is missing or invalid")

    expected = [item["filename"] for item in lock["wheels"]]
    if expected != sorted(expected):
        raise SystemExit("lock wheel entries must use deterministic filename order")

    output.parent.mkdir(parents=True, exist_ok=True)
    if output.exists():
        raise SystemExit("output already exists")
    with zipfile.ZipFile(output, "w", allowZip64=True) as archive:
        add_bytes(archive, "manifest.json", manifest.read_bytes())
        add_bytes(archive, "wheelhouse-lock.json", lock_raw)
        for filename in expected:
            path = wheelhouse / filename
            if not path.is_file() or path.is_symlink():
                raise SystemExit(f"wheel is missing or invalid: {filename}")
            add_bytes(archive, filename, path.read_bytes())
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
