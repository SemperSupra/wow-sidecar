#!/usr/bin/env python3
"""Read-only controller observation for durable trusted-host request receipts."""
from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass
from typing import Any

from .errors import SidecarError
from .host_control import (
    CLAIM_REF_PREFIX,
    RECEIPT_SCHEMA,
    REVISION_RE,
    GitControlPlane,
    REQUEST_ID_RE,
    SHA_RE,
)


RECEIPT_FIELDS = {
    "schema",
    "request_id",
    "request_commit_sha",
    "claim_ref",
    "operator_profile",
    "operator_revision",
    "authority_record",
    "authority_revision",
    "authority_state",
    "result",
    "status",
    "workspace_mutation_performed",
    "retry_authorized",
    "promotion_performed",
    "release_performed",
    "details",
}


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise SidecarError(message)


def _request_id(value: Any) -> str:
    _require(isinstance(value, str) and bool(REQUEST_ID_RE.fullmatch(value)), "invalid host-operator request_id")
    return value


def _sha(value: Any, name: str) -> str:
    _require(isinstance(value, str) and bool(SHA_RE.fullmatch(value)), f"{name} must be a 40-hex Git SHA")
    return value


def _optional_nonempty(value: Any, name: str) -> str | None:
    if value is None:
        return None
    _require(isinstance(value, str) and bool(value.strip()) and value == value.strip(), f"invalid {name}")
    return value


def _optional_revision(value: Any) -> str | None:
    if value is None:
        return None
    _require(isinstance(value, str) and bool(REVISION_RE.fullmatch(value)), "invalid authority_revision")
    return value


def _optional_state(value: Any) -> str | None:
    if value is None:
        return None
    _require(value == "open", "invalid authority_state")
    return value


def _canonical_digest(value: dict[str, Any]) -> str:
    raw = json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode("utf-8")
    return "sha256:" + hashlib.sha256(raw).hexdigest()


@dataclass(frozen=True)
class HostOperatorReceipt:
    request_id: str
    request_commit_sha: str
    claim_ref: str
    operator_profile: str | None
    operator_revision: str | None
    authority_record: str | None
    authority_revision: str | None
    authority_state: str | None
    result: str
    status: str
    workspace_mutation_performed: bool | None
    raw: dict[str, Any]

    @classmethod
    def parse(cls, value: Any) -> "HostOperatorReceipt":
        _require(isinstance(value, dict), "receipt.json must be an object")
        _require(set(value) == RECEIPT_FIELDS, "receipt.json fields do not match the v1 schema")
        _require(value.get("schema") == RECEIPT_SCHEMA, "unsupported host-operator receipt schema")
        request_id = _request_id(value.get("request_id"))
        request_sha = _sha(value.get("request_commit_sha"), "receipt.request_commit_sha")
        _require(value.get("claim_ref") == CLAIM_REF_PREFIX + request_id, "receipt claim_ref does not match request_id")
        profile = _optional_nonempty(value.get("operator_profile"), "operator_profile")
        revision = value.get("operator_revision")
        if revision is not None:
            revision = _sha(revision, "receipt.operator_revision")
        authority = _optional_nonempty(value.get("authority_record"), "authority_record")
        authority_revision = _optional_revision(value.get("authority_revision"))
        authority_state = _optional_state(value.get("authority_state"))
        result = value.get("result")
        _require(result in {"ELIGIBLE", "REJECTED", "UNKNOWN"}, "invalid receipt result")
        status = value.get("status")
        _require(isinstance(status, str) and bool(status.strip()) and status == status.strip(), "invalid receipt status")
        mutation = value.get("workspace_mutation_performed")
        _require(mutation is None or isinstance(mutation, bool), "invalid workspace_mutation_performed")
        _require(value.get("retry_authorized") is False, "receipt must not authorize retry")
        _require(value.get("promotion_performed") is False, "receipt must not report promotion")
        _require(value.get("release_performed") is False, "receipt must not report release")
        _require(isinstance(value.get("details"), dict), "receipt.details must be an object")
        return cls(
            request_id=request_id,
            request_commit_sha=request_sha,
            claim_ref=value["claim_ref"],
            operator_profile=profile,
            operator_revision=revision,
            authority_record=authority,
            authority_revision=authority_revision,
            authority_state=authority_state,
            result=result,
            status=status,
            workspace_mutation_performed=mutation,
            raw=dict(value),
        )


