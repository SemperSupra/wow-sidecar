from __future__ import annotations

from dataclasses import dataclass, replace
import json
import os
from pathlib import Path
import re
from typing import Any, Callable, Protocol

from ..errors import SidecarError


CUTOVER_SCHEMA = "wow-sidecar.linux-cutover-journal.v1"
RECEIPT_SCHEMA = "wow-sidecar.linux-cutover-receipt.v1"
CUTOVER_JOURNAL_PATH = "/var/lib/wow-sidecar/cutover.json"
CANDIDATE_UNIT = "wow-sidecar-worker.service"
LEGACY_UNITS = (
    "wow-sidecar-host-control.service",
    "wow-sidecar-stack.service",
)
UNIT_RE = re.compile(r"^[A-Za-z0-9_.@:-]+\.service$")
TERMINAL_PHASES = {"committed", "rolled-back"}
TRANSIENT_PHASES = {
    "prepared",
    "quiescing-legacy",
    "legacy-quiesced",
    "starting-candidate",
    "candidate-started",
    "verifying-candidate",
    "committing",
    "rolling-back",
    "rollback-failed",
}


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise SidecarError(message)


def _revision(value: str) -> str:
    _require(
        isinstance(value, str)
        and len(value) == 40
        and all(ch in "0123456789abcdef" for ch in value),
        "desired_revision must be a 40-hex Git SHA",
    )
    return value


def _unit(value: str) -> str:
    _require(
        isinstance(value, str) and UNIT_RE.fullmatch(value) is not None,
        "invalid systemd service unit",
    )
    return value


@dataclass(frozen=True)
class ServiceState:
    present: bool
    active: bool
    enabled: bool

    def __post_init__(self) -> None:
        _require(isinstance(self.present, bool), "present must be boolean")
        _require(isinstance(self.active, bool), "active must be boolean")
        _require(isinstance(self.enabled, bool), "enabled must be boolean")
        if not self.present:
            _require(not self.active and not self.enabled, "absent unit cannot be active or enabled")

    def document(self) -> dict[str, bool]:
        return {
            "present": self.present,
            "active": self.active,
            "enabled": self.enabled,
        }

    @classmethod
    def parse(cls, value: Any) -> "ServiceState":
        _require(isinstance(value, dict), "service state must be an object")
        _require(set(value) == {"present", "active", "enabled"}, "service state fields are invalid")
        return cls(
            present=value["present"],
            active=value["active"],
            enabled=value["enabled"],
        )


class SystemdControl(Protocol):
    def observe(self, unit: str) -> ServiceState: ...
    def start(self, unit: str) -> None: ...
    def stop(self, unit: str) -> None: ...
    def enable(self, unit: str) -> None: ...
    def disable(self, unit: str) -> None: ...


@dataclass(frozen=True)
class CutoverRecord:
    desired_revision: str
    phase: str
    candidate_unit: str
    candidate_initial: ServiceState
    legacy_initial: tuple[tuple[str, ServiceState], ...]

    def __post_init__(self) -> None:
        _revision(self.desired_revision)
        _require(self.phase in TERMINAL_PHASES | TRANSIENT_PHASES, "invalid cutover phase")
        _require(_unit(self.candidate_unit) == CANDIDATE_UNIT, "unexpected candidate unit")
        names = tuple(name for name, _ in self.legacy_initial)
        _require(names == LEGACY_UNITS, "legacy unit set/order is invalid")
        for name, state in self.legacy_initial:
            _unit(name)
            _require(isinstance(state, ServiceState), "invalid legacy service state")

    def document(self) -> dict[str, Any]:
        return {
            "schema": CUTOVER_SCHEMA,
            "desired_revision": self.desired_revision,
            "phase": self.phase,
            "candidate_unit": self.candidate_unit,
            "candidate_initial": self.candidate_initial.document(),
            "legacy_initial": {
                name: state.document() for name, state in self.legacy_initial
            },
        }

    @classmethod
    def parse(cls, value: Any) -> "CutoverRecord":
        _require(isinstance(value, dict), "cutover journal must be an object")
        _require(
            set(value)
            == {
                "schema",
                "desired_revision",
                "phase",
                "candidate_unit",
                "candidate_initial",
                "legacy_initial",
            },
            "cutover journal fields are invalid",
        )
        _require(value["schema"] == CUTOVER_SCHEMA, "unsupported cutover journal schema")
        legacy = value["legacy_initial"]
        _require(isinstance(legacy, dict), "legacy_initial must be an object")
        _require(tuple(legacy.keys()) == LEGACY_UNITS, "legacy unit set/order is invalid")
        return cls(
            desired_revision=value["desired_revision"],
            phase=value["phase"],
            candidate_unit=value["candidate_unit"],
            candidate_initial=ServiceState.parse(value["candidate_initial"]),
            legacy_initial=tuple(
                (name, ServiceState.parse(legacy[name])) for name in LEGACY_UNITS
            ),
        )


