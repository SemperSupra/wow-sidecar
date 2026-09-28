from __future__ import annotations

import json
import os
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import jwt

from .errors import SidecarError


@dataclass(frozen=True)
class GitHubAppConfig:
    app_id: str
    private_key: str
    api_base: str = "https://api.github.com"

    @classmethod
    def from_env(cls) -> "GitHubAppConfig":
        app_id = os.environ.get("GITHUB_APP_ID", "").strip()
        key_file = os.environ.get("GITHUB_APP_PRIVATE_KEY_FILE", "").strip()
        if key_file:
            try:
                private_key = Path(key_file).read_text(encoding="utf-8")
            except OSError as exc:
                raise SidecarError(f"cannot read GITHUB_APP_PRIVATE_KEY_FILE: {key_file}: {exc}") from exc
        else:
            private_key = os.environ.get("GITHUB_APP_PRIVATE_KEY", "")
            if "\\n" in private_key and "\n" not in private_key:
                private_key = private_key.replace("\\n", "\n")
        missing = [
            name
            for name, value in (
                ("GITHUB_APP_ID", app_id),
                ("GITHUB_APP_PRIVATE_KEY[_FILE]", private_key),
            )
            if not value
        ]
        if missing:
            raise SidecarError("missing GitHub App runtime configuration: " + ", ".join(missing))
        return cls(
            app_id=app_id,
            private_key=private_key,
            api_base=os.environ.get("GITHUB_API_BASE", "https://api.github.com").rstrip("/"),
        )


class GitHubAppClient:
    """Minimal GitHub App client required by WOW Sidecar host authority/control."""

    def __init__(self, config: GitHubAppConfig, *, opener: Any = None):
        self.config = config
        self.opener = opener or urllib.request.urlopen

    @classmethod
    def from_env(cls) -> "GitHubAppClient":
        return cls(GitHubAppConfig.from_env())

    def app_jwt(self, *, now: int | None = None) -> str:
        current = int(now if now is not None else time.time())
        payload = {"iat": current - 60, "exp": current + 9 * 60, "iss": self.config.app_id}
        encoded = jwt.encode(payload, self.config.private_key, algorithm="RS256")
        return encoded.decode("utf-8") if isinstance(encoded, bytes) else encoded

    def _request_json(
        self,
        method: str,
        path: str,
        *,
        token: str,
        body: dict[str, Any] | None = None,
        expected_shape: str = "object",
        opener: Any = None,
    ) -> Any:
        if expected_shape not in ("object", "array"):
            raise SidecarError(f"invalid expected_shape: {expected_shape!r}; must be 'object' or 'array'")
        active_opener = opener if opener is not None else getattr(self, "opener", urllib.request.urlopen)
        data = None if body is None else json.dumps(body, separators=(",", ":")).encode("utf-8")
        request = urllib.request.Request(
            f"{self.config.api_base}{path}",
            data=data,
            method=method,
            headers={
                "Accept": "application/vnd.github+json",
                "Authorization": f"Bearer {token}",
                "X-GitHub-Api-Version": "2022-11-28",
                "Content-Type": "application/json",
                "User-Agent": "semper-supra-wow-sidecar/0.0.0-dev",
            },
        )
        try:
            with active_opener(request, timeout=20.0) as response:
                raw = response.read()
                if not raw:
                    return {} if expected_shape == "object" else []
                try:
                    value = json.loads(raw.decode("utf-8"))
                except (json.JSONDecodeError, UnicodeDecodeError) as exc:
                    raise SidecarError(f"GitHub returned malformed JSON for {method} {path}: {exc}") from exc
                if expected_shape == "object":
                    if not isinstance(value, dict):
                        raise SidecarError(
                            f"GitHub returned unexpected non-object JSON for {method} {path}: got {type(value).__name__}"
                        )
                    return value
                if not isinstance(value, list):
                    raise SidecarError(
                        f"GitHub returned unexpected non-array JSON for {method} {path}: got {type(value).__name__}"
                    )
                return value
        except urllib.error.HTTPError as exc:
            detail = exc.read().decode("utf-8", errors="replace")[:2000]
            raise SidecarError(f"GitHub App API failed: {method} {path}: HTTP {exc.code}: {detail}") from exc
        except urllib.error.URLError as exc:
            raise SidecarError(f"GitHub App API failed: {method} {path}: {exc.reason}") from exc

    def installation_id_for_repository(self, repository: str, *, opener: Any = None) -> int:
        if repository.count("/") != 1:
            raise SidecarError("repository must be owner/name")
        owner, repo = repository.split("/", 1)
        if not owner or not repo or owner.strip() != owner or repo.strip() != repo:
            raise SidecarError("repository must be owner/name")
        active_opener = opener if opener is not None else getattr(self, "opener", urllib.request.urlopen)
        value = self._request_json(
            "GET",
            f"/repos/{owner}/{repo}/installation",
            token=self.app_jwt(),
            opener=active_opener,
        )
        installation_id = value.get("id")
        if not isinstance(installation_id, int):
            raise SidecarError(f"GitHub App is not installed or installation id is missing for {repository}")
        return installation_id

    def installation_token_for_repository(self, repository: str, *, opener: Any = None) -> str:
        active_opener = opener if opener is not None else getattr(self, "opener", urllib.request.urlopen)
        installation_id = self.installation_id_for_repository(repository, opener=active_opener)
        value = self._request_json(
            "POST",
            f"/app/installations/{installation_id}/access_tokens",
            token=self.app_jwt(),
            body={},
            opener=active_opener,
        )
        token = value.get("token")
        if not isinstance(token, str) or not token:
            raise SidecarError("GitHub did not return an installation access token")
        return token
