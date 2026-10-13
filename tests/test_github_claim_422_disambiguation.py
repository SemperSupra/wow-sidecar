"""Red-team HTTP 422 disambiguation: duplicate Git ref vs provider validation failure.

Hermetic public-safe transport fixture. This is NOT a live WOW GitHub App
qualification and cannot authorize any consequential executor.
"""
import unittest

from wow_sidecar.idempotent_github_effect import (
    AmbiguousClaimError,
    ClaimConflictError,
    EffectError,
    GitHubRefClaimStore,
)


class _Git422Stub(GitHubRefClaimStore):
    def __init__(self, observed_status, observed_ref=None):
        super().__init__("ExampleOrg/example-repo", token="not-a-secret")
        self.observed_status = observed_status
        self.observed_ref = observed_ref
        self.calls = []

    def _http_request(self, method, path, body=None):
        self.calls.append((method, path))
        if method == "POST" and path.endswith("/git/refs"):
            return 422, {"message": "Validation Failed", "errors": [{"field": "sha", "code": "invalid"}]}
        if method == "GET" and "/git/ref/heads/claims/" in path:
            if self.observed_status == 200:
                return 200, {
                    "ref": self.observed_ref or "refs/heads/claims/valid-claim-422",
                    "object": {"type": "commit", "sha": "b" * 40},
                }
            return self.observed_status, {"message": "injected lookup"}
        raise AssertionError("unexpected transport access: " + method + " " + path)


class GitHub422DisambiguationTests(unittest.TestCase):
    def test_422_with_observed_same_ref_is_conflict(self):
        store = _Git422Stub(200)
        with self.assertRaises(ClaimConflictError):
            store.create_claim("valid-claim-422", "a" * 40)
        self.assertEqual([kind for kind, _ in store.calls], ["POST", "GET"])

    def test_422_without_ref_does_not_fabricate_competing_owner(self):
        store = _Git422Stub(404)
        with self.assertRaises(AmbiguousClaimError) as ctx:
            store.create_claim("valid-claim-422", "a" * 40)
        self.assertIn("422", str(ctx.exception))
        self.assertEqual([kind for kind, _ in store.calls], ["POST", "GET"])

    def test_422_with_unavailable_readback_is_not_confirmed_conflict(self):
        store = _Git422Stub(429)
        with self.assertRaises(AmbiguousClaimError):
            store.create_claim("valid-claim-422", "a" * 40)
        self.assertEqual([kind for kind, _ in store.calls], ["POST", "GET"])

    def test_422_with_wrong_ref_identity_is_not_confirmed_conflict(self):
        store = _Git422Stub(200, "refs/heads/claims/different-claim-422")
        with self.assertRaises(EffectError) as ctx:
            store.create_claim("valid-claim-422", "a" * 40)
        self.assertNotIsInstance(ctx.exception, ClaimConflictError)
        self.assertEqual([kind for kind, _ in store.calls], ["POST", "GET"])


if __name__ == "__main__":
    unittest.main()
