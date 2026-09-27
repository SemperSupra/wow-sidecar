#!/usr/bin/env python3

from __future__ import annotations

import unittest

from wow_sidecar.errors import SidecarError
from wow_sidecar.host_authority import GitHubIssueAuthorityReader, authority_revision


RECORD = "github-issue-comment:ExampleOrg/authority-repo#64:5646380550"
BODY = "authorize bounded non-mutating host-control proof"


class FakeClient:
    def __init__(self, *, issue_state="open", body=BODY, issue_url=None):
        self.issue_state = issue_state
        self.body = body
        self.issue_url = issue_url or "https://api.github.com/repos/ExampleOrg/authority-repo/issues/64"
        self.calls = []

    def installation_token_for_repository(self, repository):
        self.calls.append(("token", repository))
        return "installation-token"

    def _request_json(self, method, path, *, token, **_kwargs):
        self.calls.append((method, path, token))
        if path.endswith("/issues/64"):
            return {"state": self.issue_state}
        if path.endswith("/issues/comments/5646380550"):
            return {
                "id": 5646380550,
                "body": self.body,
                "issue_url": self.issue_url,
            }
        raise AssertionError(path)


class AuthorityRevisionTests(unittest.TestCase):
    def test_revision_is_stable_and_body_sensitive(self):
        first = authority_revision(RECORD, BODY)
        second = authority_revision(RECORD, BODY)
        changed = authority_revision(RECORD, BODY + " changed")
        self.assertEqual(first, second)
        self.assertNotEqual(first, changed)
        self.assertTrue(first.startswith("sha256:"))


class IssueAuthorityReaderTests(unittest.TestCase):
    def test_open_exact_comment_verifies(self):
        client = FakeClient()
        reader = GitHubIssueAuthorityReader(client)
        expected = authority_revision(RECORD, BODY)
        result = reader.verify(record=RECORD, expected_revision=expected, expected_state="open")
        self.assertTrue(result["verified"])
        self.assertEqual(result["record"], RECORD)
        self.assertEqual(result["revision"], expected)
        self.assertEqual(result["issue_state"], "open")

    def test_stale_revision_fails_closed(self):
        reader = GitHubIssueAuthorityReader(FakeClient())
        with self.assertRaisesRegex(SidecarError, "revision is stale"):
            reader.verify(record=RECORD, expected_revision="sha256:" + "0" * 64, expected_state="open")

    def test_closed_issue_fails_closed(self):
        reader = GitHubIssueAuthorityReader(FakeClient(issue_state="closed"))
        with self.assertRaisesRegex(SidecarError, "closed or cancelled"):
            reader.verify(
                record=RECORD,
                expected_revision=authority_revision(RECORD, BODY),
                expected_state="open",
            )

    def test_comment_must_belong_to_expected_issue(self):
        reader = GitHubIssueAuthorityReader(
            FakeClient(issue_url="https://api.github.com/repos/ExampleOrg/authority-repo/issues/65")
        )
        with self.assertRaisesRegex(SidecarError, "not attached to the expected issue"):
            reader.read_comment(RECORD)

    def test_unknown_locator_is_rejected(self):
        reader = GitHubIssueAuthorityReader(FakeClient())
        with self.assertRaisesRegex(SidecarError, "unsupported authority record"):
            reader.read_comment("github:somewhere")


if __name__ == "__main__":
    unittest.main()
