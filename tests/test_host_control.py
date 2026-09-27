#!/usr/bin/env python3

from __future__ import annotations

import unittest

from wow_sidecar.errors import SidecarError
from wow_sidecar.host_control import (
    CLAIM_REF_PREFIX,
    HostOperatorRequest,
    REQUEST_SCHEMA,
    process_pending_once,
    process_request,
)


REQ_SHA = "1" * 40
OP_SHA = "2" * 40
RECEIPT_SHA = "3" * 40
AUTH_REV = "sha256:" + "a" * 64
AUTH_RECORD = "github-issue-comment:example/repository#64:1234567890"


def request_value(**overrides):
    value = {
        "schema": REQUEST_SCHEMA,
        "request_id": "host-control-proof-001",
        "operator_profile": "host-control-proof",
        "operator_revision": OP_SHA,
        "authority": {
            "record": AUTH_RECORD,
            "revision": AUTH_REV,
            "state": "open",
        },
        "inputs": {"nonce": "proof-001"},
        "constraints": {
            "no_retry": True,
            "promotion_performed": False,
            "release_performed": False,
        },
    }
    value.update(overrides)
    return value


def authority_ok(request):
    return {
        "record": request.authority_record,
        "revision": request.authority_revision,
        "issue_state": request.authority_state,
        "verified": True,
    }


class FakeControl:
    def __init__(self, request=None):
        self.maps = {"requests": {}, "claims": {}, "receipts": {}}
        self.request = request if request is not None else request_value()
        self.created_refs = []
        self.receipts = []
        self.fail_claim_once = False

    def ref_map(self, prefix):
        return dict(self.maps[prefix])

    def create_ref(self, ref, sha):
        self.created_refs.append((ref, sha))
        if ref.startswith(CLAIM_REF_PREFIX):
            request_id = ref[len(CLAIM_REF_PREFIX):]
            if self.fail_claim_once:
                self.fail_claim_once = False
                self.maps["claims"][request_id] = sha
                raise SidecarError("simulated concurrent claim")
            self.maps["claims"][request_id] = sha
        return {"ref": ref, "object": {"sha": sha}}

    def read_json_at_commit(self, commit_sha, path):
        self.assertions = (commit_sha, path)
        return self.request

    def create_receipt_commit(self, *, request_id, request_sha, receipt):
        self.receipts.append((request_id, request_sha, receipt))
        self.maps["receipts"][request_id] = RECEIPT_SHA
        return RECEIPT_SHA, "sha256:" + "4" * 64


class HostOperatorRequestTests(unittest.TestCase):
    def test_rejects_shell_string_extension(self):
        value = request_value()
        value["command"] = "rm -rf /"
        with self.assertRaisesRegex(SidecarError, "fields do not match"):
            HostOperatorRequest.parse(value)

    def test_rejects_self_asserted_human_authority(self):
        value = request_value()
        value["authority"] = {
            "human_in_command": True,
            "record": AUTH_RECORD,
        }
        with self.assertRaisesRegex(SidecarError, "authority fields"):
            HostOperatorRequest.parse(value)

    def test_requires_pinned_authority_revision(self):
        value = request_value()
        value["authority"] = dict(value["authority"], revision="latest")
        with self.assertRaisesRegex(SidecarError, "authority.revision"):
            HostOperatorRequest.parse(value)

    def test_requires_no_retry_constraint(self):
        value = request_value()
        value["constraints"] = dict(value["constraints"], no_retry=False)
        with self.assertRaisesRegex(SidecarError, "no_retry"):
            HostOperatorRequest.parse(value)


