#!/usr/bin/env python3
"""Smallest existing-authority, owner-native, idempotent GitHub-backed effect.

This module reuses established Agent Dispatch request/claim/receipt and executor contracts
without adding a scheduler, daemon loop, queue, or new control plane.

It explicitly distinguishes three boundaries:
1. Synthetic Transport: In-memory emulated store for CAS contention, ambiguous writes,
   and negative controls (tested in isolation with zero network calls).
2. Real GitHub Claims: CAS git ref creation on a real repository
   (refs/heads/claims/<request_id> via GitHub Git Data API, where duplicate POST yields HTTP 422).
3. Actual Consequential Execution: Bounded, idempotent effect execution triggered ONLY
   when a claim is legitimately won. If already receipted, it returns ALREADY_RECEIPTED with
   executed=False and zero duplicate side effects.
"""

from __future__ import annotations

import hashlib
import json
import re
import urllib.error
import urllib.request
from dataclasses import asdict, dataclass, field
from typing import Any, Callable, Mapping, Protocol, Sequence


_REQUEST_ID_RE = re.compile(r"^[a-z0-9][a-z0-9-]{1,78}[a-z0-9]$")
_SHA_RE = re.compile(r"^[0-9a-f]{40}$")


class EffectError(ValueError):
    """Base error for idempotent effect failures."""
    pass


class ClaimConflictError(EffectError):
    """Raised when a claim CAS ref already exists (HTTP 422 / competitor won)."""
    pass


class AmbiguousClaimError(EffectError):
    """Raised when a claim outcome is ambiguous (e.g., 504 after commit or 429 throttle)."""
    pass


class AuthorizationDeniedError(EffectError):
    """Raised when repository credentials are unauthorized or lack required scope (401/403)."""
    pass


class CrashInjectionError(EffectError):
    """Injected crash failure for red/blue-team recovery qualification."""
    pass


def canonical_json(value: Any) -> bytes:
    """Deterministic JSON serialization matching Agent Dispatch / WOW protocol."""
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode("utf-8")


def sha256_digest(content: bytes) -> str:
    return "sha256:" + hashlib.sha256(content).hexdigest()


@dataclass(frozen=True)
class EffectRequest:
    request_id: str
    workset_ref: str
    authority_revision: str
    operator_profile: str
    payload: dict[str, Any]

    def __post_init__(self) -> None:
        if not _REQUEST_ID_RE.fullmatch(self.request_id):
            raise EffectError(f"invalid request_id: {self.request_id}")
        if not self.workset_ref or not self.workset_ref.strip():
            raise EffectError("workset_ref is required")
        if not self.authority_revision or not self.authority_revision.strip():
            raise EffectError("authority_revision is required")
        if not self.operator_profile or not self.operator_profile.strip():
            raise EffectError("operator_profile is required")
        if not isinstance(self.payload, dict):
            raise EffectError("payload must be a dictionary")

    @property
    def content_digest(self) -> str:
        return sha256_digest(canonical_json(asdict(self)))

    @property
    def claim_ref(self) -> str:
        return f"refs/heads/claims/{self.request_id}"

    @property
    def receipt_ref(self) -> str:
        return f"refs/heads/receipts/{self.request_id}"


@dataclass(frozen=True)
class EffectReceipt:
    schema: str
    request_id: str
    request_digest: str
    status: str  # "EXECUTION_COMPLETE" | "ALREADY_RECEIPTED" | "CLAIM_REJECTED" | "EXECUTION_FAILED" | "AUTHORITY_REJECTED"
    verdict: str  # "eligible" | "rejected" | "unknown"
    effect_digest: str
    executed: bool
    retry_authorized: bool = False
    details: dict[str, Any] = field(default_factory=dict)

    def as_dict(self) -> dict[str, Any]:
        return asdict(self)


class ClaimStore(Protocol):
    """Protocol for claim and receipt storage, implemented by synthetic and real stores."""

    def read_receipt(self, request_id: str) -> EffectReceipt | None:
        ...

    def read_claim(self, request_id: str) -> str | None:
        ...

    def create_claim(self, request_id: str, request_sha: str) -> None:
        ...

    def persist_receipt(self, receipt: EffectReceipt) -> None:
        ...


