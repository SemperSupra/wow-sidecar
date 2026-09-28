from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable, Mapping

from .errors import SidecarError
from .host_control import OperatorHandler
from .integrations.pinned_repository import (
    PinnedRepositoryOperator,
    PinnedRepositoryOperatorSpec,
)
from .worker import AuthorityBinding


PROFILE_SCHEMA = "wow-sidecar.operator-profile.v1"


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise SidecarError(message)


@dataclass(frozen=True)
class LoadedOperatorProfile:
    profile: str
    handler: OperatorHandler
    authority: AuthorityBinding


def _string(value: Any, name: str) -> str:
    _require(isinstance(value, str) and bool(value) and value.strip() == value, f"{name} must be a normalized non-empty string")
    return value


def _string_map(value: Any, name: str) -> dict[str, str]:
    _require(isinstance(value, Mapping), f"{name} must be an object")
    out: dict[str, str] = {}
    for key, item in value.items():
        _string(key, f"{name} key")
        _require(isinstance(item, str), f"{name}.{key} must be a string")
        out[str(key)] = item
    return out


def load_profile(
    value: Any,
    *,
    repository_token_provider: Callable[[str], str],
) -> LoadedOperatorProfile:
    """Load one exact, declarative, non-shell operator profile.

    Version 1 supports only the pinned-repository operator. Extension requires a
    new explicit operator kind and its own validation path; unknown kinds fail
    closed.
    """

    _require(callable(repository_token_provider), "repository token provider is required")
    _require(isinstance(value, dict), "operator profile document must be an object")
    _require(
        set(value) == {"schema", "profile", "authority", "operator"},
        "operator profile document fields do not match v1 schema",
    )
    _require(value.get("schema") == PROFILE_SCHEMA, "unsupported operator profile schema")

    profile = _string(value.get("profile"), "profile")

    authority = value.get("authority")
    _require(isinstance(authority, dict), "authority must be an object")
    _require(
        set(authority) == {"record", "revision", "state"},
        "authority fields do not match v1 schema",
    )
    record = _string(authority.get("record"), "authority.record")
    revision = authority.get("revision")
    _require(
        revision is None
        or (
            isinstance(revision, str)
            and revision.startswith("sha256:")
            and len(revision) == 71
            and all(ch in "0123456789abcdef" for ch in revision.removeprefix("sha256:"))
        ),
        "authority.revision must be null or sha256:<64-hex>",
    )
    _require(authority.get("state") == "open", "authority.state must be open")
    authority_binding = AuthorityBinding(record=record, revision=revision, state="open")

    operator = value.get("operator")
    _require(isinstance(operator, dict), "operator must be an object")
    _require(
        set(operator)
        == {
            "kind",
            "repository",
            "revision",
            "operator_path",
            "require_main_revision",
            "environment",
        },
        "operator fields do not match pinned-repository v1 schema",
    )
    _require(operator.get("kind") == "pinned-repository", "unsupported operator kind")
    require_main = operator.get("require_main_revision")
    _require(isinstance(require_main, bool), "operator.require_main_revision must be boolean")
    spec = PinnedRepositoryOperatorSpec(
        profile=profile,
        repository=_string(operator.get("repository"), "operator.repository"),
        revision=_string(operator.get("revision"), "operator.revision"),
        operator_path=_string(operator.get("operator_path"), "operator.operator_path"),
        require_main_revision=require_main,
        environment=_string_map(operator.get("environment"), "operator.environment"),
    )
    return LoadedOperatorProfile(
        profile=profile,
        handler=PinnedRepositoryOperator(
            spec,
            token_provider=repository_token_provider,
        ),
        authority=authority_binding,
    )


def load_profile_file(
    path: str | Path,
    *,
    repository_token_provider: Callable[[str], str],
) -> LoadedOperatorProfile:
    try:
        value = json.loads(Path(path).read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise SidecarError(f"cannot load operator profile document: {type(exc).__name__}") from exc
    return load_profile(
        value,
        repository_token_provider=repository_token_provider,
    )


def compose_profiles(
    profiles: list[LoadedOperatorProfile],
) -> tuple[dict[str, OperatorHandler], dict[str, AuthorityBinding]]:
    _require(bool(profiles), "at least one operator profile is required")
    registry: dict[str, OperatorHandler] = {}
    authorities: dict[str, AuthorityBinding] = {}
    for item in profiles:
        _require(isinstance(item, LoadedOperatorProfile), "invalid loaded operator profile")
        _require(item.profile not in registry, f"duplicate operator profile: {item.profile}")
        registry[item.profile] = item.handler
        authorities[item.profile] = item.authority
    return registry, authorities