class ProcessRequestTests(unittest.TestCase):
    def test_success_rereads_authority_claims_once_and_publishes_receipt(self):
        control = FakeControl()
        calls = []
        authority_calls = []

        def handler(request):
            calls.append(request.request_id)
            return {
                "result": "ELIGIBLE",
                "status": "proof-ready",
                "workspace_mutation_performed": False,
            }

        def verify(request):
            authority_calls.append(request.authority_record)
            return authority_ok(request)

        result = process_request(
            control=control,
            request_id="host-control-proof-001",
            request_sha=REQ_SHA,
            registry={"host-control-proof": handler},
            local_operator_revision=OP_SHA,
            authority_verifier=verify,
        )
        self.assertEqual(authority_calls, [AUTH_RECORD])
        self.assertEqual(calls, ["host-control-proof-001"])
        self.assertEqual(result["result"], "ELIGIBLE")
        self.assertTrue(result["executed"])
        self.assertEqual(control.created_refs, [(CLAIM_REF_PREFIX + "host-control-proof-001", REQ_SHA)])
        receipt = control.receipts[0][2]
        self.assertEqual(receipt["authority_record"], AUTH_RECORD)
        self.assertEqual(receipt["authority_revision"], AUTH_REV)
        self.assertTrue(receipt["details"]["authority_verified"])
        self.assertFalse(receipt["retry_authorized"])

    def test_authority_failure_is_unknown_and_handler_not_invoked(self):
        control = FakeControl()
        called = []

        def reject_authority(_request):
            raise SidecarError("canonical authority revision is stale")

        result = process_request(
            control=control,
            request_id="host-control-proof-001",
            request_sha=REQ_SHA,
            registry={"host-control-proof": lambda _request: called.append(True)},
            local_operator_revision=OP_SHA,
            authority_verifier=reject_authority,
        )
        self.assertEqual(called, [])
        self.assertEqual(result["result"], "UNKNOWN")
        self.assertFalse(result["executed"])
        receipt = control.receipts[0][2]
        self.assertEqual(receipt["status"], "host-operator-failed-closed")
        self.assertFalse(receipt["retry_authorized"])

    def test_existing_claim_never_rereads_or_executes(self):
        control = FakeControl()
        control.maps["claims"]["host-control-proof-001"] = REQ_SHA
        called = []

        result = process_request(
            control=control,
            request_id="host-control-proof-001",
            request_sha=REQ_SHA,
            registry={"host-control-proof": lambda _request: called.append(True)},
            local_operator_revision=OP_SHA,
            authority_verifier=lambda _request: called.append("authority"),
        )
        self.assertEqual(called, [])
        self.assertEqual(result["result"], "UNKNOWN")
        self.assertFalse(result["retry_authorized"])
        self.assertFalse(result["executed"])
        self.assertEqual(control.receipts, [])

    def test_operator_revision_mismatch_is_receipted_unknown_without_handler(self):
        control = FakeControl()
        called = []

        result = process_request(
            control=control,
            request_id="host-control-proof-001",
            request_sha=REQ_SHA,
            registry={"host-control-proof": lambda _request: called.append(True)},
            local_operator_revision="9" * 40,
            authority_verifier=authority_ok,
        )
        self.assertEqual(called, [])
        self.assertEqual(result["result"], "UNKNOWN")
        self.assertFalse(result["executed"])

    def test_concurrent_claim_loser_does_not_execute(self):
        control = FakeControl()
        control.fail_claim_once = True
        called = []

        result = process_request(
            control=control,
            request_id="host-control-proof-001",
            request_sha=REQ_SHA,
            registry={"host-control-proof": lambda _request: called.append(True)},
            local_operator_revision=OP_SHA,
            authority_verifier=authority_ok,
        )
        self.assertEqual(called, [])
        self.assertEqual(result["state"], "claimed-without-receipt")
        self.assertFalse(result["executed"])
        self.assertEqual(control.receipts, [])

    def test_pending_cycle_processes_at_most_one_request(self):
        control = FakeControl()
        control.maps["requests"] = {
            "host-control-proof-001": REQ_SHA,
            "host-control-proof-002": "5" * 40,
        }
        result = process_pending_once(
            control=control,
            registry={"host-control-proof": lambda _request: {"result": "ELIGIBLE", "workspace_mutation_performed": False}},
            local_operator_revision=OP_SHA,
            authority_verifier=authority_ok,
            max_requests=1,
        )
        self.assertEqual(len(result), 1)
        self.assertEqual(result[0]["request_id"], "host-control-proof-001")


if __name__ == "__main__":
    unittest.main()
