from __future__ import annotations

import hashlib
import os
from dataclasses import dataclass
from pathlib import Path
import re
import subprocess
from typing import Any

from ..errors import SidecarError
from ..recovery_capture import CaptureItem


LEGACY_SCHEMA = "wow-sidecar.legacy-agent-dispatch-observation.v1"
KNOWN_UNIT_PATHS = (
    "/etc/systemd/system/wow-sidecar-host-control.service",
    "/etc/systemd/system/wow-sidecar-stack.service",
)
CONFIG_DIR = "/etc/wow-sidecar"
SOURCE_DIR = "/opt/wow-sidecar"
STATE_DIR = "/var/lib/wow-sidecar"
ENV_FILE_KEY_RE = re.compile(r"^[A-Z][A-Z0-9_]*_FILE$")


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise SidecarError(message)


def _token(value: str) -> str:
    return "sha256:" + hashlib.sha256(value.encode("utf-8")).hexdigest()


def _under_root(root: Path, absolute_path: str) -> Path:
    _require(isinstance(absolute_path, str) and absolute_path.startswith("/"), "legacy path must be absolute")
    root = root.resolve()
    candidate = root.joinpath(absolute_path.removeprefix("/"))
    resolved_parent = candidate.parent.resolve()
    _require(resolved_parent == root or root in resolved_parent.parents, "legacy path escapes observation root")
    return candidate


@dataclass(frozen=True)
class FileObservation:
    logical_name: str
    absolute_path: str
    path_token: str
    exists: bool
    regular_file: bool
    symlink: bool
    size: int | None
    digest: str | None
    mode: str | None

    def evidence(self) -> dict[str, Any]:
        return {
            "logical_name": self.logical_name,
            "path_token": self.path_token,
            "exists": self.exists,
            "regular_file": self.regular_file,
            "symlink": self.symlink,
            "size": self.size,
            "digest": self.digest,
            "mode": self.mode,
        }


@dataclass(frozen=True)
class LegacyObservation:
    source_present: bool
    source_git_head: str | None
    source_dirty_entry_count: int | None
    config_items: tuple[FileObservation, ...]
    referenced_files: tuple[FileObservation, ...]
    units: tuple[FileObservation, ...]
    state_present: bool

    def evidence_receipt(self) -> dict[str, Any]:
        return {
            "schema": LEGACY_SCHEMA,
            "source_present": self.source_present,
            "source_git_head": self.source_git_head,
            "source_dirty_entry_count": self.source_dirty_entry_count,
            "config_items": [item.evidence() for item in self.config_items],
            "referenced_files": [item.evidence() for item in self.referenced_files],
            "units": [item.evidence() for item in self.units],
            "state_present": self.state_present,
            "contains_secret_values": False,
            "contains_absolute_paths": False,
            "mutation_performed": False,
        }

    def capture_items(self, root: Path) -> tuple[CaptureItem, ...]:
        seen: set[str] = set()
        out: list[CaptureItem] = []
        for item in (*self.config_items, *self.referenced_files, *self.units):
            if not item.regular_file or item.symlink or not item.exists:
                continue
            if item.absolute_path in seen:
                continue
            seen.add(item.absolute_path)
            out.append(CaptureItem(item.logical_name, _under_root(root, item.absolute_path)))
        return tuple(out)


def _observe_file(root: Path, logical_name: str, absolute_path: str) -> FileObservation:
    path = _under_root(root, absolute_path)
    exists = path.exists() or path.is_symlink()
    symlink = path.is_symlink() if exists else False
    regular = path.is_file() and not symlink if exists else False
    size = None
    digest = None
    mode = None
    if regular:
        try:
            raw = path.read_bytes()
            st = path.stat()
        except OSError as exc:
            raise SidecarError("legacy file observation failed") from exc
        size = len(raw)
        digest = "sha256:" + hashlib.sha256(raw).hexdigest()
        mode = f"{st.st_mode & 0o777:04o}"
    return FileObservation(
        logical_name=logical_name,
        absolute_path=absolute_path,
        path_token=_token(absolute_path),
        exists=exists,
        regular_file=regular,
        symlink=symlink,
        size=size,
        digest=digest,
        mode=mode,
    )


def _top_level_config_files(root: Path) -> tuple[FileObservation, ...]:
    config = _under_root(root, CONFIG_DIR)
    if not config.exists():
        return ()
    _require(config.is_dir() and not config.is_symlink(), "legacy config path must be a real directory")
    items: list[FileObservation] = []
    try:
        children = sorted(config.iterdir(), key=lambda p: p.name)
    except OSError as exc:
        raise SidecarError("legacy config directory cannot be listed") from exc
    for child in children:
        absolute = CONFIG_DIR.rstrip("/") + "/" + child.name
        items.append(_observe_file(root, f"config:{child.name}", absolute))
    return tuple(items)


def _env_file_references(root: Path, config_items: tuple[FileObservation, ...]) -> tuple[FileObservation, ...]:
    refs: dict[str, FileObservation] = {}
    for item in config_items:
        if not item.regular_file or item.symlink or not item.absolute_path.endswith(".env"):
            continue
        path = _under_root(root, item.absolute_path)
        try:
            lines = path.read_text(encoding="utf-8").splitlines()
        except (OSError, UnicodeDecodeError):
            continue
        for line in lines:
            stripped = line.strip()
            if not stripped or stripped.startswith("#") or "=" not in stripped:
                continue
            key, value = stripped.split("=", 1)
            key = key.strip()
            value = value.strip().strip('"').strip("'")
            if not ENV_FILE_KEY_RE.fullmatch(key) or not value.startswith("/"):
                continue
            if value.startswith("/run/"):
                # Container/runtime mount paths are not host recovery sources.
                continue
            logical = f"referenced:{key.lower()}"
            refs[value] = _observe_file(root, logical, value)
    return tuple(refs[path] for path in sorted(refs))


def _git_state(root: Path) -> tuple[bool, str | None, int | None]:
    source = _under_root(root, SOURCE_DIR)
    if not source.is_dir():
        return False, None, None
    git_dir = source / ".git"
    if not git_dir.exists():
        return True, None, None
    try:
        head = subprocess.run(
            ["git", "-C", str(source), "rev-parse", "HEAD"],
            text=True,
            capture_output=True,
            check=True,
        ).stdout.strip()
        status = subprocess.run(
            ["git", "-C", str(source), "status", "--porcelain"],
            text=True,
            capture_output=True,
            check=True,
        ).stdout.splitlines()
    except (OSError, subprocess.CalledProcessError):
        return True, None, None
    if not (len(head) == 40 and all(ch in "0123456789abcdef" for ch in head)):
        head = None
    return True, head, len(status)


def observe_legacy_agent_dispatch(root: Path = Path("/")) -> LegacyObservation:
    _require(isinstance(root, Path), "root must be a Path")
    _require(root.is_absolute(), "root must be absolute")
    config_items = _top_level_config_files(root)
    referenced = _env_file_references(root, config_items)
    units = tuple(
        _observe_file(root, "unit:" + Path(path).name, path)
        for path in KNOWN_UNIT_PATHS
    )
    source_present, source_head, dirty_count = _git_state(root)
    state = _under_root(root, STATE_DIR)
    return LegacyObservation(
        source_present=source_present,
        source_git_head=source_head,
        source_dirty_entry_count=dirty_count,
        config_items=config_items,
        referenced_files=referenced,
        units=units,
        state_present=state.exists(),
    )
