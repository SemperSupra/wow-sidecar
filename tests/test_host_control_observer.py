#!/usr/bin/env python3
from __future__ import annotations

import json
import unittest
from pathlib import Path

from wow_sidecar.errors import SidecarError
from wow_sidecar.host_control import CLAIM_REF_PREFIX, RECEIPT_SCHEMA
from wow_sidecar import host_control_observer as observer_module
from wow_sidecar.host_control_observer import HostOperatorReceipt, observe_request


REQUEST_ID = "host-proof-001"
REQUEST_SHA = "1" * 40
RECEIPT_SHA = "2" * 40
OPERATOR_SHA = "3" * 40
AUTH_REV = "sha256:" + "a" * 64


def receipt_value(**overrides):
    value = {
        "schema": RECEIPT_SCHEMA,
        "request_id": REQUEST_ID,
        "request_commit_sha": REQUEST_SHA,
        "claim_ref": CLAIM_REF_PREFIX + REQUEST_ID,
        "operator_profile": "host-control-proof",
        "operator_revision": OPERATOR_SHA,
        "authority_record": "github-issue-comment:example/repository#64:1234567890",
        "authority_revision": AUTH_REV,
        "authority_state": "open",
        "result": "ELIGIBLE",
        "status": "host-control-proof-receiptable",
        "workspace_mutation_performed": False,
        "retry_authorized": False,
        "promotion_performed": False,
        "release_performed": False,
        "details": {
            "result": "ELIGIBLE",
            "workspace_mutation_performed": False,
            "nonce": "synthetic-proof-nonce",
            "authority_verified": True,
        },
    }
    value.update(overrides)
    return value


class FakeClient:
    def __init__(self, parent_sha=REQUEST_SHA):
        self.parent_sha = parent_sha
        self.calls = []

    def _request_json(self, method, path, *, token=None, **_kwargs):
        self.calls.append((method, path, token))
        if method == "GET" and "/git/commits/" in path:
            return {"parents": [{"sha": self.parent_sha}]}
        raise AssertionError(f"unexpected API call: {method} {path}")


class FakeControl:
    owner = "SemperSupra"
    repo = "example-execution-control"

    def __init__(self, *, parent_sha=REQUEST_SHA, receipt=None):
        self.client = FakeClient(parent_sha=parent_sha)
        self.maps = {
            "requests": {REQUEST_ID: REQUEST_SHA},
            "claims": {REQUEST_ID: REQUEST_SHA},
            "receipts": {REQUEST_ID: RECEIPT_SHA},
        }
        self.receipt = receipt if receipt is not None else receipt_value()
        self.reads = []

    def _token(self):
        return "synthetic-read-token"

    def ref_map(self, logical_prefix):
        return dict(self.maps[logical_prefix])

    def read_json_at_commit(self, commit_sha, path):
        self.reads.append((commit_sha, path))
        if (commit_sha, path) != (RECEIPT_SHA, "receipt.json"):
            raise AssertionError(f"unexpected read: {commit_sha} {path}")
        return dict(self.receipt)


class HostOperatorReceiptTests(unittest.TestCase):
    def test_exact_receipt_parses(self):
        receipt = HostOperatorReceipt.parse(receipt_value())
        self.assertEqual(receipt.request_id, REQUEST_ID)
        self.assertEqual(receipt.request_commit_sha, REQUEST_SHA)
        self.assertEqual(receipt.result, "ELIGIBLE")
        self.assertEqual(receipt.authority_revision, AUTH_REV)
        self.assertFalse(receipt.workspace_mutation_performed)

    def test_receipt_rejects_extension_retry_invalid_mutation_or_bad_authority(self):
        extra = receipt_value(extra="not-allowed")
        retry = receipt_value(retry_authorized=True)
        mutation = receipt_value(workspace_mutation_performed="unknown")
        bad_authority = receipt_value(authority_revision="latest")
        for value in (extra, retry, mutation, bad_authority):
            with self.subTest(value=value):
                with self.assertRaises(SidecarError):
                    HostOperatorReceipt.parse(value)


