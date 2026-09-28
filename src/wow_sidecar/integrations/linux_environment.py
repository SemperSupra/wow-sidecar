from __future__ import annotations

from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
from typing import Any
import venv

from ..errors import SidecarError
from .linux_systemd import PRODUCT_ROOT


WHEELHOUSE_SCHEMA = "wow-sidecar.wheelhouse.v1"
ENVIRONMENT_SCHEMA = "wow-sidecar.linux-environment.v1"


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


def _sha256_bytes(raw: bytes) -> str:
    return "sha256:" + hashlib.sha256(raw).hexdigest()


def _rooted(root: Path, absolute: str) -> Path:
    _require(isinstance(root, Path) and root.is_absolute(), "root must be an absolute Path")
    _require(absolute.startswith("/"), "layout path must be absolute")
    base = root.resolve()
    candidate = base / absolute.removeprefix("/")
    parent = candidate.parent.resolve()
    _require(parent == base or base in parent.parents, "layout path escapes root")
    return candidate


@dataclass(frozen=True)
class WheelArtifact:
    filename: str
    digest: str

    def __post_init__(self) -> None:
        _require(
            isinstance(self.filename, str)
            and bool(self.filename)
            and self.filename == Path(self.filename).name
            and "/" not in self.filename
            and "\\" not in self.filename
            and self.filename.endswith(".whl"),
            "wheel filename must be a normalized .whl basename",
        )
        _require(
            isinstance(self.digest, str)
            and self.digest.startswith("sha256:")
            and len(self.digest) == 71
            and all(ch in "0123456789abcdef" for ch in self.digest.removeprefix("sha256:")),
            "wheel digest must be sha256:<64-hex>",
        )


@dataclass(frozen=True)
class WheelhouseManifest:
    source_revision: str
    wheels: tuple[WheelArtifact, ...]
    manifest_digest: str

    def __post_init__(self) -> None:
        _revision(self.source_revision)
        _require(bool(self.wheels), "wheelhouse must contain at least one wheel")
        names = [wheel.filename for wheel in self.wheels]
        _require(len(names) == len(set(names)), "duplicate wheel filename")
        _require(
            sum(name.startswith("wow_sidecar-") for name in names) == 1,
            "wheelhouse must contain exactly one wow_sidecar wheel",
        )


@dataclass(frozen=True)
class LinuxEnvironmentMaterialization:
    revision: str
    environment_path: Path
    manifest_digest: str
    wheel_count: int

    def receipt(self) -> dict[str, Any]:
        return {
            "schema": ENVIRONMENT_SCHEMA,
            "revision": self.revision,
            "manifest_digest": self.manifest_digest,
            "wheel_count": self.wheel_count,
            "mutation_performed": True,
            "activation_performed": False,
            "active_unit_changed": False,
            "network_dependency": False,
            "contains_wheel_filenames": False,
        }


