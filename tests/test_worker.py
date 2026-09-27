from __future__ import annotations

import json
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

from wow_sidecar.errors import SidecarError
from wow_sidecar.host_control import HostOperatorRequest, REQUEST_SCHEMA
from wow_sidecar import worker


REVISION = "a" * 40
AUTH_REV = "sha256:" + "b" * 64
PROFILE = "synthetic-proof"
AUTH_RECORD = "github-issue-comment:ExampleOrg/authority#7:11"


def request(*, profile=PROFILE, record=AUTH_RECORD, revision=AUTH_REV):
    return HostOperatorRequest.parse(
        {
            "schema": REQUEST_SCHEMA,
            "request_id": "synthetic-proof-001",
            "operator_profile": profile,
            "operator_revision": REVISION,
            "authority": {
                "record": record,
                "revision": revision,
                "state": "open",
            },
            "inputs": {},
            "constraints": {
                "no_retry": True,
                "promotion_performed": False,
                "release_performed": False,
            },
        }
    )


def handler(_request):
    return {
        "result": "ELIGIBLE",
        "status": "synthetic-proof-complete",
        "workspace_mutation_performed": False,
    }


class FakeAuthorityReader:
    def __init__(self):
        self.calls = []

    def verify(self, *, record, expected_revision, expected_state):
        self.calls.append((record, expected_revision, expected_state))
        return {
            "record": record,
            "revision": expected_revision,
            "issue_state": expected_state,
            "verified": True,
        }


