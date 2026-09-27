from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path
import subprocess
import time
from typing import Any, Callable, Mapping

from .errors import SidecarError
from .host_authority import GitHubIssueAuthorityReader
from .host_control import GitControlPlane, HostOperatorRequest, OperatorHandler, process_pending_once


DEFAULT_POLL_SECONDS = 15
CycleFactory = Callable[[], list[dict[str, Any]]]


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise SidecarError(message)


@dataclass(frozen=True)
class AuthorityBinding:
    """Exact canonical authority admitted for one operator profile."""

    record: str
    revision: str | None = None
    state: str = "open"

    def __post_init__(self) -> None:
        _require(isinstance(self.record, str) and bool(self.record.strip()), "authority binding record is required")
        if self.revision is not None:
            _require(
                isinstance(self.revision, str)
                and self.revision.startswith("sha256:")
                and len(self.revision) == 71
                and all(ch in "0123456789abcdef" for ch in self.revision.removeprefix("sha256:")),
                "authority binding revision is invalid",
            )
        _require(self.state == "open", "authority binding state must be open")


AuthorityBindings = Mapping[str, AuthorityBinding]


def local_checkout_revision(repo_root: Path) -> str:
    """Return the exact clean checkout revision used as operator_revision."""
    try:
        head = subprocess.run(
            ["git", "-C", str(repo_root), "rev-parse", "HEAD"],
            capture_output=True,
            text=True,
            check=True,
        ).stdout.strip()
        dirty = subprocess.run(
            ["git", "-C", str(repo_root), "status", "--porcelain"],
            capture_output=True,
            text=True,
            check=True,
        ).stdout
    except (OSError, subprocess.CalledProcessError) as exc:
        raise SidecarError("cannot establish trusted host operator checkout identity") from exc
    _require(len(head) == 40 and all(ch in "0123456789abcdef" for ch in head), "local checkout HEAD is not a Git SHA")
    _require(not dirty, "trusted host operator checkout is dirty")
    return head


def validate_registry(
    registry: Mapping[str, OperatorHandler],
    authority_bindings: AuthorityBindings,
) -> None:
    """Require every executable profile to have exactly one authority binding."""
    _require(bool(registry), "operator registry must not be empty")
    _require(
        set(registry) == set(authority_bindings),
        "operator registry and authority bindings must have identical profile sets",
    )
    for profile, handler in registry.items():
        _require(
            isinstance(profile, str) and bool(profile) and profile.strip() == profile,
            "operator profile names must be normalized non-empty strings",
        )
        _require(callable(handler), f"operator profile {profile!r} handler is not callable")
        _require(
            isinstance(authority_bindings[profile], AuthorityBinding),
            f"operator profile {profile!r} authority binding is invalid",
        )


def verify_request_authority(
    reader: GitHubIssueAuthorityReader,
    request: HostOperatorRequest,
    authority_bindings: AuthorityBindings,
) -> dict[str, Any]:
    binding = authority_bindings.get(request.operator_profile)
    _require(binding is not None, "request operator profile is outside the admitted host-control envelope")
    _require(
        request.authority_record == binding.record,
        "request authority is outside the admitted host-control envelope",
    )
    if binding.revision is not None:
        _require(
            request.authority_revision == binding.revision,
            "request authority revision is outside the admitted host-control envelope",
        )
    _require(
        request.authority_state == binding.state,
        "request authority state is outside the admitted host-control envelope",
    )
    return reader.verify(
        record=request.authority_record,
        expected_revision=request.authority_revision,
        expected_state=request.authority_state,
    )


def process_cycle(
    *,
    control_client: Any,
    authority_reader: GitHubIssueAuthorityReader,
    control_repository: str,
    registry: Mapping[str, OperatorHandler],
    authority_bindings: AuthorityBindings,
    local_operator_revision: str,
) -> list[dict[str, Any]]:
    """Process at most one pending request through an exact admitted registry."""
    _require(
        isinstance(control_repository, str)
        and control_repository.count("/") == 1
        and all(part.strip() for part in control_repository.split("/", 1)),
        "control repository must be owner/name",
    )
    _require(
        isinstance(local_operator_revision, str)
        and len(local_operator_revision) == 40
        and all(ch in "0123456789abcdef" for ch in local_operator_revision),
        "local operator revision must be a 40-hex Git SHA",
    )
    validate_registry(registry, authority_bindings)
    control = GitControlPlane(control_client, control_repository)
    return process_pending_once(
        control=control,
        registry=registry,
        local_operator_revision=local_operator_revision,
        authority_verifier=lambda request: verify_request_authority(
            authority_reader,
            request,
            authority_bindings,
        ),
        max_requests=1,
    )


def event(result: str, status: str, **extra: Any) -> str:
    _require(result in {"ELIGIBLE", "REJECTED", "UNKNOWN"}, "invalid worker event result")
    _require(isinstance(status, str) and bool(status.strip()), "worker event status is required")
    value = {
        "result": result,
        "status": status,
        "retry_authorized": False,
    }
    value.update(extra)
    return json.dumps(value, sort_keys=True)


def run_service(
    cycle: CycleFactory,
    *,
    poll_seconds: int = DEFAULT_POLL_SECONDS,
    sleep: Callable[[float], None] = time.sleep,
    emit: Callable[[str], None] = print,
) -> int:
    """Run a bounded polling loop; cycle failures are UNKNOWN and never replayed specially.

    Event-driven wake mechanisms should call process_cycle directly. Polling is
    a runtime option, not a protocol requirement.
    """
    _require(callable(cycle), "cycle must be callable")
    _require(5 <= poll_seconds <= 3600, "poll interval must be between 5 and 3600 seconds")
    while True:
        try:
            results = cycle()
            if results:
                emit(event("ELIGIBLE", "host-control-cycle-complete", results=results))
        except KeyboardInterrupt:
            return 0
        except Exception as exc:
            emit(
                event(
                    "UNKNOWN",
                    "host-control-cycle-failed-closed",
                    reason=f"{type(exc).__name__}: raw error omitted",
                    preserve_evidence=True,
                )
            )
        sleep(poll_seconds)
