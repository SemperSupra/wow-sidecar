#!/usr/bin/env python3
"""Read canonical issue-native authority through the normal Sidecar GitHub App.

The execution-control repository is deliberately non-authoritative.  Consequential
host operators must reread a canonical GitHub issue/comment using the separate
read-only Sidecar identity before invoking a registered operator.
"""

from __future__ import annotations

import hashlib
import re
import urllib.parse
from dataclasses import dataclass
from typing import Any

from .errors import SidecarError
from .github_app import GitHubAppClient


AUTHORITY_RECORD_RE = re.compile(
    r"^github-issue-comment:(?P<repository>[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)#(?P<issue>[1-9][0-9]*):(?P<comment>[1-9][0-9]*)$"
)
REVISION_PREFIX = "sha256:"


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise SidecarError(message)


def authority_revision(record: str, body: str) -> str:
    """Return a stable revision token that changes if the authority text changes."""
    raw = (record + "\n" + body).encode("utf-8")
    return REVISION_PREFIX + hashlib.sha256(raw).hexdigest()


@dataclass(frozen=True)
class IssueCommentAuthority:
    record: str
    repository: str
    issue_number: int
    comment_id: int
    revision: str
    issue_state: str

    def receipt(self) -> dict[str, Any]:
        return {
            "record": self.record,
            "revision": self.revision,
            "issue_state": self.issue_state,
            "verified": True,
        }


class GitHubIssueAuthorityReader:
    """GET-only canonical authority reader bound to the normal Sidecar App."""

    def __init__(self, client: GitHubAppClient | None = None):
        self.client = client or GitHubAppClient.from_env()

    def read_comment(self, record: str) -> IssueCommentAuthority:
        match = AUTHORITY_RECORD_RE.fullmatch(record)
        _require(match is not None, "unsupported authority record locator")
        repository = match.group("repository")
        issue_number = int(match.group("issue"))
        comment_id = int(match.group("comment"))
        owner, repo = repository.split("/", 1)
        token = self.client.installation_token_for_repository(repository)

        issue = self.client._request_json(
            "GET",
            f"/repos/{urllib.parse.quote(owner, safe='')}/{urllib.parse.quote(repo, safe='')}/issues/{issue_number}",
            token=token,
        )
        issue_state = issue.get("state")
        _require(issue_state in {"open", "closed"}, "canonical authority issue returned invalid state")

        comment = self.client._request_json(
            "GET",
            f"/repos/{urllib.parse.quote(owner, safe='')}/{urllib.parse.quote(repo, safe='')}/issues/comments/{comment_id}",
            token=token,
        )
        observed_id = comment.get("id")
        body = comment.get("body")
        issue_url = comment.get("issue_url")
        _require(observed_id == comment_id, "canonical authority comment id mismatch")
        _require(isinstance(body, str) and body, "canonical authority comment body is unavailable")
        expected_issue_suffix = f"/repos/{owner}/{repo}/issues/{issue_number}"
        _require(isinstance(issue_url, str) and issue_url.endswith(expected_issue_suffix), "authority comment is not attached to the expected issue")

        return IssueCommentAuthority(
            record=record,
            repository=repository,
            issue_number=issue_number,
            comment_id=comment_id,
            revision=authority_revision(record, body),
            issue_state=issue_state,
        )

    def verify(self, *, record: str, expected_revision: str, expected_state: str) -> dict[str, Any]:
        _require(expected_state == "open", "host-control authority must require open canonical state")
        _require(
            isinstance(expected_revision, str)
            and expected_revision.startswith(REVISION_PREFIX)
            and len(expected_revision) == len(REVISION_PREFIX) + 64,
            "invalid authority revision token",
        )
        observed = self.read_comment(record)
        _require(observed.issue_state == expected_state, "canonical authority is closed or cancelled")
        _require(observed.revision == expected_revision, "canonical authority revision is stale")
        return observed.receipt()