def load_wheelhouse_manifest(
    wheelhouse: Path,
    *,
    expected_revision: str,
) -> WheelhouseManifest:
    expected_revision = _revision(expected_revision)
    _require(
        isinstance(wheelhouse, Path) and wheelhouse.is_absolute(),
        "wheelhouse must be an absolute Path",
    )
    _require(
        wheelhouse.is_dir() and not wheelhouse.is_symlink(),
        "wheelhouse must be a local directory",
    )
    manifest_path = wheelhouse / "manifest.json"
    _require(
        manifest_path.is_file() and not manifest_path.is_symlink(),
        "wheelhouse manifest is missing or invalid",
    )
    try:
        raw = manifest_path.read_bytes()
        document = json.loads(raw.decode("utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise SidecarError("wheelhouse manifest cannot be read") from exc

    _require(isinstance(document, dict), "wheelhouse manifest must be an object")
    _require(
        set(document) == {"schema", "source_revision", "wheels"},
        "wheelhouse manifest fields are invalid",
    )
    _require(document["schema"] == WHEELHOUSE_SCHEMA, "unsupported wheelhouse manifest schema")
    _require(document["source_revision"] == expected_revision, "wheelhouse source revision mismatch")
    _require(isinstance(document["wheels"], list), "wheelhouse wheels must be an array")

    wheels: list[WheelArtifact] = []
    for item in document["wheels"]:
        _require(isinstance(item, dict), "wheel entry must be an object")
        _require(set(item) == {"filename", "sha256"}, "wheel entry fields are invalid")
        wheels.append(WheelArtifact(filename=item["filename"], digest=item["sha256"]))

    result = WheelhouseManifest(
        source_revision=expected_revision,
        wheels=tuple(wheels),
        manifest_digest=_sha256_bytes(raw),
    )

    listed = {wheel.filename for wheel in result.wheels}
    present = {
        path.name
        for path in wheelhouse.iterdir()
        if path.is_file() and not path.is_symlink() and path.name.endswith(".whl")
    }
    _require(present == listed, "wheelhouse wheel set does not match manifest")

    for wheel in result.wheels:
        path = wheelhouse / wheel.filename
        _require(path.is_file() and not path.is_symlink(), "wheel artifact is missing or invalid")
        try:
            digest = _sha256_bytes(path.read_bytes())
        except OSError as exc:
            raise SidecarError("wheel artifact cannot be read") from exc
        _require(digest == wheel.digest, "wheel artifact digest mismatch")

    return result


def _pip_environment() -> dict[str, str]:
    env = os.environ.copy()
    env["PIP_CONFIG_FILE"] = os.devnull
    env["PIP_NO_INDEX"] = "1"
    env["PIP_DISABLE_PIP_VERSION_CHECK"] = "1"
    env["PYTHONNOUSERSITE"] = "1"
    env.pop("PYTHONPATH", None)
    env.pop("PYTHONHOME", None)
    return env


def _pip_install_command(python: Path, wheel_paths: tuple[Path, ...]) -> list[str]:
    return [
        str(python),
        "-m",
        "pip",
        "install",
        "--no-index",
        "--no-deps",
        "--disable-pip-version-check",
        *[str(path) for path in wheel_paths],
    ]


def _run_sanitized(command: list[str], *, timeout: int) -> None:
    try:
        completed = subprocess.run(
            command,
            env=_pip_environment(),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            timeout=timeout,
            check=False,
        )
    except (OSError, subprocess.SubprocessError) as exc:
        raise SidecarError("environment command failed") from exc
    if completed.returncode != 0:
        raise SidecarError("environment command failed")


def _write_stamp(path: Path, *, revision: str, manifest_digest: str) -> None:
    raw = (
        json.dumps(
            {
                "schema": ENVIRONMENT_SCHEMA,
                "revision": revision,
                "manifest_digest": manifest_digest,
            },
            sort_keys=True,
        )
        + "\n"
    ).encode("utf-8")
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
    fd = os.open(path, flags, 0o444)
    try:
        os.write(fd, raw)
        os.fsync(fd)
    finally:
        os.close(fd)


def build_linux_environment(
    *,
    root: Path,
    wheelhouse: Path,
    revision: str,
) -> LinuxEnvironmentMaterialization:
    revision = _revision(revision)
    manifest = load_wheelhouse_manifest(wheelhouse, expected_revision=revision)

    target = _rooted(root, f"{PRODUCT_ROOT}/venvs/{revision}")
    _require(not target.exists(), "desired environment already exists")
    target.parent.mkdir(parents=True, exist_ok=True)

    wheel_paths = tuple(wheelhouse / wheel.filename for wheel in manifest.wheels)
    try:
        # Python console scripts embed the virtual environment's absolute path.
        # Build at the final revision path; a failed first materialization removes
        # only that newly created environment.
        venv.EnvBuilder(with_pip=True, clear=False, symlinks=False).create(target)
        python = target / "bin/python"
        worker = target / "bin/wow-sidecar-worker"
        _require(python.is_file() and not python.is_symlink(), "environment Python is invalid")
        _run_sanitized(_pip_install_command(python, wheel_paths), timeout=120)
        _require(worker.is_file() and not worker.is_symlink(), "WOW worker entrypoint was not installed")
        _require(os.access(worker, os.X_OK), "WOW worker entrypoint is not executable")
        _run_sanitized([str(worker), "--help"], timeout=30)

        _write_stamp(
            target / "wow-sidecar-environment.json",
            revision=revision,
            manifest_digest=manifest.manifest_digest,
        )
        parent_fd = os.open(target.parent, os.O_RDONLY)
        try:
            os.fsync(parent_fd)
        finally:
            os.close(parent_fd)
    except Exception:
        if target.exists():
            shutil.rmtree(target, ignore_errors=True)
        raise

    return LinuxEnvironmentMaterialization(
        revision=revision,
        environment_path=target,
        manifest_digest=manifest.manifest_digest,
        wheel_count=len(manifest.wheels),
    )
