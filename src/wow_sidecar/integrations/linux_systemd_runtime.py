from __future__ import annotations

from dataclasses import dataclass
import os
from pathlib import Path
import subprocess
from typing import Any, Callable

from ..errors import SidecarError
from .linux_cutover import CANDIDATE_UNIT, CORESIDENT_UNITS, LEGACY_UNITS, ServiceState
from .linux_systemd import LinuxServiceSpec, UNIT_PATH, render_systemd_unit


SYSTEMCTL = "/usr/bin/systemctl"
OBSERVABLE_UNITS = frozenset((CANDIDATE_UNIT, *LEGACY_UNITS, *CORESIDENT_UNITS))
MUTABLE_UNITS = frozenset((CANDIDATE_UNIT, *LEGACY_UNITS))
ADMITTED_UNIT_FILE_STATES = frozenset({"enabled", "disabled"})


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise SidecarError(message)


@dataclass(frozen=True)
class CommandResult:
    returncode: int
    stdout: str


CommandRunner = Callable[[tuple[str, ...]], CommandResult]


def _subprocess_runner(argv: tuple[str, ...]) -> CommandResult:
    env = os.environ.copy()
    env["LC_ALL"] = "C"
    env["SYSTEMD_COLORS"] = "0"
    env["SYSTEMD_PAGER"] = "cat"
    try:
        completed = subprocess.run(
            list(argv),
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            env=env,
            timeout=30,
            check=False,
        )
    except (OSError, subprocess.SubprocessError) as exc:
        raise SidecarError("systemctl execution failed") from exc
    return CommandResult(returncode=completed.returncode, stdout=completed.stdout)


def _observable_unit(value: str) -> str:
    _require(value in OBSERVABLE_UNITS, "systemd unit is outside the admitted observation set")
    return value


def _mutable_unit(value: str) -> str:
    _require(value in MUTABLE_UNITS, "systemd unit is outside the admitted mutation set")
    return value


def _parse_show(stdout: str) -> dict[str, str]:
    _require(isinstance(stdout, str), "systemctl output must be text")
    values: dict[str, str] = {}
    for line in stdout.splitlines():
        if not line:
            continue
        _require("=" in line, "systemctl show output is malformed")
        key, value = line.split("=", 1)
        _require(bool(key) and key not in values, "systemctl show output has invalid property")
        values[key] = value
    return values


class SystemctlControl:
    def __init__(
        self,
        *,
        runner: CommandRunner = _subprocess_runner,
        binary: str = SYSTEMCTL,
    ):
        _require(callable(runner), "runner must be callable")
        _require(binary == SYSTEMCTL, "systemctl binary must use the admitted absolute path")
        self._runner = runner
        self._binary = binary

    def _run(self, *args: str) -> str:
        argv = (self._binary, "--no-ask-password", "--no-pager", *args)
        result = self._runner(argv)
        _require(isinstance(result, CommandResult), "runner returned invalid result")
        if result.returncode != 0:
            raise SidecarError("systemctl command failed")
        return result.stdout

    def observe(self, unit: str) -> ServiceState:
        unit = _observable_unit(unit)
        values = _parse_show(
            self._run(
                "show",
                "--property=LoadState",
                "--property=ActiveState",
                "--property=UnitFileState",
                unit,
            )
        )
        _require(
            set(values) == {"LoadState", "ActiveState", "UnitFileState"},
            "systemctl service observation fields are incomplete",
        )
        load = values["LoadState"]
        active = values["ActiveState"]
        unit_file = values["UnitFileState"]

        if load == "not-found":
            _require(active in {"inactive", ""}, "absent unit has unexpected active state")
            _require(unit_file in {"", "disabled"}, "absent unit has unexpected unit-file state")
            return ServiceState(False, False, False)

        _require(load == "loaded", "service unit is not in admitted loaded state")
        _require(active in {"active", "inactive"}, "service active state is transitional or failed")
        _require(
            unit_file in ADMITTED_UNIT_FILE_STATES,
            "service unit-file state is outside enabled/disabled admission",
        )
        return ServiceState(
            True,
            active == "active",
            unit_file == "enabled",
        )

    def _mutate(self, operation: str, unit: str) -> None:
        _require(operation in {"start", "stop", "enable", "disable"}, "unsupported systemctl mutation")
        unit = _mutable_unit(unit)
        self._run(operation, unit)

    def start(self, unit: str) -> None:
        self._mutate("start", unit)

    def stop(self, unit: str) -> None:
        self._mutate("stop", unit)

    def enable(self, unit: str) -> None:
        self._mutate("enable", unit)

    def disable(self, unit: str) -> None:
        self._mutate("disable", unit)

    def daemon_reload(self) -> None:
        self._run("daemon-reload")

    def candidate_health(self) -> dict[str, Any]:
        values = _parse_show(
            self._run(
                "show",
                "--property=LoadState",
                "--property=ActiveState",
                "--property=SubState",
                "--property=MainPID",
                "--property=NRestarts",
                "--property=NeedDaemonReload",
                "--property=FragmentPath",
                CANDIDATE_UNIT,
            )
        )
        required = {
            "LoadState",
            "ActiveState",
            "SubState",
            "MainPID",
            "NRestarts",
            "NeedDaemonReload",
            "FragmentPath",
        }
        _require(set(values) == required, "candidate health fields are incomplete")
        _require(values["LoadState"] == "loaded", "candidate unit is not loaded")
        _require(values["ActiveState"] == "active", "candidate unit is not active")
        _require(values["SubState"] == "running", "candidate unit is not running")
        _require(values["NeedDaemonReload"] == "no", "candidate unit requires daemon reload")
        _require(values["FragmentPath"] == UNIT_PATH, "candidate fragment path is unexpected")
        try:
            pid = int(values["MainPID"])
            restarts = int(values["NRestarts"])
        except ValueError as exc:
            raise SidecarError("candidate process health fields are invalid") from exc
        _require(pid > 0, "candidate main pid is invalid")
        _require(restarts == 0, "candidate restarted before cutover commit")
        return {
            "schema": "wow-sidecar.systemd-candidate-health.v1",
            "active": True,
            "running": True,
            "main_pid_present": True,
            "restart_count": 0,
            "daemon_reload_required": False,
            "fragment_path_expected": True,
            "healthy": True,
            "contains_command_output": False,
        }