class CutoverJournal:
    def __init__(self, root: Path):
        _require(isinstance(root, Path) and root.is_absolute(), "root must be an absolute Path")
        self.root = root.resolve()
        self.path = self.root / CUTOVER_JOURNAL_PATH.removeprefix("/")
        parent = self.path.parent.resolve()
        _require(parent == self.root or self.root in parent.parents, "journal path escapes root")

    def exists(self) -> bool:
        return self.path.exists() or self.path.is_symlink()

    def read(self) -> CutoverRecord:
        _require(self.path.is_file() and not self.path.is_symlink(), "cutover journal is missing or invalid")
        try:
            value = json.loads(self.path.read_text(encoding="utf-8"))
        except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise SidecarError("cutover journal cannot be read") from exc
        return CutoverRecord.parse(value)

    def write(self, record: CutoverRecord) -> None:
        _require(isinstance(record, CutoverRecord), "invalid cutover record")
        self.path.parent.mkdir(parents=True, exist_ok=True)
        _require(not self.path.is_symlink(), "cutover journal must not be a symlink")
        temp = self.path.with_name(self.path.name + ".tmp")
        _require(not temp.exists() and not temp.is_symlink(), "cutover journal temp path already exists")
        raw = (json.dumps(record.document(), sort_keys=True, separators=(",", ":")) + "\n").encode("utf-8")
        fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        try:
            os.write(fd, raw)
            os.fsync(fd)
        finally:
            os.close(fd)
        try:
            os.replace(temp, self.path)
            directory_fd = os.open(self.path.parent, os.O_RDONLY)
            try:
                os.fsync(directory_fd)
            finally:
                os.close(directory_fd)
        finally:
            if temp.exists():
                temp.unlink()


def _legacy_snapshot(control: SystemdControl) -> tuple[tuple[str, ServiceState], ...]:
    return tuple((unit, control.observe(unit)) for unit in LEGACY_UNITS)


def _legacy_present(record: CutoverRecord) -> tuple[str, ...]:
    return tuple(unit for unit, state in record.legacy_initial if state.present)


def _assert_candidate_initial(state: ServiceState) -> None:
    _require(state.present, "candidate unit must already be staged")
    _require(not state.active, "candidate unit must be inactive before cutover")
    _require(not state.enabled, "candidate unit must be disabled before cutover")


def _assert_legacy_quiesced(control: SystemdControl, record: CutoverRecord) -> None:
    for unit in _legacy_present(record):
        state = control.observe(unit)
        _require(state.present, "legacy unit disappeared during cutover")
        _require(not state.active, "legacy unit is still active")
        _require(not state.enabled, "legacy unit is still enabled")


def _assert_candidate_off(control: SystemdControl, record: CutoverRecord) -> None:
    state = control.observe(record.candidate_unit)
    _require(state.present, "candidate unit disappeared during cutover")
    _require(not state.active, "candidate unit is still active")
    _require(not state.enabled, "candidate unit is still enabled")


def _assert_committed(control: SystemdControl, record: CutoverRecord) -> None:
    _assert_legacy_quiesced(control, record)
    candidate = control.observe(record.candidate_unit)
    _require(candidate.present and candidate.active and candidate.enabled, "candidate is not active and enabled")


def _assert_rolled_back(control: SystemdControl, record: CutoverRecord) -> None:
    _assert_candidate_off(control, record)
    for unit, expected in record.legacy_initial:
        observed = control.observe(unit)
        _require(observed == expected, "legacy unit state was not restored exactly")


def _receipt(control: SystemdControl, record: CutoverRecord, *, outcome: str, recovered: bool) -> dict[str, Any]:
    candidate = control.observe(record.candidate_unit)
    legacy = [control.observe(unit) for unit in LEGACY_UNITS]
    dual_active = candidate.active and any(state.active for state in legacy)
    return {
        "schema": RECEIPT_SCHEMA,
        "desired_revision": record.desired_revision,
        "outcome": outcome,
        "phase": record.phase,
        "recovered": recovered,
        "candidate_active": candidate.active,
        "candidate_enabled": candidate.enabled,
        "legacy_active_count": sum(1 for state in legacy if state.active),
        "legacy_enabled_count": sum(1 for state in legacy if state.enabled),
        "dual_active_observed": dual_active,
        "mutation_performed": True,
        "contains_secret_values": False,
    }


def _checkpoint(
    journal: CutoverJournal,
    record: CutoverRecord,
    phase: str,
    hook: Callable[[str], None],
) -> CutoverRecord:
    updated = replace(record, phase=phase)
    journal.write(updated)
    hook(phase)
    return updated


