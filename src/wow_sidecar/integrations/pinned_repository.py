from __future__ import annotations

from dataclasses import dataclass, field
import os
from pathlib import Path, PurePosixPath
import subprocess
import tempfile
from typing import Any, Callable, Mapping

from ..errors import SidecarError
from ..host_control import HostOperatorRequest


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise SidecarError(message)


RepositoryTokenProvider = Callable[[str], str]


def github_git_env(
    token: str,
    askpass_path: Path,
    base: Mapping[str, str] | None = None,
) -> dict[str, str]:
    _require(
        isinstance(token, str)
        and bool(token)
        and "\n" not in token
        and "\x00" not in token,
        "repository installation token is invalid",
    )
    _require(
        isinstance(askpass_path, Path) and askpass_path.is_absolute(),
        "askpass path must be absolute",
    )
    env = dict(base if base is not None else os.environ)

    # Remove ambient Git authentication/configuration so the pinned operator
    # cannot silently depend on a host login, SSH agent, or credential helper.
    for key in tuple(env):
        if key.startswith("GIT_") or key in {"SSH_ASKPASS", "SSH_AUTH_SOCK"}:
            env.pop(key, None)

    env["GIT_CONFIG_NOSYSTEM"] = "1"
    env["GIT_CONFIG_GLOBAL"] = "/dev/null"
    env["GIT_CONFIG_COUNT"] = "1"
    env["GIT_CONFIG_KEY_0"] = "credential.helper"
    env["GIT_CONFIG_VALUE_0"] = ""
    env["GIT_TERMINAL_PROMPT"] = "0"
    env["GIT_ASKPASS_REQUIRE"] = "force"
    env["GIT_ASKPASS"] = str(askpass_path)
    env["WOW_SIDECAR_GIT_TOKEN"] = token
    return env


def _write_askpass(path: Path) -> None:
    _require(path.is_absolute(), "askpass path must be absolute")
    _require(not path.exists() and not path.is_symlink(), "askpass path already exists")
    raw = b"""#!/bin/sh
case "$1" in
  *Username*) printf '%s\\n' 'x-access-token' ;;
  *Password*) printf '%s\\n' "$WOW_SIDECAR_GIT_TOKEN" ;;
  *) exit 1 ;;
esac
"""
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o700)
    try:
        os.write(fd, raw)
        os.fsync(fd)
    finally:
        os.close(fd)


@dataclass(frozen=True)
class PinnedRepositoryOperatorSpec:
    profile: str
    repository: str
    revision: str
    operator_path: str
    require_main_revision: bool = True
    environment: Mapping[str, str] = field(default_factory=dict)

    def __post_init__(self) -> None:
        _require(
            isinstance(self.profile, str) and bool(self.profile) and self.profile.strip() == self.profile,
            "operator profile is invalid",
        )
        _require(
            isinstance(self.repository, str)
            and self.repository.count("/") == 1
            and all(part.strip() for part in self.repository.split("/", 1)),
            "operator repository must be owner/name",
        )
        _require(
            isinstance(self.revision, str)
            and len(self.revision) == 40
            and all(ch in "0123456789abcdef" for ch in self.revision),
            "operator revision must be a 40-hex Git SHA",
        )
        path = PurePosixPath(self.operator_path)
        _require(
            isinstance(self.operator_path, str)
            and bool(self.operator_path)
            and not path.is_absolute()
            and path.as_posix() == self.operator_path
            and ".." not in path.parts
            and "." not in path.parts,
            "operator path must be a normalized relative path",
        )
        _require(
            all(
                isinstance(key, str)
                and bool(key)
                and key.strip() == key
                and isinstance(value, str)
                for key, value in self.environment.items()
            ),
            "operator environment must map normalized string keys to string values",
        )