class ObserveRequestTests(unittest.TestCase):
    def test_valid_receipt_requires_exact_claim_parent_and_record_binding(self):
        control = FakeControl()
        result = observe_request(control, REQUEST_ID)
        self.assertEqual(result["state"], "receipted")
        self.assertEqual(result["result"], "ELIGIBLE")
        self.assertEqual(result["authority_revision"], AUTH_REV)
        self.assertEqual(result["authority_state"], "open")
        self.assertTrue(result["execution_consumed"])
        self.assertFalse(result["retry_authorized"])
        self.assertFalse(result["execution_authorized_by_observer"])
        self.assertEqual(
            result["binding"],
            {
                "claim_matches_request": True,
                "receipt_parent_matches_request": True,
                "receipt_record_matches_request": True,
            },
        )
        self.assertEqual(control.reads, [(RECEIPT_SHA, "receipt.json")])
        self.assertEqual(len(control.client.calls), 1)
        self.assertNotIn("details", result)
        self.assertNotIn("synthetic-proof-nonce", json.dumps(result))

    def test_unclaimed_request_is_visible_but_observer_never_authorizes_execution(self):
        control = FakeControl()
        control.maps["claims"] = {}
        control.maps["receipts"] = {}
        result = observe_request(control, REQUEST_ID)
        self.assertEqual(result["state"], "unclaimed")
        self.assertIsNone(result["result"])
        self.assertFalse(result["execution_consumed"])
        self.assertFalse(result["execution_authorized_by_observer"])

    def test_claim_without_receipt_is_durable_unknown_and_no_retry(self):
        control = FakeControl()
        control.maps["receipts"] = {}
        result = observe_request(control, REQUEST_ID)
        self.assertEqual(result["state"], "claimed-without-receipt")
        self.assertEqual(result["result"], "UNKNOWN")
        self.assertTrue(result["execution_consumed"])
        self.assertFalse(result["retry_authorized"])

    def test_claim_request_mismatch_fails_closed_before_receipt_read(self):
        control = FakeControl()
        control.maps["claims"][REQUEST_ID] = "9" * 40
        result = observe_request(control, REQUEST_ID)
        self.assertEqual(result["state"], "claim-request-mismatch")
        self.assertEqual(result["result"], "UNKNOWN")
        self.assertEqual(control.reads, [])

    def test_receipt_commit_wrong_parent_is_unknown(self):
        control = FakeControl(parent_sha="8" * 40)
        result = observe_request(control, REQUEST_ID)
        self.assertEqual(result["state"], "invalid-receipt-binding")
        self.assertEqual(result["result"], "UNKNOWN")

    def test_receipt_record_wrong_request_binding_is_unknown(self):
        control = FakeControl(receipt=receipt_value(request_commit_sha="7" * 40))
        result = observe_request(control, REQUEST_ID)
        self.assertEqual(result["state"], "invalid-receipt-binding")
        self.assertEqual(result["result"], "UNKNOWN")

    def test_receipt_without_claim_is_unknown_without_reading_receipt(self):
        control = FakeControl()
        control.maps["claims"] = {}
        result = observe_request(control, REQUEST_ID)
        self.assertEqual(result["state"], "receipt-without-claim")
        self.assertEqual(result["result"], "UNKNOWN")
        self.assertEqual(control.reads, [])

    def test_observer_source_contains_no_git_mutation_methods(self):
        source = Path(observer_module.__file__).resolve()
        text = source.read_text(encoding="utf-8")
        for method in ('"POST"', '"PUT"', '"PATCH"', '"DELETE"'):
            self.assertNotIn(method, text)
        self.assertIn('"GET"', text)
        self.assertNotIn("create_ref(", text)
        self.assertNotIn("process_request(", text)


if __name__ == "__main__":
    unittest.main()
