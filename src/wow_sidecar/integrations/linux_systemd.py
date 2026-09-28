from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
import re
from typing import Any

from ..errors import SidecarError


PRODUCT_ROOT = "/usr/local/lib/wow-sidecar"
CONFIG_ROOT = "/etc/wow-sidecar"
STATE_ROOT = "/var/lib/wow-sidecar"
UNIT_PATH = "/etc/systemd/system/wow-sidecar-worker.service"
CONTROL_REPOSITORY_RE = re.compile(r"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$")
PROFILE_PATH_RE = re.compile(r"^/etc/wow-sidecar/profiles/[A-Za-z0-9_.-]+\.json$")


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


@dataclass(frozen=True)
class LinuxServiceSpec:
    revision: str
    control_repository: str
    profile_paths: tuple[str, ...]
    poll_seconds: int = 15

    def __post_init__(self) -> None:
        _revision(self.revision)
        _require(
            isinstance(self.control_repository, str)
            and CONTROL_REPOSITORY_RE.fullmatch(self.control_repository) is not None,
            "control_repository must be owner/name",
        )
        _require(bool(self.profile_paths), "at least one profile path is required")
        _require(len(set(self.profile_paths)) == len(self.profile_paths), "duplicate profile path")
        for path in self.profile_paths:
            _require(
                isinstance(path, str) and PROFILE_PATH_RE.fullmatch(path) is not None,
                "profile path must be /etc/wow-sidecar/profiles/<name>.json",
            )
        _require(
            isinstance(self.poll_seconds, int) and 5 <= self.poll_seconds <= 3600,
            "poll_seconds must be 5..3600",
        )

    @property
    def release_path(self) -> str:
        return f"{PRODUCT_ROOT}/releases/{self.revision}"

    @property
    def venv_path(self) -> str:
        return f"{PRODUCT_ROOT}/venvs/{self.revision}"


@dataclass(frozen=True)
class LinuxLayoutObservation:
    revision: str
    release_present: bool
    venv_worker_present: bool
    unit_present: bool
    config_root_present: bool
    state_root_present: bool

    def receipt(self) -> dict[str, Any]:
        return {
            "schema": "wow-sidecar.linux-layout-observation.v1",
            "revision": self.revision,
            "release_present": self.release_present,
            "venv_worker_present": self.venv_worker_present,
            "unit_present": self.unit_present,
            "config_root_present": self.config_root_present,
            "state_root_present": self.state_root_present,
            "mutation_performed": False,
        }


def render_systemd_unit(spec: LinuxServiceSpec) -> str:
    profile_args = " ".join(f"--profile {path}" for path in spec.profile_paths)
    return (
        "[Unit]\n"
        "Description=WOW Sidecar bounded trusted-host worker\n"
        "After=network-online.target\n"
        "Wants=network-online.target\n"
        "\n"
        "[Service]\n"
        "Type=simple\n"
        "User=wow-sidecar\n"
        "Group=wow-sidecar\n"
        f"WorkingDirectory={spec.release_path}\n"
        "EnvironmentFile=/etc/wow-sidecar/worker.env\n"
        f"ExecStart={spec.venv_path}/bin/wow-sidecar-worker "
        f"--control-repository {spec.control_repository} "
        f"{profile_args} "
        f"--repo-root {spec.release_path} --serve --poll-seconds {spec.poll_seconds}\n"
        "Restart=on-failure\n"
        "RestartSec=15\n"
        "NoNewPrivileges=true\n"
        "PrivateTmp=true\n"
        "ProtectSystem=strict\n"
        "ProtectHome=true\n"
        "ReadOnlyPaths=/etc/wow-sidecar\n"
        f"ReadOnlyPaths={spec.release_path}\n"
        f"ReadOnlyPaths={spec.venv_path}\n"
        "ReadWritePaths=/var/lib/wow-sidecar\n"
        "RestrictSUIDSGID=true\n"
        "LockPersonality=true\n"
        "\n"
        "# Managed-By: wow-sidecar\n"
        f"# WOW-Sidecar-Revision: {spec.revision}\n"
        "\n"
        "[Install]\n"
        "WantedBy=multi-user.target\n"
    )


def _rooted(root: Path, absolute: str) -> Path:
    _require(isinstance(root, Path) and root.is_absolute(), "root must be an absolute Path")
    _require(absolute.startswith("/"), "layout path must be absolute")
    base = root.resolve()
    candidate = base / absolute.removeprefix("/")
    parent = candidate.parent.resolve()
    _require(parent == base or base in parent.parents, "layout path escapes root")
    return candidate


def observe_linux_layout(root: Path, revision: str) -> LinuxLayoutObservation:
    revision = _revision(revision)
    release = _rooted(root, f"{PRODUCT_ROOT}/releases/{revision}")
    worker = _rooted(root, f"{PRODUCT_ROOT}/venvs/{revision}/bin/wow-sidecar-worker")
    unit = _rooted(root, UNIT_PATH)
    config = _rooted(root, CONFIG_ROOT)
    state = _rooted(root, STATE_ROOT)
    return LinuxLayoutObservation(
        revision=revision,
        release_present=release.is_dir(),
        venv_worker_present=worker.is_file() and not worker.is_symlink(),
        unit_present=unit.is_file() and not unit.is_symlink(),
        config_root_present=config.is_dir() and not config.is_symlink(),
        state_root_present=state.is_dir() and not state.is_symlink(),
    )
