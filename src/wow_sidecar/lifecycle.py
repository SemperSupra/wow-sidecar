from __future__ import annotations

import hashlib
import json
from dataclasses import asdict, dataclass
from typing import Any, Iterable

from .errors import SidecarError


PLAN_SCHEMA = "wow-sidecar.lifecycle-plan.v1"
RECOVERY_SCHEMA = "wow-sidecar.recovery-manifest.v1"
OPERATIONS = {
    "install",
    "upgrade",
    "repair",
    "import-existing",
    "rollback",
    "uninstall",
}


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise SidecarError(message)


def _revision(value: str, name: str) -> str:
    _require(
        isinstance(value, str)
        and len(value) == 40
        and all(ch in "0123456789abcdef" for ch in value),
        f"{name} must be a 40-hex Git SHA",
    )
    return value


def sha256_bytes(value: bytes) -> str:
    _require(isinstance(value, bytes), "recovery material must be bytes")
    return "sha256:" + hashlib.sha256(value).hexdigest()


@dataclass(frozen=True)
class ObservedInstallation:
    present: bool
    managed: bool
    revision: str | None
    data_present: bool = False

    def __post_init__(self) -> None:
        _require(isinstance(self.present, bool), "present must be boolean")
        _require(isinstance(self.managed, bool), "managed must be boolean")
        _require(isinstance(self.data_present, bool), "data_present must be boolean")
        if not self.present:
            _require(not self.managed, "absent installation cannot be managed")
            _require(self.revision is None, "absent installation cannot have a revision")
        elif self.managed:
            _require(self.revision is not None, "managed installation requires revision")
            _revision(self.revision, "revision")
        else:
            _require(self.revision is None, "unexpected unmanaged target revision must remain unknown")


@dataclass(frozen=True)
class RecoveryItem:
    logical_name: str
    digest: str
    size: int

    def __post_init__(self) -> None:
        _require(
            isinstance(self.logical_name, str)
            and bool(self.logical_name)
            and self.logical_name.strip() == self.logical_name
            and "/" not in self.logical_name
            and "\\" not in self.logical_name,
            "recovery logical_name must be a normalized opaque name",
        )
        _require(
            isinstance(self.digest, str)
            and self.digest.startswith("sha256:")
            and len(self.digest) == 71
            and all(ch in "0123456789abcdef" for ch in self.digest.removeprefix("sha256:")),
            "recovery digest must be sha256:<64-hex>",
        )
        _require(isinstance(self.size, int) and self.size >= 0, "recovery size must be non-negative")


@dataclass(frozen=True)
class RecoveryManifest:
    source_state: str
    items: tuple[RecoveryItem, ...]
    schema: str = RECOVERY_SCHEMA

    def __post_init__(self) -> None:
        _require(
            self.source_state in {"absent", "managed", "legacy-unmanaged"},
            "invalid recovery source_state",
        )
        names = [item.logical_name for item in self.items]
        _require(len(names) == len(set(names)), "duplicate recovery logical_name")

    def receipt(self) -> dict[str, Any]:
        return {
            "schema": self.schema,
            "source_state": self.source_state,
            "items": [asdict(item) for item in self.items],
            "contains_secret_values": False,
        }


@dataclass(frozen=True)
class LifecyclePlan:
    operation: str
    observed_state: str
    desired_revision: str | None
    preserve_data: bool
    mutation_allowed: bool
    steps: tuple[str, ...]
    rollback_steps: tuple[str, ...]
    schema: str = PLAN_SCHEMA

    def receipt(self) -> dict[str, Any]:
        return {
            "schema": self.schema,
            "operation": self.operation,
            "observed_state": self.observed_state,
            "desired_revision": self.desired_revision,
            "preserve_data": self.preserve_data,
            "mutation_allowed": self.mutation_allowed,
            "steps": list(self.steps),
            "rollback_steps": list(self.rollback_steps),
            "contains_secret_values": False,
        }


def recovery_manifest(
    *,
    source_state: str,
    material: Iterable[tuple[str, bytes]],
) -> RecoveryManifest:
    items = []
    for logical_name, raw in material:
        _require(isinstance(raw, bytes), "recovery material must be bytes")
        items.append(
            RecoveryItem(
                logical_name=logical_name,
                digest=sha256_bytes(raw),
                size=len(raw),
            )
        )
    return RecoveryManifest(source_state=source_state, items=tuple(items))


def verify_recovery_copy(manifest: RecoveryManifest, copied: dict[str, bytes]) -> dict[str, Any]:
    _require(isinstance(manifest, RecoveryManifest), "invalid recovery manifest")
    _require(isinstance(copied, dict), "copied recovery material must be a mapping")
    expected_names = {item.logical_name for item in manifest.items}
    _require(set(copied) == expected_names, "recovery copy item set mismatch")
    for item in manifest.items:
        raw = copied[item.logical_name]
        _require(isinstance(raw, bytes), "copied recovery material must be bytes")
        _require(len(raw) == item.size, f"recovery copy size mismatch: {item.logical_name}")
        _require(sha256_bytes(raw) == item.digest, f"recovery copy digest mismatch: {item.logical_name}")
    return {
        "schema": "wow-sidecar.recovery-copy-verification.v1",
        "verified": True,
        "item_count": len(manifest.items),
        "contains_secret_values": False,
    }


