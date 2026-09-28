from __future__ import annotations

import re
from pathlib import Path
from typing import Any

from .errors import SidecarError


IMAGE_REF_RE = re.compile(r"^[a-z0-9./_-]+(?::[A-Za-z0-9._-]+)?@sha256:[0-9a-f]{64}$")


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise SidecarError(message)


def validate_base_image_reference(value: str) -> str:
    _require(
        isinstance(value, str) and IMAGE_REF_RE.fullmatch(value) is not None,
        "base image must be an exact name@sha256:<64-hex> reference",
    )
    return value


def container_build_inputs(*, source_revision: str, base_image: str) -> dict[str, Any]:
    _require(
        isinstance(source_revision, str)
        and len(source_revision) == 40
        and all(ch in "0123456789abcdef" for ch in source_revision),
        "source_revision must be a 40-hex Git SHA",
    )
    return {
        "source_revision": source_revision,
        "base_image": validate_base_image_reference(base_image),
        "dockerfile": "Dockerfile",
        "revision_file": "/usr/share/wow-sidecar/source-revision",
        "entrypoint": "wow-sidecar-worker",
        "runtime_user": "wow-sidecar",
    }


def inspect_dockerfile(path: Path) -> dict[str, Any]:
    try:
        text = path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError) as exc:
        raise SidecarError("Dockerfile cannot be read") from exc
    required = (
        "ARG PYTHON_BASE",
        "FROM ${PYTHON_BASE}",
        "ARG WOW_SOURCE_REVISION",
        "/usr/share/wow-sidecar/source-revision",
        "chmod 0444 /usr/share/wow-sidecar/source-revision",
        "USER wow-sidecar:wow-sidecar",
        'ENTRYPOINT ["wow-sidecar-worker"]',
    )
    for value in required:
        _require(value in text, f"Dockerfile missing container invariant: {value}")
    forbidden = (
        "privileged",
        "/var/run/docker.sock",
        "systemctl",
        "/opt/wow-sidecar",
        "agent-dispatch",
        "garm",
    )
    lowered = text.lower()
    for value in forbidden:
        _require(value not in lowered, f"Dockerfile contains forbidden integration assumption: {value}")
    return {
        "schema": "wow-sidecar.container-source-contract.v1",
        "revision_file": "/usr/share/wow-sidecar/source-revision",
        "runtime_user": "wow-sidecar",
        "entrypoint": "wow-sidecar-worker",
        "base_image_requires_digest": True,
        "contains_integration_policy": False,
    }
