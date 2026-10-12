#!/usr/bin/env python3
"""Comprehensive unit and red/blue-team qualification tests for Idempotent GitHub Effect Adapter.

Qualifies:
1. Claim ownership (only claimant executes).
2. Crash-after-claim (unreceipted claim fails closed to CLAIM_REJECTED, zero effect calls).
3. Crash-after-effect-before-receipt (claim held, no receipt -> fail closed on recovery, zero duplicate calls).
4. Ambiguous POST (504 post-commit / 429 pre-commit fail closed with zero blind retries).
5. Stale authority (authority revision revoked/stale rejected before claim or effect).
6. Competitor claims (concurrent contention -> exactly 1 win, competitors get CLAIM_REJECTED).
7. Duplicate consequence prevention (ALREADY_RECEIPTED -> executed=False, handler calls strictly 1).
8. Tier 2 wire simulation (GitHubRefClaimStore with HTTP mock server for 201, 422, 401, 403, 504, 429).
"""

from __future__ import annotations

import concurrent.futures
import http.server
import json
import threading
import unittest
from typing import Any

from wow_sidecar.idempotent_github_effect import (
    AmbiguousClaimError,
    AuthorizationDeniedError,
    ClaimConflictError,
    CrashInjectionError,
    EffectError,
    EffectReceipt,
    EffectRequest,
    GitHubRefClaimStore,
    IdempotentEffectExecutor,
    SyntheticClaimStore,
    canonical_json,
    sha256_digest,
)


class CountingHandler:
    def __init__(self, verdict: str = "eligible", task_name: str = "test-task") -> None:
        self.verdict = verdict
        self.task_name = task_name
        self.call_count = 0
        self.lock = threading.Lock()

    def __call__(self, req: EffectRequest) -> tuple[str, dict[str, Any]]:
        with self.lock:
            self.call_count += 1
            call_idx = self.call_count
        return self.verdict, {
            "task_name": self.task_name,
            "call_index": call_idx,
            "request_id": req.request_id,
            "output_digest": sha256_digest(f"result-{req.request_id}-{call_idx}".encode("utf-8")),
        }


