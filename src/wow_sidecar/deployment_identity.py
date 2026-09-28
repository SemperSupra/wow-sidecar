from __future__ import annotations

from pathlib import Path
import stat

from .errors import SidecarError
from .worker import local_checkout_revision


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise SidecarError(message)


def _validate_revision(value: str) -> str:
    _require(
        isinstance(value, str)
        and len(value) == 40
        and all(ch in "0123456789abcdef" for ch in value),
        "operator revision must be a 40-hex Git SHA",
    )
    return value


def revision_from_file(path: Path) -> str:
    _require(isinstance(path, Path) and path.is_absolute(), "revision file must be an absolute Path")
    _require(path.exists(), "revision file is missing")
    _require(not path.is_symlink(), "revision file must not be a symlink")
    _require(path.is_file(), "revision file must be a regular file")
    try:
        st = path.stat()
        raw = path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError) as exc:
        raise SidecarError("revision file cannot be read") from exc
    _require(
        (stat.S_IMODE(st.st_mode) & 0o222) == 0,
        "revision file must not be writable",
    )
    return _validate_revision(raw.strip())


def resolve_operator_revision(
    *,
    repo_root: Path | None = None,
    revision_file: Path | None = None,
) -> str:
    _require(
        (repo_root is None) != (revision_file is None),
        "exactly one deployment identity source is required",
    )
    if repo_root is not None:
        return local_checkout_revision(repo_root)
    assert revision_file is not None
    return revision_from_file(revision_file)