def _receipt_parent_shas(control: GitControlPlane, receipt_commit_sha: str) -> list[str]:
    commit_sha = _sha(receipt_commit_sha, "receipt_commit_sha")
    value = control.client._request_json(
        "GET",
        f"/repos/{control.owner}/{control.repo}/git/commits/{commit_sha}",
        token=control._token(),
    )
    _require(isinstance(value, dict), "receipt commit response must be an object")
    parents = value.get("parents")
    _require(isinstance(parents, list), "receipt commit parents must be an array")
    out: list[str] = []
    for parent in parents:
        _require(isinstance(parent, dict), "receipt commit parent entry must be an object")
        out.append(_sha(parent.get("sha"), "receipt parent SHA"))
    return out


def _unknown(request_id: str, state: str, **evidence: Any) -> dict[str, Any]:
    return {
        "request_id": request_id,
        "state": state,
        "result": "UNKNOWN",
        "retry_authorized": False,
        "execution_authorized_by_observer": False,
        **evidence,
    }


def observe_request(control: GitControlPlane, request_id: str) -> dict[str, Any]:
    request_id = _request_id(request_id)
    requests = control.ref_map("requests")
    request_sha = requests.get(request_id)
    if request_sha is None:
        return _unknown(request_id, "request-absent")
    request_sha = _sha(request_sha, "request ref target")

    claims = control.ref_map("claims")
    receipts = control.ref_map("receipts")
    claim_sha = claims.get(request_id)
    receipt_sha = receipts.get(request_id)

    if claim_sha is None:
        if receipt_sha is not None:
            return _unknown(request_id, "receipt-without-claim", request_commit_sha=request_sha, receipt_commit_sha=receipt_sha)
        return {
            "request_id": request_id,
            "state": "unclaimed",
            "request_commit_sha": request_sha,
            "result": None,
            "execution_consumed": False,
            "execution_authorized_by_observer": False,
        }

    if claim_sha != request_sha:
        return _unknown(request_id, "claim-request-mismatch", request_commit_sha=request_sha, claim_commit_sha=claim_sha, receipt_commit_sha=receipt_sha)

    if receipt_sha is None:
        return _unknown(request_id, "claimed-without-receipt", request_commit_sha=request_sha, claim_commit_sha=claim_sha, execution_consumed=True)

    try:
        receipt_sha = _sha(receipt_sha, "receipt ref target")
        parents = _receipt_parent_shas(control, receipt_sha)
        _require(parents == [request_sha], "receipt commit is not rooted directly in the exact request commit")
        raw = control.read_json_at_commit(receipt_sha, "receipt.json")
        receipt = HostOperatorReceipt.parse(raw)
        _require(receipt.request_id == request_id, "receipt request_id does not match observed ref")
        _require(receipt.request_commit_sha == request_sha, "receipt request_commit_sha does not match request ref")
        _require(receipt.claim_ref == CLAIM_REF_PREFIX + request_id, "receipt claim_ref does not match observed claim")
    except Exception:
        return _unknown(
            request_id,
            "invalid-receipt-binding",
            request_commit_sha=request_sha,
            claim_commit_sha=claim_sha,
            receipt_commit_sha=receipt_sha,
            execution_consumed=True,
            reason="receipt schema or Git binding invalid; raw error omitted",
        )

    return {
        "request_id": request_id,
        "state": "receipted",
        "request_commit_sha": request_sha,
        "claim_commit_sha": claim_sha,
        "receipt_commit_sha": receipt_sha,
        "receipt_sha256": _canonical_digest(receipt.raw),
        "result": receipt.result,
        "status": receipt.status,
        "operator_profile": receipt.operator_profile,
        "operator_revision": receipt.operator_revision,
        "authority_record": receipt.authority_record,
        "authority_revision": receipt.authority_revision,
        "authority_state": receipt.authority_state,
        "workspace_mutation_performed": receipt.workspace_mutation_performed,
        "retry_authorized": False,
        "promotion_performed": False,
        "release_performed": False,
        "execution_consumed": True,
        "execution_authorized_by_observer": False,
        "binding": {
            "claim_matches_request": True,
            "receipt_parent_matches_request": True,
            "receipt_record_matches_request": True,
        },
    }