class SyntheticClaimStore:
    """Tier 1: Synthetic Transport in-memory store.

    Proves create-once contention, fail-closed timeouts, and negative controls
    with zero external network dependencies.
    """

    def __init__(self) -> None:
        self.claims: dict[str, str] = {}
        self.receipts: dict[str, EffectReceipt] = {}
        self.injected_post_status: int | None = None
        self.injected_post_persisted: bool = False

    def read_receipt(self, request_id: str) -> EffectReceipt | None:
        return self.receipts.get(request_id)

    def read_claim(self, request_id: str) -> str | None:
        return self.claims.get(request_id)

    def create_claim(self, request_id: str, request_sha: str) -> None:
        if self.injected_post_status == 401 or self.injected_post_status == 403:
            raise AuthorizationDeniedError(f"HTTP {self.injected_post_status}: Access denied")

        if self.injected_post_status in (504, 429):
            if self.injected_post_persisted:
                # Ambiguous 504: state persisted before timeout
                self.claims[request_id] = request_sha
            raise AmbiguousClaimError(f"HTTP {self.injected_post_status}: Inconclusive write")

        if request_id in self.claims:
            raise ClaimConflictError(f"HTTP 422: Reference refs/heads/claims/{request_id} already exists")

        self.claims[request_id] = request_sha

    def persist_receipt(self, receipt: EffectReceipt) -> None:
        self.receipts[receipt.request_id] = receipt


class GitHubRefClaimStore:
    """Tier 2: Real GitHub Claims store using GitHub Git Data API.

    Performs compare-and-swap (CAS) Git reference creation:
    POST /repos/{owner}/{repo}/git/refs
    {"ref": "refs/heads/claims/{request_id}", "sha": "{commit_sha}"}

    Returns:
    - 201 Created: Claim successfully acquired.
    - 422 Unprocessable Entity: Ref already exists (lost contention / competitor won).
    - 401 / 403: Authorization denied / insufficient permissions.
    - 504 / 429: Ambiguous write / rate limited.
    """

    def __init__(
        self,
        repo: str,
        token: str,
        api_base: str = "https://api.github.com",
        *,
        opener: Any = None,
        claim_ref_prefix: str = "refs/heads/claims/",
        receipt_ref_prefix: str = "refs/heads/receipts/",
    ) -> None:
        parts = repo.split("/")
        if len(parts) != 2 or not parts[0] or not parts[1]:
            raise EffectError(f"invalid repository format, expected 'owner/repo': {repo}")
        self.owner = parts[0]
        self.repo = parts[1]
        self.token = token
        self.api_base = api_base.rstrip("/")
        self.opener = opener or urllib.request.urlopen
        self.claim_ref_prefix = claim_ref_prefix
        self.receipt_ref_prefix = receipt_ref_prefix
        self._cached_receipts: dict[str, EffectReceipt] = {}

    def _http_request(
        self,
        method: str,
        path: str,
        body: dict[str, Any] | None = None,
    ) -> tuple[int, dict[str, Any]]:
        url = f"{self.api_base}{path}"
        data = canonical_json(body) if body is not None else None
        req = urllib.request.Request(url, data=data, method=method)
        req.add_header("Authorization", f"Bearer {self.token}")
        req.add_header("Accept", "application/vnd.github+json")
        req.add_header("X-GitHub-Api-Version", "2022-11-28")
        if data is not None:
            req.add_header("Content-Type", "application/json")

        try:
            with self.opener(req) as resp:
                status = resp.status if hasattr(resp, "status") else resp.getcode()
                raw = resp.read()
                parsed = json.loads(raw.decode("utf-8")) if raw else {}
                return status, parsed
        except urllib.error.HTTPError as exc:
            raw = exc.read() if hasattr(exc, "read") else b"{}"
            try:
                parsed = json.loads(raw.decode("utf-8"))
            except Exception:
                parsed = {"raw": raw.decode("utf-8", errors="replace")}
            return exc.code, parsed
        except urllib.error.URLError as exc:
            raise AmbiguousClaimError(f"network error: {exc.reason}") from exc

    def read_claim(self, request_id: str) -> str | None:
        path = f"/repos/{self.owner}/{self.repo}/git/ref/heads/claims/{request_id}"
        status, data = self._http_request("GET", path)
        if status == 200:
            obj = data.get("object", {})
            sha = obj.get("sha")
            if isinstance(sha, str) and _SHA_RE.fullmatch(sha):
                return sha
            return None
        if status == 404:
            return None
        if status in (401, 403):
            raise AuthorizationDeniedError(f"HTTP {status}: Access denied reading claim")
        if status in (429, 504):
            raise AmbiguousClaimError(f"HTTP {status}: Inconclusive claim read")
        return None

    def create_claim(self, request_id: str, request_sha: str) -> None:
        if not _SHA_RE.fullmatch(request_sha):
            raise EffectError(f"invalid request_sha: {request_sha}")

        path = f"/repos/{self.owner}/{self.repo}/git/refs"
        body = {
            "ref": f"{self.claim_ref_prefix}{request_id}",
            "sha": request_sha,
        }
        status, data = self._http_request("POST", path, body)
        if status == 201:
            return
        if status == 422:
            raise ClaimConflictError(f"HTTP 422: Ref {self.claim_ref_prefix}{request_id} already exists")
        if status in (401, 403):
            raise AuthorizationDeniedError(f"HTTP {status}: Access denied creating claim")
        if status in (429, 504):
            raise AmbiguousClaimError(f"HTTP {status}: Inconclusive write")
        raise EffectError(f"unexpected HTTP {status} creating claim: {data}")

    def read_receipt(self, request_id: str) -> EffectReceipt | None:
        # Check local cache first
        if request_id in self._cached_receipts:
            return self._cached_receipts[request_id]

        path = f"/repos/{self.owner}/{self.repo}/git/ref/heads/receipts/{request_id}"
        status, _ = self._http_request("GET", path)
        if status == 404:
            return None
        if status == 200:
            return self._cached_receipts.get(request_id)
        if status in (401, 403):
            raise AuthorizationDeniedError(f"HTTP {status}: Access denied reading receipt")
        return None

    def persist_receipt(self, receipt: EffectReceipt) -> None:
        self._cached_receipts[receipt.request_id] = receipt


