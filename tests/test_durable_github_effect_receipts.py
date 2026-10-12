"""Credentialless GitHub-API emulation for durable receipt recovery.

This qualifies source behavior against an in-process Git Data mock, not real
GitHub App installation or live release/dispatch authority.
"""
from __future__ import annotations

import base64
import hashlib
import http.server
import json
import subprocess
import sys
import threading
import unittest
from urllib.parse import parse_qs, urlsplit

from wow_sidecar.idempotent_github_effect import (
    EffectError,
    EffectRequest,
    GitHubRefClaimStore,
    IdempotentEffectExecutor,
    canonical_json,
)


class _GitDataHarness:
    def __init__(self):
        self.lock = threading.Lock()
        self.refs = {}
        self.blobs = {}
        self.trees = {}
        self.commits = {}
        self.events = []
        self.malformed_claim_ref = False

    def handle(self, method, route, data):
        root = "/repos/test-org/test-repo"
        if route.startswith(root + "/git/ref/heads/"):
            ref = "refs/heads/" + route.removeprefix(root + "/git/ref/heads/")
            if self.malformed_claim_ref and ref.startswith("refs/heads/claims/"):
                return 200, {"object": {"type": "commit", "sha": "not-a-git-sha"}}
            with self.lock:
                sha = self.refs.get(ref)
            return (200, {"ref": ref, "object": {"type": "commit", "sha": sha}}) if sha else (404, {"message": "not found"})
        if method == "GET" and route.startswith(root + "/contents/receipt.json"):
            qs = parse_qs(urlsplit(route).query)
            commit_sha = qs.get("ref", [""])[0]
            with self.lock:
                tree_sha = self.commits.get(commit_sha, {}).get("tree")
                blob_sha = self.trees.get(tree_sha)
                raw = self.blobs.get(blob_sha)
            if raw is None:
                return 404, {"message": "not found"}
            return 200, {"type": "file", "encoding": "base64",
                         "content": base64.b64encode(raw).decode("ascii")}
        if method != "POST":
            return 404, {"message": "invalid route"}
        if route == root + "/git/blobs":
            if data.get("encoding") != "utf-8":
                return 400, {}
            raw = data["content"].encode("utf-8")
            sha = hashlib.sha1(b"blob " + str(len(raw)).encode() + b"\\0" + raw).hexdigest()
            with self.lock:
                self.blobs[sha] = raw
            return 201, {"sha": sha}
        if route == root + "/git/trees":
            entry = data["tree"][0]
            if entry["path"] != "receipt.json" or entry["sha"] not in self.blobs:
                return 422, {}
            sha = hashlib.sha1(canonical_json(data)).hexdigest()
            with self.lock:
                self.trees[sha] = entry["sha"]
            return 201, {"sha": sha}
        if route == root + "/git/commits":
            if len(data.get("parents", [])) != 1 or data["tree"] not in self.trees:
                return 422, {}
            sha = hashlib.sha1(canonical_json(data)).hexdigest()
            with self.lock:
                self.commits[sha] = {"tree": data["tree"], "parent": data["parents"][0]}
            return 201, {"sha": sha}
        if route == root + "/git/refs":
            ref, sha = data["ref"], data["sha"]
            with self.lock:
                if ref in self.refs:
                    return 422, {"message": "already exists"}
                self.refs[ref] = sha
                self.events.append((ref, sha))
            return 201, {"ref": ref, "object": {"sha": sha}}
        return 404, {"message": "invalid route"}


