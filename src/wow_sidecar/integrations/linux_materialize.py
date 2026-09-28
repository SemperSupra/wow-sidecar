from __future__ import annotations

from dataclasses import dataclass
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
from typing import Any

from ..errors import SidecarError
from ..worker import local_checkout_revision
from .linux_systemd import PRODUCT_ROOT


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise SidecarError(message)


def _revision(value: str) -> str:
    _require(
        isinstance(value, str)
        and len(value) == 40
        and all(ch in "0123456789abcdef" for ch in value),
        "revision must be a 40-hex Git SHA",
    )
    return value


def _rooted(root: Path, absolute: str) -> Path:
    _require(isinstance(root, Path) and root.is_absolute(), "root must be an absolute Path")
    _require(absolute.startswith("/"), "layout path must be absolute")
    base = root.resolve()
    candidate = base / absolute.removeprefix("/")
    parent = candidate.parent.resolve()
    _require(parent == base or base in parent.parents, "layout path escapes root")
    return candidate


def _run_git(*args: str) -> None:
    env = os.environ.copy()
    env["GIT_TERMINAL_PROMPT"] = "0"
    try:
        completed = subprocess.run(
            ["git", *args],
            env=env,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            timeout=120,
            check=False,
        )
    except (OSError, subprocess.SubprocessError) as exc:
        raise SidecarError("git materialization failed") from exc
    if completed.returncode != 0:
        raise SidecarError("git materialization failed")


def _reject_symlinks(path: Path) -> None:
    for child in path.rglob("*"):
        _require(not child.is_symlink(), "materialized checkout contains symlink")


def _tree_digest(path: Path) -> tuple[int, str]:
    digest = hashlib.sha256()
    count = 0
    files = sorted(
        (
            child
            for child in path.rglob("*")
            if child.is_file()
            and not child.is_symlink()
            and ".git" not in child.relative_to(path).parts
        ),
        key=lambda child: child.relative_to(path).as_posix(),
    )
    for child in files:
        relative = child.relative_to(path).as_posix()
        raw = child.read_bytes()
        digest.update(relative.encode("utf-8"))
        digest.update(b"\0")
        digest.update(str(len(raw)).encode("ascii"))
        digest.update(b"\0")
        digest.update(hashlib.sha256(raw).digest())
        count += 1
    return count, "sha256:" + digest.hexdigest()


def _fsync_tree(path: Path) -> None:
    for child in path.rglob("*"):
        if child.is_file() and not child.is_symlink():
            fd = os.open(child, os.O_RDONLY)
            try:
                os.fsync(fd)
            finally:
                os.close(fd)
    directories = [child for child in path.rglob("*") if child.is_dir() and not child.is_symlink()]
    directories.sort(key=lambda child: len(child.parts), reverse=True)
    directories.append(path)
    for directory in directories:
        fd = os.open(directory, os.O_RDONLY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)


def _ensure_parent(root: Path, parent: Path) -> list[Path]:
    base = root.resolve()
    _require(parent == base or base in parent.parents, "materialization parent escapes root")
    missing: list[Path] = []
    cursor = parent
    while cursor != base and not cursor.exists():
        missing.append(cursor)
        cursor = cursor.parent
    _require(cursor == base or cursor.is_dir(), "materialization parent is not a directory")
    created: list[Path] = []
    for directory in reversed(missing):
        directory.mkdir(mode=0o755)
        created.append(directory)
    return created


def _cleanup_empty_directories(created: list[Path]) -> None:
    for directory in reversed(created):
        try:
            directory.rmdir()
        except OSError:
            break


@dataclass(frozen=True)
class LinuxReleaseMaterialization:
    revision: str
    release_path: Path
    file_count: int
    tree_digest: str

    def receipt(self) -> dict[str, Any]:
        return {
            "schema": "wow-sidecar.linux-release-materialization.v1",
            "revision": self.revision,
            "file_count": self.file_count,
            "tree_digest": self.tree_digest,
            "mutation_performed": True,
            "activation_performed": False,
            "active_unit_changed": False,
            "contains_source_path": False,
        }


def materialize_linux_release(
    *,
    root: Path,
    source_repository: Path,
    revision: str,
) -> LinuxReleaseMaterialization:
    revision = _revision(revision)
    _require(
        isinstance(source_repository, Path) and source_repository.is_absolute(),
        "source_repository must be an absolute Path",
    )
    _require(
        source_repository.exists()
        and source_repository.is_dir()
        and not source_repository.is_symlink(),
        "source_repository must be a local repository directory",
    )

    target = _rooted(root, f"{PRODUCT_ROOT}/releases/{revision}")
    _require(not target.exists(), "desired release already exists")
    partial = target.with_name(target.name + ".partial")
    _require(not partial.exists(), "partial release already exists")

    created = _ensure_parent(root, target.parent)
    try:
        _run_git(
            "clone",
            "--no-checkout",
            "--no-hardlinks",
            "--",
            str(source_repository),
            str(partial),
        )
        _run_git("-C", str(partial), "checkout", "--detach", "--force", revision)
        _run_git("-C", str(partial), "remote", "remove", "origin")

        actual = local_checkout_revision(partial)
        _require(actual == revision, "materialized checkout revision mismatch")
        _reject_symlinks(partial)
        file_count, tree_digest = _tree_digest(partial)
        _fsync_tree(partial)

        os.replace(partial, target)
        parent_fd = os.open(target.parent, os.O_RDONLY)
        try:
            os.fsync(parent_fd)
        finally:
            os.close(parent_fd)
    except Exception:
        if partial.exists():
            shutil.rmtree(partial, ignore_errors=True)
        _cleanup_empty_directories(created)
        raise

    return LinuxReleaseMaterialization(
        revision=revision,
        release_path=target,
        file_count=file_count,
        tree_digest=tree_digest,
    )