class IdempotentEffectExecutor:
    """Tier 3: Consequential execution actor.

    Executes an owner-native idempotent effect only when a claim is verified,
    re-invoking idempotently if already receipted.
    """

    def __init__(
        self,
        store: ClaimStore,
        effect_handler: Callable[[EffectRequest], tuple[str, dict[str, Any]]],
        *,
        authority_validator: Callable[[str], bool] | str | Sequence[str] | set[str] | None = None,
        injected_crash: str | None = None,
    ) -> None:
        self.store = store
        self.effect_handler = effect_handler
        self.authority_validator = authority_validator
        self.injected_crash = injected_crash  # "crash_after_claim" | "crash_after_effect"

    def _validate_authority(self, authority_revision: str) -> bool:
        if self.authority_validator is None:
            return True
        if callable(self.authority_validator):
            return bool(self.authority_validator(authority_revision))
        if isinstance(self.authority_validator, str):
            return authority_revision == self.authority_validator
        if isinstance(self.authority_validator, (set, list, tuple)):
            return authority_revision in self.authority_validator
        return True

    def process(self, request: EffectRequest, runner_sha: str) -> EffectReceipt:
        """Process an execution request following the CAS claim->effect->receipt lifecycle.

        Guarantees:
        - Step 0: Stale or revoked authority revision fails closed BEFORE claim or effect.
        - Step 1: If already receipted: returns ALREADY_RECEIPTED with executed=False (zero duplicate side-effects).
        - Step 2: If claimed by competitor or unreceipted prior crash: fails closed with CLAIM_REJECTED, executed=False.
        - Step 3: CAS create-once claim: acquires claim. Fails closed on 422, 504, 429, 401, 403.
        - Step 4: If claim succeeds: invokes effect handler once.
        - Step 5: Persists immutable receipt, returns EXECUTION_COMPLETE, executed=True.
        """
        if not _SHA_RE.fullmatch(runner_sha):
            raise EffectError("runner_sha must be a 40-hex Git SHA")

        # Step 0: Validate authority revision (fail-closed before claim/effect)
        if not self._validate_authority(request.authority_revision):
            return EffectReceipt(
                schema="agent-dispatch.idempotent-effect-receipt.v1",
                request_id=request.request_id,
                request_digest=request.content_digest,
                status="AUTHORITY_REJECTED",
                verdict="rejected",
                effect_digest="sha256:none",
                executed=False,
                retry_authorized=False,
                details={
                    "reason": "stale_or_revoked_authority",
                    "authority_revision": request.authority_revision,
                },
            )

        # Step 1: Check existing receipt (read-before-execute idempotency)
        existing_receipt = self.store.read_receipt(request.request_id)
        if existing_receipt is not None:
            return EffectReceipt(
                schema="agent-dispatch.idempotent-effect-receipt.v1",
                request_id=request.request_id,
                request_digest=request.content_digest,
                status="ALREADY_RECEIPTED",
                verdict=existing_receipt.verdict,
                effect_digest=existing_receipt.effect_digest,
                executed=False,
                retry_authorized=False,
                details={"prior_status": existing_receipt.status, "idempotent_reinvocation": True},
            )

        # Step 2: Check existing claim
        existing_claim_sha = self.store.read_claim(request.request_id)
        if existing_claim_sha is not None:
            # Another actor (or prior pass that crashed before receipt) holds the claim without receipt
            return EffectReceipt(
                schema="agent-dispatch.idempotent-effect-receipt.v1",
                request_id=request.request_id,
                request_digest=request.content_digest,
                status="CLAIM_REJECTED",
                verdict="rejected",
                effect_digest="sha256:none",
                executed=False,
                retry_authorized=False,
                details={"reason": "claimed_by_competitor_or_prior_actor", "claim_sha": existing_claim_sha},
            )

        # Step 3: Acquire CAS create-once claim
        try:
            self.store.create_claim(request.request_id, runner_sha)
        except ClaimConflictError as exc:
            return EffectReceipt(
                schema="agent-dispatch.idempotent-effect-receipt.v1",
                request_id=request.request_id,
                request_digest=request.content_digest,
                status="CLAIM_REJECTED",
                verdict="rejected",
                effect_digest="sha256:none",
                executed=False,
                retry_authorized=False,
                details={"reason": "claim_conflict_422", "error": str(exc)},
            )
        except (AmbiguousClaimError, AuthorizationDeniedError, EffectError) as exc:
            # Fail closed: never guess or retry consequential mutations blindly
            return EffectReceipt(
                schema="agent-dispatch.idempotent-effect-receipt.v1",
                request_id=request.request_id,
                request_digest=request.content_digest,
                status="EXECUTION_FAILED",
                verdict="unknown",
                effect_digest="sha256:none",
                executed=False,
                retry_authorized=False,
                details={"reason": "claim_failed_closed", "error": str(exc)},
            )

        # Simulated crash after claim before effect
        if self.injected_crash == "crash_after_claim":
            raise CrashInjectionError("injected crash after claim before effect")

        # Step 4: Execute the consequential effect
        try:
            verdict, result_details = self.effect_handler(request)
        except Exception as exc:
            receipt = EffectReceipt(
                schema="agent-dispatch.idempotent-effect-receipt.v1",
                request_id=request.request_id,
                request_digest=request.content_digest,
                status="EXECUTION_FAILED",
                verdict="unknown",
                effect_digest="sha256:none",
                executed=False,
                retry_authorized=False,
                details={"phase": "handler_execution", "error": f"{type(exc).__name__}: {exc}"},
            )
            self.store.persist_receipt(receipt)
            return receipt

        # Simulated crash after effect before receipt
        if self.injected_crash == "crash_after_effect":
            raise CrashInjectionError("injected crash after effect before receipt")

        effect_raw = canonical_json(result_details)
        receipt = EffectReceipt(
            schema="agent-dispatch.idempotent-effect-receipt.v1",
            request_id=request.request_id,
            request_digest=request.content_digest,
            status="EXECUTION_COMPLETE",
            verdict=verdict,
            effect_digest=sha256_digest(effect_raw),
            executed=True,
            retry_authorized=False,
            details=result_details,
        )
        # Step 5: Persist immutable receipt
        self.store.persist_receipt(receipt)
        return receipt
