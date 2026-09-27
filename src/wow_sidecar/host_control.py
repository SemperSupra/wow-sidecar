#!/usr/bin/env python3
"""Durable request/claim/receipt control for bounded trusted-host operators.

This module intentionally does not accept shell commands. A caller supplies a
code-defined registry of pre-qualified operator profiles. Coordination refs are
non-authoritative; every executable request must be bound to canonical authority
that is reread independently before the registered handler is invoked.
"""

from __future__ import annotations

import base64
import hashlib
import json
import re
import urllib.parse
from dataclasses import dataclass
from typing import Any, Callable, Mapping

from .errors import SidecarError


REQUEST_SCHEMA = "agent-dispatch.host-operator-request.v1"
RECEIPT_SCHEMA = "agent-dispatch.host-operator-receipt.v1"
REQUEST_REF_PREFIX = "refs/heads/requests/"
CLAIM_REF_PREFIX = "refs/heads/claims/"
RECEIPT_REF_PREFIX = "refs/heads/receipts/"
REQUEST_ID_RE = re.compile(r"^[a-z0-9][a-z0-9-]{2,79}$")
SHA_RE = re.compile(r"^[0-9a-f]{40}$")
REVISION_RE = re.compile(r"^sha256:[0-9a-f]{64}$")


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise SidecarError(message)


def _canonical_json(value: Any) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode("utf-8")


def _repository_parts(repository: str) -> tuple[str, str]:
    _require(isinstance(repository, str) and repository.count("/") == 1, "repository must be owner/name")
    owner, repo = repository.split("/", 1)
    _require(bool(owner) and bool(repo), "repository must be owner/name")
    return owner, repo


def _request_id(value: Any) -> str:
    _require(isinstance(value, str) and bool(REQUEST_ID_RE.fullmatch(value)), "invalid host-operator request_id")
    return value


@dataclass(frozen=True)
class HostOperatorRequest:
    request_id: str
    operator_profile: str
    operator_revision: str
    authority_record: str
    authority_revision: str
    authority_state: str
    inputs: dict[str, Any]
    constraints: dict[str, Any]
    raw: dict[str, Any]

    @classmethod
    def parse(cls, value: Any) -> "HostOperatorRequest":
        _require(isinstance(value, dict), "request.json must be an object")
        expected = {
            "schema",
            "request_id",
            "operator_profile",
            "operator_revision",
            "authority",
            "inputs",
            "constraints",
        }
        _require(set(value) == expected, "request.json fields do not match the v1 schema")
        _require(value.get("schema") == REQUEST_SCHEMA, "unsupported host-operator request schema")
        request_id = _request_id(value.get("request_id"))
        profile = value.get("operator_profile")
        _require(isinstance(profile, str) and bool(profile.strip()) and profile == profile.strip(), "invalid operator_profile")
        revision = value.get("operator_revision")
        _require(isinstance(revision, str) and bool(SHA_RE.fullmatch(revision)), "operator_revision must be a 40-hex Git SHA")
        authority = value.get("authority")
        _require(isinstance(authority, dict), "authority must be an object")
        _require(set(authority) == {"record", "revision", "state"}, "authority fields do not match the v1 schema")
        authority_record = authority.get("record")
        authority_revision = authority.get("revision")
        authority_state = authority.get("state")
        _require(isinstance(authority_record, str) and bool(authority_record.strip()), "authority.record is required")
        _require(isinstance(authority_revision, str) and bool(REVISION_RE.fullmatch(authority_revision)), "authority.revision is invalid")
        _require(authority_state == "open", "authority.state must be open")
        inputs = value.get("inputs")
        constraints = value.get("constraints")
        _require(isinstance(inputs, dict), "inputs must be an object")
        _require(isinstance(constraints, dict), "constraints must be an object")
        required_constraints = {
            "no_retry": True,
            "promotion_performed": False,
            "release_performed": False,
        }
        for key, expected_value in required_constraints.items():
            _require(constraints.get(key) is expected_value, f"constraint {key} must be {expected_value!r}")
        return cls(
            request_id=request_id,
            operator_profile=profile,
            operator_revision=revision,
            authority_record=authority_record,
            authority_revision=authority_revision,
            authority_state=authority_state,
            inputs=dict(inputs),
            constraints=dict(constraints),
            raw=dict(value),
        )


