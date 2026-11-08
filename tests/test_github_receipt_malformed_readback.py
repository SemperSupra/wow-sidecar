"""G3: deterministic malformed GitHub receipt-readback negative controls.

Synthetic transport only: no GitHub API access, real credentials, dispatch,
receipt writes, or system mutations. This test-only branch is intentionally
stacked on the separately owned claim-readback correction.
"""
import unittest

from wow_sidecar.idempotent_github_effect import EffectError, GitHubRefClaimStore


_REQUEST_ID = "valid-receipt-g3"
_CLAIM_SHA = "b" * 40
_RECEIPT_SHA = "c" * 40


def _receipt_ref():
    return {
        "ref": "refs/heads/receipts/" + _REQUEST_ID,
        "object": {"type": "commit", "sha": _RECEIPT_SHA},
    }


def _receipt_commit():
    return {"sha": _RECEIPT_SHA, "parents": [{"sha": _CLAIM_SHA}]}


class _NoNetworkReceiptTransport(GitHubRefClaimStore):
    def __init__(self, *, receipt_ref=None, commit=None, receipt_file=None, request_file=None):
        super().__init__("ExampleOrg/example-repo", token="synthetic-not-credential")
        self.responses = {
            "receipt_ref": (200, receipt_ref),
            "commit": (200, commit),
            "receipt_file": (200, receipt_file),
            "request_file": (200, request_file),
        }
        self.calls = []

    def read_claim(self, request_id):
        if request_id != _REQUEST_ID:
            raise AssertionError("unexpected synthetic request ID")
        return _CLAIM_SHA

    def _http_request(self, method, path, body=None):
        if method != "GET" or body is not None:
            raise AssertionError("receipt readback must not mutate GitHub state")
        if "/git/ref/heads/receipts/" in path:
            key = "receipt_ref"
        elif "/git/commits/" in path:
            key = "commit"
        elif "/contents/receipt.json?" in path:
            key = "receipt_file"
        elif "/contents/request.json?" in path:
            key = "request_file"
        else:
            raise AssertionError("unexpected synthetic access")
        self.calls.append(key)
        return self.responses[key]


class MalformedReceiptReadbackTests(unittest.TestCase):
    def test_http_200_receipt_ref_shape_must_fail_with_typed_error(self):
        valid = _receipt_ref()
        cases = {
            "null_root": None,
            "list_root": [],
            "missing_object": {"ref": valid["ref"]},
            "null_object": {"ref": valid["ref"], "object": None},
            "list_object": {"ref": valid["ref"], "object": []},
            "wrong_ref": {"ref": "refs/heads/receipts/another", "object": valid["object"]},
            "wrong_type": {"ref": valid["ref"], "object": {"type": "tree", "sha": _RECEIPT_SHA}},
            "missing_sha": {"ref": valid["ref"], "object": {"type": "commit"}},
            "invalid_sha": {"ref": valid["ref"], "object": {"type": "commit", "sha": "bad"}},
        }
        for name, malformed in cases.items():
            with self.subTest(name=name):
                store = _NoNetworkReceiptTransport(receipt_ref=malformed)
                with self.assertRaises(EffectError):
                    store.read_receipt(_REQUEST_ID)
                self.assertEqual(store.calls, ["receipt_ref"])

    def test_http_200_receipt_commit_shape_must_fail_with_typed_error(self):
        cases = {
            "null_root": None,
            "list_root": [],
            "null_parents": {"sha": _RECEIPT_SHA, "parents": None},
            "missing_parents": {"sha": _RECEIPT_SHA},
            "wrong_parent": {"sha": _RECEIPT_SHA, "parents": [{"sha": "d" * 40}]},
        }
        for name, malformed in cases.items():
            with self.subTest(name=name):
                store = _NoNetworkReceiptTransport(
                    receipt_ref=_receipt_ref(), commit=malformed
                )
                with self.assertRaises(EffectError):
                    store.read_receipt(_REQUEST_ID)
                self.assertEqual(store.calls, ["receipt_ref", "commit"])

    def test_http_200_receipt_file_shape_must_fail_with_typed_error(self):
        cases = {
            "null_root": None,
            "list_root": [],
            "missing_encoding": {"type": "file"},
            "null_content": {"type": "file", "encoding": "base64", "content": None},
        }
        for name, malformed in cases.items():
            with self.subTest(name=name):
                store = _NoNetworkReceiptTransport(
                    receipt_ref=_receipt_ref(), commit=_receipt_commit(),
                    receipt_file=malformed
                )
                with self.assertRaises(EffectError):
                    store.read_receipt(_REQUEST_ID)
                self.assertEqual(store.calls, ["receipt_ref", "commit", "receipt_file"])

    def test_http_200_bound_request_shape_must_fail_with_typed_error(self):
        cases = {
            "null_root": None,
            "list_root": [],
            "missing_encoding": {"type": "file"},
            "null_content": {"type": "file", "encoding": "base64", "content": None},
        }
        for name, malformed in cases.items():
            with self.subTest(name=name):
                store = _NoNetworkReceiptTransport(request_file=malformed)
                with self.assertRaises(EffectError):
                    store._read_bound_digest(_REQUEST_ID, _CLAIM_SHA)
                self.assertEqual(store.calls, ["request_file"])


if __name__ == "__main__":
    unittest.main()