class TestDurableGitHubEffectReceipts(unittest.TestCase):
    def setUp(self):
        self.data = _GitDataHarness()
        harness = self.data
        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, fmt, *args):
                return
            def _handle(self):
                raw = self.rfile.read(int(self.headers.get("Content-Length", 0))) if self.command == "POST" else b""
                data = json.loads(raw.decode("utf-8")) if raw else {}
                status, result = harness.handle(self.command, self.path, data)
                encoded = canonical_json(result)
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(encoded)))
                self.end_headers()
                self.wfile.write(encoded)
            def do_GET(self):
                self._handle()
            def do_POST(self):
                self._handle()
        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.worker = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.worker.start()
        self.api_base = "http://127.0.0.1:" + str(self.server.server_port)
        self.revision = "a" * 40
        self.runner_sha = "b" * 40
        self.request = EffectRequest(
            request_id="durable-suprasession-001",
            workset_ref="public-synthetic-workstream",
            authority_revision=self.revision,
            operator_profile="public-synthetic",
            payload={"sequence": 1},
        )
        self.calls = []

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.worker.join(timeout=3)

    def store(self):
        return GitHubRefClaimStore(
            repo="test-org/test-repo", token="synthetic-only",
            api_base=self.api_base,
        )

    def executor(self, store, validator=None):
        return IdempotentEffectExecutor(
            store,
            lambda request: (self.calls.append(request.request_id) or "eligible",
                             {"accepted": True, "request_id": request.request_id}),
            authority_validator=(validator if validator is not None else lambda revision: revision == self.revision),
        )

    def test_receipt_survives_fresh_store_and_no_duplicate_effect(self):
        first = self.executor(self.store()).process(self.request, self.runner_sha)
        self.assertEqual(first.status, "EXECUTION_COMPLETE")
        self.assertEqual(len(self.calls), 1)
        self.assertTrue(self.data.refs["refs/heads/receipts/" + self.request.request_id])
        # New client/store/executor has NO shared memory receipt cache.
        new_store = self.store()
        recovered = new_store.read_receipt(self.request.request_id)
        self.assertIsNotNone(recovered)
        self.assertEqual(recovered.request_digest, self.request.content_digest)
        second = self.executor(new_store).process(self.request, self.runner_sha)
        self.assertEqual(second.status, "ALREADY_RECEIPTED")
        self.assertFalse(second.executed)
        self.assertEqual(len(self.calls), 1)

    def test_claim_bound_receipt_commit_parent(self):
        self.executor(self.store()).process(self.request, self.runner_sha)
        ref_sha = self.data.refs["refs/heads/receipts/" + self.request.request_id]
        self.assertEqual(self.data.commits[ref_sha]["parent"], self.runner_sha)
        self.assertIn(("refs/heads/claims/" + self.request.request_id, self.runner_sha), self.data.events)

    def test_same_id_different_payload_cannot_reuse_accepted_receipt(self):
        self.executor(self.store()).process(self.request, self.runner_sha)
        replacement = EffectRequest(
            request_id=self.request.request_id,
            workset_ref=self.request.workset_ref,
            authority_revision=self.request.authority_revision,
            operator_profile=self.request.operator_profile,
            payload={"sequence": 2},
        )
        result = self.executor(self.store()).process(replacement, self.runner_sha)
        self.assertEqual(result.status, "CLAIM_REJECTED")
        self.assertEqual(result.verdict, "unknown")
        self.assertFalse(result.executed)
        self.assertEqual(len(self.calls), 1)

    def test_missing_receipt_body_fails_closed_no_effect(self):
        self.executor(self.store()).process(self.request, self.runner_sha)
        ref_sha = self.data.refs["refs/heads/receipts/" + self.request.request_id]
        del self.data.commits[ref_sha]
        with self.assertRaises(EffectError):
            self.executor(self.store()).process(self.request, self.runner_sha)
        self.assertEqual(len(self.calls), 1)

    def test_revocation_between_claim_and_effect_blocks_handler(self):
        state = {"validations": 0}
        def validator(_):
            state["validations"] += 1
            return state["validations"] == 1
        result = self.executor(self.store(), validator).process(self.request, self.runner_sha)
        self.assertEqual(result.status, "AUTHORITY_REJECTED")
        self.assertEqual(len(self.calls), 0)
        self.assertIn("refs/heads/claims/" + self.request.request_id, self.data.refs)
        self.assertNotIn("refs/heads/receipts/" + self.request.request_id, self.data.refs)

    def test_separate_interpreters_recover_durable_receipt(self):
        # Parent owns only synthetic Git Data HTTP state; process A's memory
        # is discarded before B observes the same durable Git receipt ref.
        source = """import json, sys
from wow_sidecar.idempotent_github_effect import (
    EffectRequest, GitHubRefClaimStore, IdempotentEffectExecutor
)
request = EffectRequest(
    request_id="durable-suprasession-001",
    workset_ref="public-synthetic-workstream",
    authority_revision="a" * 40,
    operator_profile="public-synthetic",
    payload={"sequence": 1},
)
def effect(_):
    print("HANDLER_FIRED", file=sys.stderr)
    return ("eligible", {"accepted": True, "request_id": request.request_id})
store = GitHubRefClaimStore(
    repo="test-org/test-repo", token="synthetic-only", api_base=sys.argv[1],
)
executor = IdempotentEffectExecutor(
    store, effect, authority_validator=lambda rev: rev == "a" * 40
)
result = executor.process(request, "b" * 40)
print(json.dumps(result.as_dict(), sort_keys=True))
"""
        def fresh_run():
            completed = subprocess.run(
                [sys.executable, "-c", source, self.api_base],
                check=True, capture_output=True, text=True,
                timeout=20,
            )
            return json.loads(completed.stdout), completed.stderr

        first, first_err = fresh_run()
        self.assertEqual(first["status"], "EXECUTION_COMPLETE")
        self.assertIn("HANDLER_FIRED", first_err)
        ref = self.data.refs["refs/heads/receipts/" + self.request.request_id]
        self.assertEqual(self.data.commits[ref]["parent"], self.runner_sha)

        second, second_err = fresh_run()
        self.assertEqual(second["status"], "ALREADY_RECEIPTED")
        self.assertFalse(second["executed"])
        self.assertNotIn("HANDLER_FIRED", second_err)
        self.assertEqual(second["effect_digest"], first["effect_digest"])

    def test_malformed_claim_read_stops_before_create_or_effect(self):
        self.data.malformed_claim_ref = True
        with self.assertRaises(EffectError):
            self.executor(self.store()).process(self.request, self.runner_sha)
        self.assertEqual(self.calls, [])
        self.assertEqual(self.data.events, [])

    def test_real_store_requires_live_callable_authority_validator(self):
        for validator in (None, self.revision, [self.revision]):
            exe = IdempotentEffectExecutor(
                self.store(), lambda request: self.fail("handler must not execute"),
                authority_validator=validator,
            )
            with self.assertRaises(EffectError):
                exe.process(self.request, self.runner_sha)
        self.assertEqual(self.data.events, [])


if __name__ == "__main__":
    unittest.main()