class WorkerRuntimeTests(unittest.TestCase):
    def test_registry_and_authority_profiles_must_match_exactly(self):
        with self.assertRaisesRegex(SidecarError, "identical profile sets"):
            worker.validate_registry(
                {PROFILE: handler},
                {"other-profile": worker.AuthorityBinding(AUTH_RECORD)},
            )

    def test_exact_authority_binding_is_enforced(self):
        reader = FakeAuthorityReader()
        evidence = worker.verify_request_authority(
            reader,
            request(),
            {PROFILE: worker.AuthorityBinding(AUTH_RECORD, AUTH_REV)},
        )
        self.assertTrue(evidence["verified"])
        self.assertEqual(reader.calls, [(AUTH_RECORD, AUTH_REV, "open")])

    def test_pinned_revision_rejects_drift_before_authority_reader(self):
        reader = FakeAuthorityReader()
        with self.assertRaisesRegex(SidecarError, "revision is outside"):
            worker.verify_request_authority(
                reader,
                request(revision="sha256:" + "c" * 64),
                {PROFILE: worker.AuthorityBinding(AUTH_RECORD, AUTH_REV)},
            )
        self.assertEqual(reader.calls, [])

    def test_unpinned_binding_still_rereads_exact_request_revision(self):
        reader = FakeAuthorityReader()
        worker.verify_request_authority(
            reader,
            request(),
            {PROFILE: worker.AuthorityBinding(AUTH_RECORD)},
        )
        self.assertEqual(reader.calls, [(AUTH_RECORD, AUTH_REV, "open")])

    def test_process_cycle_injects_exact_registry_authority_and_forces_one_request(self):
        client = object()
        reader = FakeAuthorityReader()
        registry = {PROFILE: handler}
        bindings = {PROFILE: worker.AuthorityBinding(AUTH_RECORD)}
        with (
            patch.object(worker, "GitControlPlane", return_value="control") as plane,
            patch.object(worker, "process_pending_once", return_value=[{"state": "receipted"}]) as process,
        ):
            result = worker.process_cycle(
                control_client=client,
                authority_reader=reader,
                control_repository="ExampleOrg/control",
                registry=registry,
                authority_bindings=bindings,
                local_operator_revision=REVISION,
            )
        plane.assert_called_once_with(client, "ExampleOrg/control")
        kwargs = process.call_args.kwargs
        self.assertEqual(kwargs["control"], "control")
        self.assertIs(kwargs["registry"], registry)
        self.assertEqual(kwargs["local_operator_revision"], REVISION)
        self.assertEqual(kwargs["max_requests"], 1)
        request_value = request()
        evidence = kwargs["authority_verifier"](request_value)
        self.assertTrue(evidence["verified"])
        self.assertEqual(reader.calls, [(AUTH_RECORD, AUTH_REV, "open")])
        self.assertEqual(result, [{"state": "receipted"}])

    def test_process_cycle_rejects_empty_registry_bad_repository_or_bad_revision(self):
        common = {
            "control_client": object(),
            "authority_reader": FakeAuthorityReader(),
            "registry": {PROFILE: handler},
            "authority_bindings": {PROFILE: worker.AuthorityBinding(AUTH_RECORD)},
            "local_operator_revision": REVISION,
        }
        with self.assertRaises(SidecarError):
            worker.process_cycle(control_repository="not-a-repository", **common)
        with self.assertRaises(SidecarError):
            worker.process_cycle(
                control_client=object(),
                authority_reader=FakeAuthorityReader(),
                control_repository="ExampleOrg/control",
                registry={},
                authority_bindings={},
                local_operator_revision=REVISION,
            )
        with self.assertRaisesRegex(SidecarError, "40-hex"):
            worker.process_cycle(
                control_client=object(),
                authority_reader=FakeAuthorityReader(),
                control_repository="ExampleOrg/control",
                registry={PROFILE: handler},
                authority_bindings={PROFILE: worker.AuthorityBinding(AUTH_RECORD)},
                local_operator_revision="latest",
            )

    def test_local_checkout_revision_requires_clean_exact_sha(self):
        with patch.object(worker.subprocess, "run", side_effect=[
            subprocess.CompletedProcess(["git"], 0, REVISION + "\n", ""),
            subprocess.CompletedProcess(["git"], 0, "", ""),
        ]):
            self.assertEqual(worker.local_checkout_revision(Path("/tmp/example")), REVISION)

        with patch.object(worker.subprocess, "run", side_effect=[
            subprocess.CompletedProcess(["git"], 0, REVISION + "\n", ""),
            subprocess.CompletedProcess(["git"], 0, " M file.py\n", ""),
        ]):
            with self.assertRaisesRegex(SidecarError, "dirty"):
                worker.local_checkout_revision(Path("/tmp/example"))

    def test_event_is_sanitized_no_retry(self):
        value = json.loads(worker.event("UNKNOWN", "failed", preserve_evidence=True))
        self.assertEqual(value["result"], "UNKNOWN")
        self.assertEqual(value["status"], "failed")
        self.assertFalse(value["retry_authorized"])
        self.assertTrue(value["preserve_evidence"])

    def test_service_bounds_poll_interval(self):
        for value in (0, 4, 3601):
            with self.subTest(value=value):
                with self.assertRaisesRegex(SidecarError, "poll interval"):
                    worker.run_service(lambda: [], poll_seconds=value)

    def test_service_failure_emits_unknown_then_continues(self):
        calls = []
        outputs = []

        def cycle():
            calls.append("cycle")
            if len(calls) == 1:
                raise RuntimeError("secret detail must not escape")
            raise KeyboardInterrupt

        result = worker.run_service(
            cycle,
            poll_seconds=5,
            sleep=lambda _seconds: None,
            emit=outputs.append,
        )
        self.assertEqual(result, 0)
        self.assertEqual(len(outputs), 1)
        value = json.loads(outputs[0])
        self.assertEqual(value["result"], "UNKNOWN")
        self.assertEqual(value["status"], "host-control-cycle-failed-closed")
        self.assertFalse(value["retry_authorized"])
        self.assertNotIn("secret detail", outputs[0])

    def test_service_emits_only_nonempty_success_cycles(self):
        outputs = []
        calls = 0

        def cycle():
            nonlocal calls
            calls += 1
            if calls == 1:
                return []
            if calls == 2:
                return [{"state": "receipted"}]
            raise KeyboardInterrupt

        result = worker.run_service(
            cycle,
            poll_seconds=5,
            sleep=lambda _seconds: None,
            emit=outputs.append,
        )
        self.assertEqual(result, 0)
        self.assertEqual(len(outputs), 1)
        value = json.loads(outputs[0])
        self.assertEqual(value["status"], "host-control-cycle-complete")
        self.assertEqual(value["results"], [{"state": "receipted"}])


if __name__ == "__main__":
    unittest.main()
