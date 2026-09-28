from __future__ import annotations

import hashlib
import json
import os
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Iterable

from .errors import SidecarError
from .lifecycle import RecoveryItem, RecoveryManifest, verify_recovery_copy


CAPTURE_SCHEMA = "wow-sidecar.recovery-capture.v1"


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise SidecarError(message)


@dataclass(frozen=True)
class CaptureItem:
    logical_name: str
    source_path: Path

    def __post_init__(self) -> None:
        RecoveryItem(self.logical_name, "sha256:" + ("0" * 64), 0)
        _require(isinstance(self.source_path, Path), "source_path must be a Path")


@dataclass(frozen=True)
class CapturedItem:
    logical_name: str
    source_path: str
    source_path_token: str
    bundle_name: str
    digest: str
    size: int

    def local_record(self) -> dict[str, Any]:
        return {
            "logical_name": self.logical_name,
            "source_path": self.source_path,
            "source_path_token": self.source_path_token,
            "bundle_name": self.bundle_name,
            "digest": self.digest,
            "size": self.size,
        }

    def evidence_record(self) -> dict[str, Any]:
        return {
            "logical_name": self.logical_name,
            "source_path_token": self.source_path_token,
            "digest": self.digest,
            "size": self.size,
        }


@dataclass(frozen=True)
class RecoveryCapture:
    source_state: str
    destination: Path
    items: tuple[CapturedItem, ...]

    def manifest(self) -> RecoveryManifest:
        return RecoveryManifest(
            source_state=self.source_state,
            items=tuple(
                RecoveryItem(item.logical_name, item.digest, item.size)
                for item in self.items
            ),
        )

    def local_manifest(self) -> dict[str, Any]:
        return {
            "schema": CAPTURE_SCHEMA,
            "source_state": self.source_state,
            "destination": str(self.destination),
            "items": [item.local_record() for item in self.items],
        }

    def evidence_receipt(self) -> dict[str, Any]:
        return {
            "schema": CAPTURE_SCHEMA,
            "source_state": self.source_state,
            "item_count": len(self.items),
            "items": [item.evidence_record() for item in self.items],
            "copy_verified": True,
            "source_mutation_performed": False,
            "contains_secret_values": False,
            "contains_source_paths": False,
        }


def _path_token(path: Path) -> str:
    return "sha256:" + hashlib.sha256(str(path.resolve()).encode("utf-8")).hexdigest()


def _read_regular_file(path: Path) -> bytes:
    _require(path.is_absolute(), "recovery source path must be absolute")
    _require(path.exists(), "recovery source is missing")
    _require(not path.is_symlink(), "recovery source symlinks are not allowed")
    _require(path.is_file(), "recovery source must be a regular file")
    try:
        return path.read_bytes()
    except OSError as exc:
        raise SidecarError("recovery source could not be read") from exc


def _write_new_private_file(path: Path, raw: bytes) -> None:
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
    fd = os.open(path, flags, 0o600)
    try:
        with os.fdopen(fd, "wb", closefd=False) as stream:
            stream.write(raw)
            stream.flush()
            os.fsync(stream.fileno())
    except Exception:
        try:
            path.unlink(missing_ok=True)
        finally:
            os.close(fd)
        raise
    os.close(fd)


def capture_recovery_bundle(
    *,
    source_state: str,
    items: Iterable[CaptureItem],
    destination: Path,
) -> RecoveryCapture:
    _require(
        source_state in {"managed", "legacy-unmanaged"},
        "capture source_state must be managed or legacy-unmanaged",
    )
    _require(isinstance(destination, Path), "destination must be a Path")
    _require(destination.is_absolute(), "recovery destination must be absolute")
    _require(not destination.exists(), "recovery destination already exists")

    capture_items = tuple(items)
    _require(bool(capture_items), "at least one recovery item is required")
    names = [item.logical_name for item in capture_items]
    _require(len(names) == len(set(names)), "duplicate recovery logical_name")

    resolved_sources: list[tuple[CaptureItem, bytes]] = []
    for item in capture_items:
        _require(isinstance(item, CaptureItem), "invalid recovery capture item")
        raw = _read_regular_file(item.source_path)
        resolved_sources.append((item, raw))

    tmp = destination.with_name(destination.name + ".partial")
    _require(not tmp.exists(), "recovery partial destination already exists")
    try:
        tmp.mkdir(parents=False, mode=0o700)
    except OSError as exc:
        raise SidecarError("recovery destination parent is unavailable") from exc

    captured: list[CapturedItem] = []
    try:
        for index, (item, raw) in enumerate(resolved_sources, start=1):
            bundle_name = f"{index:04d}.bin"
            target = tmp / bundle_name
            _write_new_private_file(target, raw)
            digest = "sha256:" + hashlib.sha256(raw).hexdigest()
            captured.append(
                CapturedItem(
                    logical_name=item.logical_name,
                    source_path=str(item.source_path),
                    source_path_token=_path_token(item.source_path),
                    bundle_name=bundle_name,
                    digest=digest,
                    size=len(raw),
                )
            )

        local_manifest = {
            "schema": CAPTURE_SCHEMA,
            "source_state": source_state,
            "items": [item.local_record() for item in captured],
        }
        manifest_raw = (json.dumps(local_manifest, sort_keys=True, indent=2) + "\n").encode("utf-8")
        _write_new_private_file(tmp / "manifest.json", manifest_raw)

        copied = {
            item.logical_name: (tmp / item.bundle_name).read_bytes()
            for item in captured
        }
        verification = verify_recovery_copy(
            RecoveryManifest(
                source_state=source_state,
                items=tuple(
                    RecoveryItem(item.logical_name, item.digest, item.size)
                    for item in captured
                ),
            ),
            copied,
        )
        _require(verification.get("verified") is True, "recovery copy verification failed")

        try:
            dir_fd = os.open(tmp, os.O_RDONLY)
            try:
                os.fsync(dir_fd)
            finally:
                os.close(dir_fd)
            os.replace(tmp, destination)
            parent_fd = os.open(destination.parent, os.O_RDONLY)
            try:
                os.fsync(parent_fd)
            finally:
                os.close(parent_fd)
        except OSError as exc:
            raise SidecarError("recovery bundle finalization failed") from exc
    except Exception:
        if tmp.exists():
            for child in tmp.iterdir():
                if child.is_file() and not child.is_symlink():
                    child.unlink(missing_ok=True)
            try:
                tmp.rmdir()
            except OSError:
                pass
        raise

    return RecoveryCapture(source_state=source_state, destination=destination, items=tuple(captured))


def verify_captured_bundle(capture: RecoveryCapture) -> dict[str, Any]:
    _require(isinstance(capture, RecoveryCapture), "invalid recovery capture")
    _require(capture.destination.is_dir(), "recovery bundle is missing")
    copied: dict[str, bytes] = {}
    for item in capture.items:
        target = capture.destination / item.bundle_name
        _require(target.is_file() and not target.is_symlink(), "recovery bundle item is invalid")
        copied[item.logical_name] = target.read_bytes()
    return verify_recovery_copy(capture.manifest(), copied)