class TestIdempotentGitHubEffect(unittest.TestCase):

    def setUp(self) -> None:
        self.store = SyntheticClaimStore()
        self.handler = CountingHandler()
        self.approved_authority = "135dfc44b8586d1833b09f644910fa14cdcce263"
        self.executor = IdempotentEffectExecutor(
            self.store,
            self.handler,
            authority_validator=self.approved_authority,
        )
        self.request = EffectRequest(
            request_id="supra-test-req-001",
            workset_ref="SemperSupra/agent-dispatch@main",
            authority_revision=self.approved_authority,
            operator_profile="disposable-public-runner",
            payload={"task": "qualify-idempotent-effect"},
        )
        self.runner_sha = "1111111111111111111111111111111111111111"

    # Vector 1: Claim Ownership
    def test_claim_ownership_only_claimant_executes(self) -> None:
        receipt = self.executor.process(self.request, self.runner_sha)
        self.assertEqual(receipt.status, "EXECUTION_COMPLETE")
        self.assertEqual(receipt.verdict, "eligible")
        self.assertTrue(receipt.executed)
        self.assertEqual(self.handler.call_count, 1)
        self.assertEqual(self.store.read_claim(self.request.request_id), self.runner_sha)

    # Vector 2: Crash-After-Claim (fails closed to UNKNOWN/CLAIM_REJECTED on retry)
    def test_crash_after_claim_fails_closed(self) -> None:
        crashed_executor = IdempotentEffectExecutor(
            self.store,
            self.handler,
            authority_validator=self.approved_authority,
            injected_crash="crash_after_claim",
        )
        with self.assertRaises(CrashInjectionError):
            crashed_executor.process(self.request, self.runner_sha)

        # Claim was created before crash, but effect handler NEVER ran
        self.assertEqual(self.store.read_claim(self.request.request_id), self.runner_sha)
        self.assertIsNone(self.store.read_receipt(self.request.request_id))
        self.assertEqual(self.handler.call_count, 0)

        # Recovery run by normal executor: detects unreceipted claim and fails closed
        recovery_receipt = self.executor.process(self.request, self.runner_sha)
        self.assertEqual(recovery_receipt.status, "CLAIM_REJECTED")
        self.assertEqual(recovery_receipt.verdict, "rejected")
        self.assertFalse(recovery_receipt.executed)
        self.assertFalse(recovery_receipt.retry_authorized)
        # Handler was still NEVER called
        self.assertEqual(self.handler.call_count, 0)

    # Vector 3: Crash-After-Effect-Before-Receipt (fail closed, no duplicate consequence)
    def test_crash_after_effect_before_receipt(self) -> None:
        crashed_executor = IdempotentEffectExecutor(
            self.store,
            self.handler,
            authority_validator=self.approved_authority,
            injected_crash="crash_after_effect",
        )
        with self.assertRaises(CrashInjectionError):
            crashed_executor.process(self.request, self.runner_sha)

        # Effect ran once before crash, but receipt was NOT persisted
        self.assertEqual(self.handler.call_count, 1)
        self.assertEqual(self.store.read_claim(self.request.request_id), self.runner_sha)
        self.assertIsNone(self.store.read_receipt(self.request.request_id))

        # Recovery attempt detects existing unreceipted claim: must NOT re-execute effect
        recovery_receipt = self.executor.process(self.request, self.runner_sha)
        self.assertEqual(recovery_receipt.status, "CLAIM_REJECTED")
        self.assertEqual(recovery_receipt.verdict, "rejected")
        self.assertFalse(recovery_receipt.executed)
        # Handler call count is strictly 1 (never called a second time)
        self.assertEqual(self.handler.call_count, 1)

    # Vector 4: Ambiguous POST (504 post-commit and 429 pre-commit fail closed)
    def test_ambiguous_504_post_write_fails_closed(self) -> None:
        self.store.injected_post_status = 504
        self.store.injected_post_persisted = True  # Ref did persist remotely before timeout

        receipt = self.executor.process(self.request, self.runner_sha)
        self.assertEqual(receipt.status, "EXECUTION_FAILED")
        self.assertEqual(receipt.verdict, "unknown")
        self.assertFalse(receipt.executed)
        self.assertFalse(receipt.retry_authorized)
        self.assertEqual(self.handler.call_count, 0)

    def test_throttled_429_pre_write_fails_closed(self) -> None:
        self.store.injected_post_status = 429
        self.store.injected_post_persisted = False

        receipt = self.executor.process(self.request, self.runner_sha)
        self.assertEqual(receipt.status, "EXECUTION_FAILED")
        self.assertEqual(receipt.verdict, "unknown")
        self.assertFalse(receipt.executed)
        self.assertFalse(receipt.retry_authorized)
        self.assertEqual(self.handler.call_count, 0)
        self.assertIsNone(self.store.read_claim(self.request.request_id))

    def test_auth_denial_401_and_403_fails_closed(self) -> None:
        for status in (401, 403):
            store = SyntheticClaimStore()
            store.injected_post_status = status
            handler = CountingHandler()
            executor = IdempotentEffectExecutor(store, handler, authority_validator=self.approved_authority)
            receipt = executor.process(self.request, self.runner_sha)
            self.assertEqual(receipt.status, "EXECUTION_FAILED")
            self.assertEqual(receipt.verdict, "unknown")
            self.assertFalse(receipt.executed)
            self.assertEqual(handler.call_count, 0)
            self.assertIsNone(store.read_claim(self.request.request_id))

    # Vector 5: Stale Authority (rejected before claim or effect)
    def test_stale_or_revoked_authority_rejected(self) -> None:
        stale_request = EffectRequest(
            request_id="supra-stale-req-002",
            workset_ref="SemperSupra/agent-dispatch@main",
            authority_revision="0000000000000000000000000000000000000000",  # Stale / revoked
            operator_profile="disposable-public-runner",
            payload={"task": "qualify-idempotent-effect"},
        )
        receipt = self.executor.process(stale_request, self.runner_sha)
        self.assertEqual(receipt.status, "AUTHORITY_REJECTED")
        self.assertEqual(receipt.verdict, "rejected")
        self.assertFalse(receipt.executed)
        self.assertFalse(receipt.retry_authorized)
        self.assertEqual(self.handler.call_count, 0)
        self.assertIsNone(self.store.read_claim(stale_request.request_id))
        self.assertIsNone(self.store.read_receipt(stale_request.request_id))

    # Vector 6: Competitor Claims (concurrent contention -> 1 win, rest rejected)
    def test_concurrent_claim_contention_single_winner(self) -> None:
        competitors = 8
        runner_shas = [f"{i:040x}" for i in range(1, competitors + 1)]
        receipts: list[EffectReceipt] = []

        with concurrent.futures.ThreadPoolExecutor(max_workers=competitors) as pool:
            futures = [pool.submit(self.executor.process, self.request, sha) for sha in runner_shas]
            for future in concurrent.futures.as_completed(futures):
                receipts.append(future.result())

        executed = [r for r in receipts if r.status == "EXECUTION_COMPLETE"]
        rejected_or_receipted = [r for r in receipts if r.status in ("CLAIM_REJECTED", "ALREADY_RECEIPTED")]

        self.assertEqual(len(executed), 1)
        self.assertEqual(len(rejected_or_receipted), competitors - 1)
        self.assertTrue(executed[0].executed)
        self.assertEqual(self.handler.call_count, 1)

    # Vector 7: Duplicate Consequence Prevention (re-invocation yields ALREADY_RECEIPTED)
    def test_duplicate_consequence_prevention(self) -> None:
        r1 = self.executor.process(self.request, self.runner_sha)
        self.assertEqual(r1.status, "EXECUTION_COMPLETE")
        self.assertTrue(r1.executed)
        self.assertEqual(self.handler.call_count, 1)

        # 5 subsequent re-invocations on same request
        for _ in range(5):
            rx = self.executor.process(self.request, self.runner_sha)
            self.assertEqual(rx.status, "ALREADY_RECEIPTED")
            self.assertFalse(rx.executed)
            self.assertEqual(rx.effect_digest, r1.effect_digest)
            self.assertEqual(rx.verdict, r1.verdict)
            self.assertFalse(rx.retry_authorized)

        # Total effect executions remains strictly 1
        self.assertEqual(self.handler.call_count, 1)

    # Negative Controls: Malformed inputs
    def test_malformed_identity_rejected(self) -> None:
        for malformed in ("", "has spaces", "UPPERCASE", "../traversal", "-leading-dash", "a"):
            with self.assertRaises(EffectError):
                EffectRequest(
                    request_id=malformed,
                    workset_ref="repo@main",
                    authority_revision="rev",
                    operator_profile="prof",
                    payload={},
                )

        for bad_sha in ("", "too-short", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"):
            with self.assertRaises(EffectError):
                self.executor.process(self.request, bad_sha)

    def test_handler_exception_records_failed_receipt(self) -> None:
        def failing_handler(req: EffectRequest) -> tuple[str, dict[str, Any]]:
            raise RuntimeError("simulated handler fault")

        failing_executor = IdempotentEffectExecutor(
            self.store,
            failing_handler,
            authority_validator=self.approved_authority,
        )
        receipt = failing_executor.process(self.request, self.runner_sha)
        self.assertEqual(receipt.status, "EXECUTION_FAILED")
        self.assertEqual(receipt.verdict, "unknown")
        self.assertFalse(receipt.executed)
        self.assertIsNotNone(self.store.read_receipt(self.request.request_id))


class MockGitHubHandler(http.server.BaseHTTPRequestHandler):
    """In-process mock server simulating GitHub Git Data API."""

    store_refs: dict[str, str] = {}
    injected_status: int | None = None

    def log_message(self, format: str, *args: Any) -> None:
        pass  # Suppress stdout logging during test

    def do_GET(self) -> None:
        if self.injected_status is not None:
            self.send_response(self.injected_status)
            self.end_headers()
            self.wfile.write(b'{"error": "injected"}')
            return

        if self.path.startswith("/repos/test-org/test-repo/git/ref/heads/claims/"):
            req_id = self.path.rsplit("/", 1)[-1]
            ref_name = f"refs/heads/claims/{req_id}"
            if ref_name in self.store_refs:
                sha = self.store_refs[ref_name]
                body = canonical_json({"ref": ref_name, "object": {"sha": sha, "type": "commit"}})
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(body)
                return
            self.send_response(404)
            self.end_headers()
            self.wfile.write(b'{"message": "Not Found"}')
            return

        self.send_response(404)
        self.end_headers()

    def do_POST(self) -> None:
        if self.injected_status is not None:
            self.send_response(self.injected_status)
            self.end_headers()
            self.wfile.write(b'{"error": "injected"}')
            return

        if self.path == "/repos/test-org/test-repo/git/refs":
            length = int(self.headers.get("Content-Length", 0))
            payload = json.loads(self.rfile.read(length).decode("utf-8"))
            ref = payload.get("ref", "")
            sha = payload.get("sha", "")
            if ref in self.store_refs:
                self.send_response(422)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(canonical_json({"message": "Reference already exists"}))
                return
            self.store_refs[ref] = sha
            self.send_response(201)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(canonical_json({"ref": ref, "object": {"sha": sha, "type": "commit"}}))
            return

        self.send_response(404)
        self.end_headers()


class TestGitHubRefClaimStoreWireProtocol(unittest.TestCase):
    """Tier 2: Real wire protocol simulation for GitHubRefClaimStore."""

    @classmethod
    def setUpClass(cls) -> None:
        MockGitHubHandler.store_refs = {}
        MockGitHubHandler.injected_status = None
        cls.server = http.server.HTTPServer(("127.0.0.1", 0), MockGitHubHandler)
        cls.server_port = cls.server.server_port
        cls.server_thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.server_thread.start()

    @classmethod
    def tearDownClass(cls) -> None:
        cls.server.shutdown()
        cls.server.server_close()

    def setUp(self) -> None:
        MockGitHubHandler.store_refs = {}
        MockGitHubHandler.injected_status = None
        self.api_base = f"http://127.0.0.1:{self.server_port}"
        self.store = GitHubRefClaimStore(
            repo="test-org/test-repo",
            token="ghp_testtoken12345",
            api_base=self.api_base,
        )
        self.sha = "3333333333333333333333333333333333333333"

    def test_create_and_read_claim_success(self) -> None:
        # Initially claim does not exist (404 -> None)
        self.assertIsNone(self.store.read_claim("test-wire-001"))

        # Create claim (201 Created)
        self.store.create_claim("test-wire-001", self.sha)

        # Read back claim (200 OK -> sha)
        self.assertEqual(self.store.read_claim("test-wire-001"), self.sha)

    def test_duplicate_create_claim_raises_claim_conflict_422(self) -> None:
        self.store.create_claim("test-wire-002", self.sha)
        with self.assertRaises(ClaimConflictError):
            self.store.create_claim("test-wire-002", self.sha)

    def test_auth_denial_401_and_403(self) -> None:
        for code in (401, 403):
            MockGitHubHandler.injected_status = code
            with self.assertRaises(AuthorizationDeniedError):
                self.store.create_claim("test-wire-auth", self.sha)

    def test_ambiguous_outcomes_504_and_429(self) -> None:
        for code in (504, 429):
            MockGitHubHandler.injected_status = code
            with self.assertRaises(AmbiguousClaimError):
                self.store.create_claim("test-wire-ambiguous", self.sha)


if __name__ == "__main__":
    unittest.main()
