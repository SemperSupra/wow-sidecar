from __future__ import annotations

import json
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

from wow_sidecar.errors import SidecarError
from wow_sidecar.host_control import HostOperatorRequest
from wow_sidecar import worker


REVISION = "a" * 40


class WorkerRuntimeTests(unittest.TestCase):
    def test_process_cycle_injects_registry_authority_and_forces_one_request(self):
        client = object()
        verifier = object()
        registry = {"proof": lambda _request: {"result": "ELIGIBLE"}}
        with (
            patch.object(worker, "GitControlPlane", return_value="control") as plane,
            patch.object(worker, "process_pending_once", return_value=[{"state": "receipted"}]) as process,
        ):
            result = worker.process_cycle(
                control_client=client,
                control_repository="example/control-private",
                registry=registry,
                local_operator_revision=REVISION,
                authority_verifier=verifier,
            )
        plane.assert_called_once_with(client, "example/control-private")
        kwargs = process.call_args.kwargs
        self.assertEqual(kwargs["control"], "control")
        self.assertIs(kwargs["registry"], registry)
        self.assertEqual(kwargs["local_operator_revision"], REVISION)
        self.assertIs(kwargs["authority_verifier"], verifier)
        self.assertEqual(kwargs["max_requests"], 1)
        self.assertEqual(result, [{"state": "receipted"}])

    def test_process_cycle_rejects_empty_registry_or_bad_repository(self):
        with self.assertRaises(SidecarError):
            worker.process_cycle(
                control_client=object(),
                control_repository="not-a-repository",
                registry={"proof": lambda _request: {}},
                local_operator_revision=REVISION,
                authority_verifier=lambda _request: {},
            )
        with self.assertRaises(SidecarError):
            worker.process_cycle(
                control_client=object(),
                control_repository="example/control",
                registry={},
                local_operator_revision=REVISION,
                authority_verifier=lambda _request: {},
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