class PinnedRepositoryOperator:
    """Execute one locally configured script from one exact repository revision.

    Repository transport is authenticated by an exact-repository GitHub App
    installation token supplied by the worker. No host gh login, SSH identity,
    command, repository, revision, path, arguments, or request environment is
    accepted from the control request.
    """

    def __init__(
        self,
        spec: PinnedRepositoryOperatorSpec,
        *,
        token_provider: RepositoryTokenProvider,
    ):
        _require(callable(token_provider), "repository token provider is required")
        self.spec = spec
        self._token_provider = token_provider

    def _run(self, argv: list[str], *, env: dict[str, str] | None = None, cwd: Path | None = None) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            argv,
            env=env,
            cwd=str(cwd) if cwd is not None else None,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            check=False,
        )

    def __call__(self, request: HostOperatorRequest) -> dict[str, Any]:
        spec = self.spec
        _require(request.operator_profile == spec.profile, "request operator profile does not match configured operator")
        _require(not request.inputs, "pinned repository operator accepts no request inputs")
        _require(
            set(request.constraints) == {"no_retry", "promotion_performed", "release_performed"},
            "request constraints are outside the admitted pinned-operator envelope",
        )
        _require(request.constraints.get("no_retry") is True, "request must forbid retry")
        _require(request.constraints.get("promotion_performed") is False, "promotion flag must remain false")
        _require(request.constraints.get("release_performed") is False, "release flag must remain false")

        try:
            token = self._token_provider(spec.repository)
        except Exception as exc:
            raise SidecarError("repository installation token is unavailable") from exc
        _require(
            isinstance(token, str)
            and bool(token)
            and "\n" not in token
            and "\x00" not in token,
            "repository installation token is invalid",
        )

        remote = f"https://github.com/{spec.repository}.git"

        with tempfile.TemporaryDirectory(prefix="wow-sidecar-pinned-operator-") as tmp:
            temp_root = Path(tmp)
            checkout = temp_root / "operator"
            askpass = temp_root / "git-askpass"
            _write_askpass(askpass)
            git_env = github_git_env(token, askpass)

            if spec.require_main_revision:
                main = self._run(
                    ["git", "ls-remote", "--exit-code", remote, "refs/heads/main"],
                    env=git_env,
                )
                _require(main.returncode == 0, "cannot read canonical repository main")
                fields = main.stdout.strip().split()
                _require(
                    len(fields) >= 1 and fields[0] == spec.revision,
                    "canonical repository main no longer matches admitted revision",
                )

            clone = self._run(
                ["git", "clone", "--quiet", "--filter=blob:none", "--no-checkout", remote, str(checkout)],
                env=git_env,
            )
            _require(clone.returncode == 0, "could not acquire admitted operator repository")
            fetch = self._run(
                ["git", "-C", str(checkout), "fetch", "--quiet", "origin", spec.revision],
                env=git_env,
            )
            _require(fetch.returncode == 0, "could not fetch admitted operator revision")
            checkout_result = self._run(
                ["git", "-C", str(checkout), "checkout", "--quiet", "--detach", spec.revision],
                env=git_env,
            )
            _require(checkout_result.returncode == 0, "could not checkout admitted operator revision")
            head = self._run(["git", "-C", str(checkout), "rev-parse", "HEAD"], env=git_env)
            dirty = self._run(["git", "-C", str(checkout), "status", "--porcelain"], env=git_env)
            _require(
                head.returncode == 0 and head.stdout.strip() == spec.revision,
                "operator checkout revision mismatch",
            )
            _require(dirty.returncode == 0 and not dirty.stdout, "operator checkout is dirty")

            operator = checkout / spec.operator_path
            _require(
                operator.is_file()
                and not operator.is_symlink()
                and checkout.resolve() in operator.resolve().parents,
                "configured operator path is missing or unsafe",
            )

            # Preserve the historical ability for an exact, trusted operator to
            # use Git against its own source repository, but replace ambient host
            # gh credentials with the exact-repository installation token.
            operator_env = dict(git_env)
            operator_env.update(spec.environment)
            completed = self._run(
                ["bash", str(operator)],
                env=operator_env,
                cwd=checkout,
            )

        if completed.returncode == 0:
            return {
                "result": "ELIGIBLE",
                "status": "pinned-repository-operator-complete",
                "workspace_mutation_performed": True,
                "profile": spec.profile,
                "operator_revision": spec.revision,
                "operator_exit_code": 0,
                "preserve_evidence": True,
            }
        return {
            "result": "UNKNOWN",
            "status": "pinned-repository-operator-nonzero",
            "workspace_mutation_performed": True,
            "profile": spec.profile,
            "operator_revision": spec.revision,
            "operator_exit_code": completed.returncode,
            "preserve_evidence": True,
        }