class GitControlPlane:
    """Minimal Git-data adapter over an existing GitHubAppClient."""

    def __init__(self, client: Any, repository: str):
        self.client = client
        self.repository = repository
        self.owner, self.repo = _repository_parts(repository)

    def _token(self) -> str:
        return self.client.installation_token_for_repository(self.repository)

    def matching_refs(self, logical_prefix: str) -> list[dict[str, Any]]:
        _require(logical_prefix in {"requests", "claims", "receipts"}, "unsupported control ref prefix")
        path = f"/repos/{self.owner}/{self.repo}/git/matching-refs/heads/{logical_prefix}/"
        value = self.client._request_json("GET", path, token=self._token(), expected_shape="array")
        _require(isinstance(value, list), "matching refs response must be an array")
        return value

    def ref_map(self, logical_prefix: str) -> dict[str, str]:
        prefix = f"refs/heads/{logical_prefix}/"
        out: dict[str, str] = {}
        for index, item in enumerate(self.matching_refs(logical_prefix)):
            _require(isinstance(item, dict), f"{logical_prefix} ref entry {index} is not an object")
            ref = item.get("ref")
            obj = item.get("object")
            sha = obj.get("sha") if isinstance(obj, dict) else None
            _require(isinstance(ref, str) and ref.startswith(prefix), f"unexpected {logical_prefix} ref")
            request_id = _request_id(ref[len(prefix):])
            _require(isinstance(sha, str) and bool(SHA_RE.fullmatch(sha)), f"{ref} lacks a commit SHA")
            _require(request_id not in out, f"duplicate {logical_prefix} request id")
            out[request_id] = sha
        return out

    def read_json_at_commit(self, commit_sha: str, path: str) -> dict[str, Any]:
        _require(bool(SHA_RE.fullmatch(commit_sha)), "commit_sha must be a 40-hex Git SHA")
        _require(isinstance(path, str) and path and not path.startswith("/") and ".." not in path.split("/"), "invalid control file path")
        encoded = urllib.parse.quote(path, safe="/")
        query = urllib.parse.urlencode({"ref": commit_sha})
        value = self.client._request_json(
            "GET",
            f"/repos/{self.owner}/{self.repo}/contents/{encoded}?{query}",
            token=self._token(),
        )
        _require(value.get("type") == "file", f"{path} is not a file")
        _require(value.get("encoding") == "base64" and isinstance(value.get("content"), str), f"{path} is not base64 content")
        try:
            raw = base64.b64decode(value["content"], validate=False)
            parsed = json.loads(raw.decode("utf-8"))
        except (ValueError, UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise SidecarError(f"{path} is not valid UTF-8 JSON") from exc
        _require(isinstance(parsed, dict), f"{path} must contain a JSON object")
        return parsed

    def create_ref(self, ref: str, sha: str) -> dict[str, Any]:
        _require(ref.startswith("refs/heads/"), "control ref must be a heads ref")
        _require(bool(SHA_RE.fullmatch(sha)), "control ref target must be a 40-hex Git SHA")
        return self.client._request_json(
            "POST",
            f"/repos/{self.owner}/{self.repo}/git/refs",
            token=self._token(),
            body={"ref": ref, "sha": sha},
        )

    def create_receipt_commit(self, *, request_id: str, request_sha: str, receipt: Mapping[str, Any]) -> tuple[str, str]:
        request_id = _request_id(request_id)
        _require(bool(SHA_RE.fullmatch(request_sha)), "request_sha must be a 40-hex Git SHA")
        raw = _canonical_json(dict(receipt))
        blob = self.client._request_json(
            "POST",
            f"/repos/{self.owner}/{self.repo}/git/blobs",
            token=self._token(),
            body={"content": raw.decode("utf-8"), "encoding": "utf-8"},
        )
        blob_sha = blob.get("sha")
        _require(isinstance(blob_sha, str) and bool(SHA_RE.fullmatch(blob_sha)), "GitHub did not return receipt blob SHA")
        tree = self.client._request_json(
            "POST",
            f"/repos/{self.owner}/{self.repo}/git/trees",
            token=self._token(),
            body={"tree": [{"path": "receipt.json", "mode": "100644", "type": "blob", "sha": blob_sha}]},
        )
        tree_sha = tree.get("sha")
        _require(isinstance(tree_sha, str) and bool(SHA_RE.fullmatch(tree_sha)), "GitHub did not return receipt tree SHA")
        commit = self.client._request_json(
            "POST",
            f"/repos/{self.owner}/{self.repo}/git/commits",
            token=self._token(),
            body={
                "message": f"record host-operator receipt {request_id}",
                "tree": tree_sha,
                "parents": [request_sha],
            },
        )
        commit_sha = commit.get("sha")
        _require(isinstance(commit_sha, str) and bool(SHA_RE.fullmatch(commit_sha)), "GitHub did not return receipt commit SHA")
        ref = RECEIPT_REF_PREFIX + request_id
        self.create_ref(ref, commit_sha)
        return commit_sha, "sha256:" + hashlib.sha256(raw).hexdigest()


OperatorHandler = Callable[[HostOperatorRequest], dict[str, Any]]
AuthorityVerifier = Callable[[HostOperatorRequest], Mapping[str, Any]]


def _receipt(
    *,
    request_id: str,
    request_sha: str,
    result: str,
    status: str,
    operator_profile: str | None,
    operator_revision: str | None,
    authority_record: str | None,
    authority_revision: str | None,
    authority_state: str | None,
    mutation_state: Any,
    details: Mapping[str, Any] | None = None,
) -> dict[str, Any]:
    _require(result in {"ELIGIBLE", "REJECTED", "UNKNOWN"}, "invalid receipt result")
    return {
        "schema": RECEIPT_SCHEMA,
        "request_id": request_id,
        "request_commit_sha": request_sha,
        "claim_ref": CLAIM_REF_PREFIX + request_id,
        "operator_profile": operator_profile,
        "operator_revision": operator_revision,
        "authority_record": authority_record,
        "authority_revision": authority_revision,
        "authority_state": authority_state,
        "result": result,
        "status": status,
        "workspace_mutation_performed": mutation_state,
        "retry_authorized": False,
        "promotion_performed": False,
        "release_performed": False,
        "details": dict(details or {}),
    }


def process_request(
    *,
    control: GitControlPlane,
    request_id: str,
    request_sha: str,
    registry: Mapping[str, OperatorHandler],
    local_operator_revision: str,
    authority_verifier: AuthorityVerifier,
) -> dict[str, Any]:
    """Claim and process one request exactly once after canonical authority reread."""

    request_id = _request_id(request_id)
    _require(bool(SHA_RE.fullmatch(request_sha)), "request ref target must be a 40-hex Git SHA")
    _require(bool(SHA_RE.fullmatch(local_operator_revision)), "local operator revision must be a 40-hex Git SHA")

    receipt_refs = control.ref_map("receipts")
    if request_id in receipt_refs:
        return {
            "request_id": request_id,
            "state": "already-receipted",
            "receipt_commit_sha": receipt_refs[request_id],
            "executed": False,
        }

    claim_refs = control.ref_map("claims")
    if request_id in claim_refs:
        return {
            "request_id": request_id,
            "state": "claimed-without-receipt",
            "request_commit_sha": request_sha,
            "claim_commit_sha": claim_refs[request_id],
            "claim_matches_request": claim_refs[request_id] == request_sha,
            "result": "UNKNOWN",
            "retry_authorized": False,
            "executed": False,
        }

    try:
        control.create_ref(CLAIM_REF_PREFIX + request_id, request_sha)
    except Exception:
        claim_refs = control.ref_map("claims")
        if request_id in claim_refs:
            return {
                "request_id": request_id,
                "state": "claimed-without-receipt",
                "request_commit_sha": request_sha,
                "claim_commit_sha": claim_refs[request_id],
                "claim_matches_request": claim_refs[request_id] == request_sha,
                "result": "UNKNOWN",
                "retry_authorized": False,
                "executed": False,
            }
        raise

    request: HostOperatorRequest | None = None
    handler_invoked = False
    try:
        raw = control.read_json_at_commit(request_sha, "request.json")
        request = HostOperatorRequest.parse(raw)
        _require(request.request_id == request_id, "request ref id does not match request.json")
        _require(
            request.operator_revision == local_operator_revision,
            "request operator_revision does not match the trusted host checkout",
        )
        handler = registry.get(request.operator_profile)
        _require(handler is not None, "operator_profile is not registered on this host")
        authority_evidence = authority_verifier(request)
        _require(isinstance(authority_evidence, Mapping), "authority verifier must return an object")
        handler_invoked = True
        details = handler(request)
        _require(isinstance(details, dict), "operator handler must return an object")
        result = details.get("result")
        _require(result in {"ELIGIBLE", "REJECTED", "UNKNOWN"}, "operator handler returned invalid result")
        mutation_state = details.get("workspace_mutation_performed")
        receipt_details = dict(details)
        receipt_details["authority_verified"] = True
        receipt = _receipt(
            request_id=request_id,
            request_sha=request_sha,
            result=result,
            status=str(details.get("status") or "host-operator-complete"),
            operator_profile=request.operator_profile,
            operator_revision=request.operator_revision,
            authority_record=request.authority_record,
            authority_revision=request.authority_revision,
            authority_state=request.authority_state,
            mutation_state=mutation_state,
            details=receipt_details,
        )
    except Exception as exc:
        receipt = _receipt(
            request_id=request_id,
            request_sha=request_sha,
            result="UNKNOWN",
            status="host-operator-failed-closed",
            operator_profile=request.operator_profile if request else None,
            operator_revision=request.operator_revision if request else None,
            authority_record=request.authority_record if request else None,
            authority_revision=request.authority_revision if request else None,
            authority_state=request.authority_state if request else None,
            mutation_state=None,
            details={
                "phase": "request-authority-validation-or-execution",
                "reason": f"{type(exc).__name__}: raw error omitted",
                "preserve_evidence": True,
            },
        )

    commit_sha, receipt_digest = control.create_receipt_commit(
        request_id=request_id,
        request_sha=request_sha,
        receipt=receipt,
    )
    return {
        "request_id": request_id,
        "state": "receipted",
        "result": receipt["result"],
        "receipt_commit_sha": commit_sha,
        "receipt_sha256": receipt_digest,
        "executed": handler_invoked,
    }


def process_pending_once(
    *,
    control: GitControlPlane,
    registry: Mapping[str, OperatorHandler],
    local_operator_revision: str,
    authority_verifier: AuthorityVerifier,
    max_requests: int = 1,
) -> list[dict[str, Any]]:
    _require(max_requests == 1, "host-control worker may process only one request per cycle")
    requests = control.ref_map("requests")
    claims = control.ref_map("claims")
    receipts = control.ref_map("receipts")
    pending = [request_id for request_id in sorted(requests) if request_id not in claims and request_id not in receipts]
    if not pending:
        return []
    request_id = pending[0]
    return [
        process_request(
            control=control,
            request_id=request_id,
            request_sha=requests[request_id],
            registry=registry,
            local_operator_revision=local_operator_revision,
            authority_verifier=authority_verifier,
        )
    ]
