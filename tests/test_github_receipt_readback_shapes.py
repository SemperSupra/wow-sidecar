"""G3: synthetic negative controls for untrusted GitHub receipt readbacks.

No network, live GitHub ref, credential, provider dispatch, or consequential effect.
This test-only stacked candidate intentionally reproduces the failure on #95 source.
"""
import unittest

from wow_sidecar.idempotent_github_effect import EffectError, GitHubRefClaimStore


REQUEST_ID = "receipt-shape-test-001"
CLAIM_SHA = "a" * 40
RECEIPT_SHA = "b" * 40


class _ScriptedReadback(GitHubRefClaimStore):
    def __init__(self, replies):
        super().__init__("ExampleOrg/example-repo", token="synthetic-only")
        self.replies = list(replies)
        self.calls = []

    def _http_request(self, method, path, body=None):
        self.calls.append((method, path))
        if method != "GET" or body is not None:
            raise AssertionError("mutation or non-GET request forbidden by fixture")
        if not self.replies:
            raise AssertionError("unexpected extra read")
        return self.replies.pop(0)


def _ref(kind, sha):
    return {"ref": f"refs/heads/{kind}/{REQUEST_ID}",
            "object": {"type": "commit", "sha": sha}}


class ReceiptReadbackShapeTests(unittest.TestCase):
    def _assert_typed_refusal(self, replies, operation):
        store = _ScriptedReadback(replies)
        with self.assertRaises(EffectError):
            operation(store)
        self.assertTrue(store.calls)
        self.assertTrue(all(method == "GET" for method, _ in store.calls))
        self.assertEqual(store.replies, [], "failure occurred before scripted read boundary")

    def test_malformed_http_200_receipt_ref_never_raises_raw_type_errors(self):
        cases = {
            "null_root": None,
            "list_root": [],
            "null_object": {"ref": f"refs/heads/receipts/{REQUEST_ID}", "object": None},
            "list_object": {"ref": f"refs/heads/receipts/{REQUEST_ID}", "object": []},
            "missing_object": {"ref": f"refs/heads/receipts/{REQUEST_ID}"},
            "wrong_ref": _ref("claims", RECEIPT_SHA),
            "wrong_type": {"ref": f"refs/heads/receipts/{REQUEST_ID}",
                           "object": {"type": "tree", "sha": RECEIPT_SHA}},
        }
        for name, payload in cases.items():
            with self.subTest(name=name):
                self._assert_typed_refusal(
                    [(200, payload)], lambda store: store.read_receipt(REQUEST_ID))

    def test_malformed_http_200_receipt_commit_never_raises_raw_type_errors(self):
        cases = {
            "null_root": None,
            "list_root": [],
            "missing_parents": {"sha": RECEIPT_SHA},
            "bad_parents": {"sha": RECEIPT_SHA, "parents": "not-a-list"},
        }
        for name, commit in cases.items():
            with self.subTest(name=name):
                replies = [(200, _ref("receipts", RECEIPT_SHA)),
                           (200, _ref("claims", CLAIM_SHA)), (200, commit)]
                self._assert_typed_refusal(
                    replies, lambda store: store.read_receipt(REQUEST_ID))

    def test_malformed_http_200_receipt_file_never_raises_raw_type_errors(self):
        cases = {"null_root": None, "list_root": [],
                 "null_content": {"type": "file", "encoding": "base64", "content": None}}
        for name, payload in cases.items():
            with self.subTest(name=name):
                replies = [(200, _ref("receipts", RECEIPT_SHA)),
                           (200, _ref("claims", CLAIM_SHA)),
                           (200, {"sha": RECEIPT_SHA, "parents": [{"sha": CLAIM_SHA}]}),
                           (200, payload)]
                self._assert_typed_refusal(
                    replies, lambda store: store.read_receipt(REQUEST_ID))

    def test_malformed_http_200_bound_request_never_raises_raw_type_errors(self):
        for name, payload in (("null_root", None), ("list_root", []),
                              ("null_content", {"type": "file", "encoding": "base64",
                                                "content": None})):
            with self.subTest(name=name):
                self._assert_typed_refusal(
                    [(200, payload)],
                    lambda store: store._read_bound_digest(REQUEST_ID, CLAIM_SHA))


if __name__ == "__main__":
    unittest.main()