def _rollback(
    *,
    control: SystemdControl,
    journal: CutoverJournal,
    record: CutoverRecord,
    checkpoint_hook: Callable[[str], None],
    recovered: bool,
) -> dict[str, Any]:
    try:
        record = _checkpoint(journal, record, "rolling-back", checkpoint_hook)

        candidate = control.observe(record.candidate_unit)
        if candidate.active:
            control.stop(record.candidate_unit)
        candidate = control.observe(record.candidate_unit)
        if candidate.enabled:
            control.disable(record.candidate_unit)
        _assert_candidate_off(control, record)

        # Candidate must be inert before any legacy unit is re-enabled or started.
        for unit, expected in record.legacy_initial:
            observed = control.observe(unit)
            _require(observed.present == expected.present, "legacy unit presence changed during cutover")
            if not expected.present:
                continue
            if expected.enabled and not observed.enabled:
                control.enable(unit)
            elif not expected.enabled and observed.enabled:
                control.disable(unit)

        for unit, expected in record.legacy_initial:
            if not expected.present:
                continue
            observed = control.observe(unit)
            if expected.active and not observed.active:
                control.start(unit)
            elif not expected.active and observed.active:
                control.stop(unit)

        _assert_rolled_back(control, record)
        record = _checkpoint(journal, record, "rolled-back", checkpoint_hook)
        return _receipt(control, record, outcome="rolled-back", recovered=recovered)
    except BaseException:
        # Best effort: preserve a durable indication that recovery did not finish.
        try:
            failed = replace(record, phase="rollback-failed")
            journal.write(failed)
        except Exception:
            pass
        raise


def begin_legacy_cutover(
    *,
    control: SystemdControl,
    journal: CutoverJournal,
    desired_revision: str,
    health_check: Callable[[], bool],
    checkpoint_hook: Callable[[str], None] = lambda _phase: None,
) -> dict[str, Any]:
    desired_revision = _revision(desired_revision)
    _require(not journal.exists(), "cutover journal already exists")
    _require(callable(health_check), "health_check must be callable")
    _require(callable(checkpoint_hook), "checkpoint_hook must be callable")

    candidate_initial = control.observe(CANDIDATE_UNIT)
    _assert_candidate_initial(candidate_initial)
    legacy_initial = _legacy_snapshot(control)
    _require(any(state.present for _, state in legacy_initial), "no legacy unit is present")
    _require(any(state.active for _, state in legacy_initial), "no legacy unit is active")

    record = CutoverRecord(
        desired_revision=desired_revision,
        phase="prepared",
        candidate_unit=CANDIDATE_UNIT,
        candidate_initial=candidate_initial,
        legacy_initial=legacy_initial,
    )
    journal.write(record)
    checkpoint_hook("prepared")

    try:
        record = _checkpoint(journal, record, "quiescing-legacy", checkpoint_hook)
        for unit, state in record.legacy_initial:
            if not state.present:
                continue
            observed = control.observe(unit)
            if observed.active:
                control.stop(unit)
            observed = control.observe(unit)
            if observed.enabled:
                control.disable(unit)
        _assert_legacy_quiesced(control, record)
        record = _checkpoint(journal, record, "legacy-quiesced", checkpoint_hook)

        record = _checkpoint(journal, record, "starting-candidate", checkpoint_hook)
        # Re-check immediately before start: never create dual consumers.
        _assert_legacy_quiesced(control, record)
        control.start(record.candidate_unit)
        candidate = control.observe(record.candidate_unit)
        _require(candidate.active, "candidate did not become active")
        _require(not candidate.enabled, "candidate became enabled before commit")
        _assert_legacy_quiesced(control, record)
        record = _checkpoint(journal, record, "candidate-started", checkpoint_hook)

        record = _checkpoint(journal, record, "verifying-candidate", checkpoint_hook)
        _require(bool(health_check()), "candidate health check failed")
        _assert_legacy_quiesced(control, record)
        candidate = control.observe(record.candidate_unit)
        _require(candidate.active and not candidate.enabled, "candidate state changed during verification")

        record = _checkpoint(journal, record, "committing", checkpoint_hook)
        control.enable(record.candidate_unit)
        _assert_committed(control, record)
        record = _checkpoint(journal, record, "committed", checkpoint_hook)
        return _receipt(control, record, outcome="committed", recovered=False)
    except Exception:
        return _rollback(
            control=control,
            journal=journal,
            record=record,
            checkpoint_hook=checkpoint_hook,
            recovered=False,
        )


def recover_legacy_cutover(
    *,
    control: SystemdControl,
    journal: CutoverJournal,
    checkpoint_hook: Callable[[str], None] = lambda _phase: None,
) -> dict[str, Any]:
    _require(journal.exists(), "cutover journal does not exist")
    record = journal.read()

    if record.phase == "committed":
        _assert_committed(control, record)
        return _receipt(control, record, outcome="committed", recovered=True)

    if record.phase == "rolled-back":
        _assert_rolled_back(control, record)
        return _receipt(control, record, outcome="rolled-back", recovered=True)

    _require(record.phase in TRANSIENT_PHASES, "unsupported recovery phase")
    return _rollback(
        control=control,
        journal=journal,
        record=record,
        checkpoint_hook=checkpoint_hook,
        recovered=True,
    )
