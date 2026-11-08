"""Source-neutral 422 claim-ref readback negative tests.

No GitHub network requests, credentials, live claims, or host operations.
A malformed successful readback is UNKNOWN, not a proven competing owner.
"""
import unittest

from wow_sidecar.idempotent_github_effect import (
    AmbiguousClaimError,
    ClaimConflictError,
    GitHubRefClaimStore,
)

class _Git422MalformedReadback(GitHubRefClaimStore):
    def __init__(self, readback):
        super().__init__("ExampleOrg/example-repo", token="synthetic-only")
        self.readback = readback
        self.calls = []

    def _http_request(self, method, path, body=None):
        self.calls.append(method)
        if method == "POST":
            return 422, {"message": "Validation Failed"}
        if method == "GET":
            return 200, self.readback
        raise AssertionError("unexpected transport operation")

class ClaimReadbackMalformedTests(unittest.TestCase):
    def test_422_malformed_successful_readback_is_unknown(self):
        good = {"ref": "refs/heads/claims/valid-claim-422",
                "object": {"type": "commit", "sha": "b" * 40}}
        cases = {
            "missing_object": {"ref": good["ref"]},
            "object_null": {"ref": good["ref"], "object": None},
            "object_list": {"ref": good["ref"], "object": []},
            "missing_sha": {"ref": good["ref"], "object": {"type": "commit"}},
            "invalid_sha": {"ref": good["ref"],
                            "object": {"type": "commit", "sha": "not-a-sha"}},
            "wrong_type": {"ref": good["ref"],
                           "object": {"type": "tree", "sha": "b" * 40}},
            "wrong_ref": {"ref": "refs/heads/claims/other",
                          "object": good["object"]},
            "top_level_list": [],
            "top_level_null": None,
        }
        for name, response in cases.items():
            with self.subTest(name=name):
                store = _Git422MalformedReadback(response)
                with self.assertRaises(AmbiguousClaimError) as cm:
                    store.create_claim("valid-claim-422", "a" * 40)
                self.assertNotIsInstance(cm.exception, ClaimConflictError)
                self.assertEqual(store.calls, ["POST", "GET"])

    def test_422_exact_valid_ref_is_a_verified_conflict(self):
        store = _Git422MalformedReadback({
            "ref": "refs/heads/claims/valid-claim-422",
            "object": {"type": "commit", "sha": "b" * 40}})
        with self.assertRaises(ClaimConflictError):
            store.create_claim("valid-claim-422", "a" * 40)
        self.assertEqual(store.calls, ["POST", "GET"])

if __name__ == "__main__":
    unittest.main()