def _rooted(root: Path, absolute: str) -> Path:
    _require(isinstance(root, Path) and root.is_absolute(), "root must be an absolute Path")
    _require(absolute.startswith("/"), "layout path must be absolute")
    base = root.resolve()
    candidate = base / absolute.removeprefix("/")
    parent = candidate.parent.resolve()
    _require(parent == base or base in parent.parents, "layout path escapes root")
    return candidate


def _reject_symlink_ancestors(root: Path, path: Path) -> None:
    base = root.resolve()
    current = path
    chain: list[Path] = []
    while current != base:
        chain.append(current)
        current = current.parent
        _require(base == current or base in current.parents, "staging path escapes root")
    for item in reversed(chain):
        if item.exists() or item.is_symlink():
            _require(not item.is_symlink(), "staging path contains symlink")


def stage_candidate_unit(root: Path, spec: LinuxServiceSpec) -> dict[str, Any]:
    _require(isinstance(spec, LinuxServiceSpec), "invalid Linux service spec")
    target = _rooted(root, UNIT_PATH)
    _reject_symlink_ancestors(root, target.parent)
    raw = render_systemd_unit(spec).encode("utf-8")

    if target.exists() or target.is_symlink():
        _require(target.is_file() and not target.is_symlink(), "candidate unit path is not a regular file")
        try:
            existing = target.read_bytes()
        except OSError as exc:
            raise SidecarError("candidate unit cannot be read") from exc
        _require(existing == raw, "existing candidate unit differs from desired content")
        return {
            "schema": "wow-sidecar.systemd-unit-stage.v1",
            "revision": spec.revision,
            "mutation_performed": False,
            "unit_already_exact": True,
        }

    target.parent.mkdir(parents=True, exist_ok=True)
    _reject_symlink_ancestors(root, target.parent)
    temp = target.with_name(target.name + ".new")
    _require(not temp.exists() and not temp.is_symlink(), "candidate unit staging path already exists")
    fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o644)
    try:
        os.write(fd, raw)
        os.fsync(fd)
    finally:
        os.close(fd)
    try:
        # Hard-link publication is create-only: a racing target can never be
        # silently replaced.
        os.link(temp, target)
        directory_fd = os.open(target.parent, os.O_RDONLY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    except FileExistsError as exc:
        raise SidecarError("candidate unit appeared during staging") from exc
    finally:
        if temp.exists():
            temp.unlink()

    return {
        "schema": "wow-sidecar.systemd-unit-stage.v1",
        "revision": spec.revision,
        "mutation_performed": True,
        "unit_already_exact": False,
    }


def prepare_candidate_unit(
    *,
    root: Path,
    spec: LinuxServiceSpec,
    control: SystemctlControl,
) -> dict[str, Any]:
    stage = stage_candidate_unit(root, spec)
    control.daemon_reload()
    state = control.observe(CANDIDATE_UNIT)
    _require(state == ServiceState(True, False, False), "candidate unit is not staged inactive+disabled")
    return {
        "schema": "wow-sidecar.systemd-candidate-preparation.v1",
        "revision": spec.revision,
        "unit_mutation_performed": stage["mutation_performed"],
        "daemon_reload_performed": True,
        "candidate_present": True,
        "candidate_active": False,
        "candidate_enabled": False,
        "contains_command_output": False,
    }
