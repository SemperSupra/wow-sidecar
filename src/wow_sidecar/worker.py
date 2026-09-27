from __future__ import annotations

import json
from pathlib import Path
import subprocess
import time
from typing import Any, Callable, Mapping

from .errors import SidecarError
from .host_control import GitControlPlane, HostOperatorRequest, OperatorHandler, process_pending_once


DEFAULT_POLL_SECONDS = 15
AuthorityVerifier = Callable[[HostOperatorRequest], Mapping[str, Any]]
CycleFactory = Callable[[], list[dict[str, Any]]]


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise SidecarError(message)


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


def process_cycle(
    *,
    control_client: Any,
    control_repository: str,
    registry: Mapping[str, OperatorHandler],
    local_operator_revision: str,
    authority_verifier: AuthorityVerifier,
) -> list[dict[str, Any]]:
    """Process at most one pending request through an injected operator registry."""
    _require(isinstance(control_repository, str) and control_repository.count("/") == 1, "control repository must be owner/name")
    _require(bool(registry), "operator registry must not be empty")
    control = GitControlPlane(control_client, control_repository)
    return process_pending_once(
        control=control,
        registry=registry,
        local_operator_revision=local_operator_revision,
        authority_verifier=authority_verifier,
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
    """Run a bounded polling loop; cycle failures are UNKNOWN and never retried specially."""
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