def _observed_state(observed: ObservedInstallation) -> str:
    if not observed.present:
        return "absent"
    return "managed" if observed.managed else "legacy-unmanaged"


def plan_lifecycle(
    *,
    operation: str,
    observed: ObservedInstallation,
    desired_revision: str | None = None,
    recovery_verified: bool = False,
    preserve_data: bool = True,
) -> LifecyclePlan:
    _require(operation in OPERATIONS, "unsupported lifecycle operation")
    _require(isinstance(observed, ObservedInstallation), "invalid observed installation")
    _require(isinstance(recovery_verified, bool), "recovery_verified must be boolean")
    _require(isinstance(preserve_data, bool), "preserve_data must be boolean")
    state = _observed_state(observed)

    if desired_revision is not None:
        desired_revision = _revision(desired_revision, "desired_revision")

    if operation in {"install", "upgrade", "repair", "import-existing"}:
        _require(desired_revision is not None, f"{operation} requires desired_revision")

    if state == "legacy-unmanaged" and operation != "import-existing":
        raise SidecarError("unexpected pre-existing target requires explicit import-existing")

    if operation == "install":
        _require(state == "absent", "install requires absent target")
        return LifecyclePlan(
            operation=operation,
            observed_state=state,
            desired_revision=desired_revision,
            preserve_data=True,
            mutation_allowed=True,
            steps=("materialize-new-target", "verify-desired-revision"),
            rollback_steps=("remove-new-target-only",),
        )

    if operation == "upgrade":
        _require(state == "managed", "upgrade requires managed target")
        _require(recovery_verified, "upgrade requires verified copy-first recovery")
        _require(observed.revision != desired_revision, "upgrade target already at desired revision")
        return LifecyclePlan(
            operation=operation,
            observed_state=state,
            desired_revision=desired_revision,
            preserve_data=True,
            mutation_allowed=True,
            steps=(
                "retain-original-source-state",
                "verify-recovery-copy",
                "materialize-desired-revision",
                "verify-desired-revision",
            ),
            rollback_steps=("restore-exact-prior-revision", "verify-restored-revision"),
        )

    if operation == "repair":
        _require(state == "managed", "repair requires managed target")
        _require(recovery_verified, "repair requires verified copy-first recovery")
        return LifecyclePlan(
            operation=operation,
            observed_state=state,
            desired_revision=desired_revision,
            preserve_data=True,
            mutation_allowed=True,
            steps=(
                "retain-original-source-state",
                "verify-recovery-copy",
                "reconcile-managed-target",
                "verify-desired-revision",
            ),
            rollback_steps=("restore-exact-prior-revision", "verify-restored-revision"),
        )

    if operation == "import-existing":
        _require(state == "legacy-unmanaged", "import-existing requires legacy unmanaged target")
        _require(recovery_verified, "import-existing requires verified copy-first recovery")
        return LifecyclePlan(
            operation=operation,
            observed_state=state,
            desired_revision=desired_revision,
            preserve_data=True,
            mutation_allowed=True,
            steps=(
                "retain-original-source-state",
                "verify-recovery-copy",
                "materialize-managed-target",
                "verify-desired-revision",
            ),
            rollback_steps=("remove-new-managed-target", "preserve-original-source-state"),
        )

    if operation == "rollback":
        _require(state == "managed", "rollback requires managed target")
        _require(desired_revision is not None, "rollback requires desired_revision")
        _require(recovery_verified, "rollback requires verified recovery material")
        return LifecyclePlan(
            operation=operation,
            observed_state=state,
            desired_revision=desired_revision,
            preserve_data=True,
            mutation_allowed=True,
            steps=("restore-exact-prior-revision", "verify-restored-revision"),
            rollback_steps=(),
        )

    _require(operation == "uninstall", "unexpected lifecycle operation")
    _require(state == "managed", "uninstall requires managed target")
    if preserve_data:
        _require(recovery_verified or not observed.data_present, "uninstall preserve-data requires verified recovery when data exists")
        steps = ("verify-recovery-copy", "remove-managed-runtime") if observed.data_present else ("remove-managed-runtime",)
    else:
        steps = ("remove-managed-runtime", "remove-managed-data")
    return LifecyclePlan(
        operation=operation,
        observed_state=state,
        desired_revision=None,
        preserve_data=preserve_data,
        mutation_allowed=True,
        steps=steps,
        rollback_steps=(),
    )


def serialized_receipt_contains(receipt: dict[str, Any], text: str) -> bool:
    _require(isinstance(receipt, dict), "receipt must be an object")
    _require(isinstance(text, str), "search text must be a string")
    return text in json.dumps(receipt, sort_keys=True)
