# Go protocol conformance baseline (G0)

Parent: #26

This document freezes the behavioral contract that the Go WOW runtime must preserve before it can replace the currently qualified Python implementation.

Oracle source revision:

`17e2be6faefdf7efbfea0bd97d790269ee8b377c`

Primary oracle modules:

- `src/wow_sidecar/host_control.py`
- `src/wow_sidecar/host_authority.py`
- `src/wow_sidecar/worker.py`
- `src/wow_sidecar/profiles.py`
- `src/wow_sidecar/host_control_observer.py`

The executable language-neutral vectors live in:

`conformance/v1/vectors.json`

Python must continue to pass those vectors while the Go implementation is developed. Go must consume the same vectors rather than inventing a second expected-result corpus.

## Frozen product behavior

### Request

Schema:

`agent-dispatch.host-operator-request.v1`

A request is admitted only when:

- the top-level field set is exact;
- `request_id` matches `^[a-z0-9][a-z0-9-]{2,79}$`;
- `operator_profile` is a normalized non-empty string;
- `operator_revision` is exactly 40 lowercase hexadecimal characters;
- authority contains exactly `record`, `revision`, and `state`;
- authority record is non-empty;
- authority revision is `sha256:<64 lower-hex>`;
- authority state is exactly `open`;
- `inputs` and `constraints` are objects;
- `no_retry=true`;
- `promotion_performed=false`;
- `release_performed=false`.

Unknown or extra request fields fail closed.

### Claim-before-execution

One request is claimed before authority reread or handler invocation.

If a receipt already exists, the request is not executed.

If a claim already exists without a receipt, the result is `UNKNOWN`, retry is not authorized, and the handler is not invoked.

If a concurrent claimant wins while this worker is creating the claim, the loser observes the claim and does not execute.

A worker processes at most one pending request in a cycle.

### Exact authority

Execution-control refs are coordination state, not authority.

Before a handler executes:

1. request authority must match the locally admitted profile authority;
2. if the local binding pins an authority revision, the request must match it;
3. request authority state must be `open`;
4. canonical GitHub issue-comment authority is reread;
5. the exact authority revision must verify.

Authority ambiguity or drift is a fail-closed `UNKNOWN`; it never authorizes an automatic retry.

### Operator admission

Schema:

`wow-sidecar.operator-profile.v1`

Version 1 admits only the `pinned-repository` operator kind.

The profile document has an exact field set. It cannot carry:

- an arbitrary shell command;
- request-supplied executable/path;
- request-supplied arguments;
- ambient host authentication.

The current legacy operator may use Git + Bash internally, but this is a compatibility implementation detail, not a generic WOW command surface.

### Receipt

Schema:

`agent-dispatch.host-operator-receipt.v1`

Every terminal receipt:

- binds the exact request ID and request commit;
- binds `claim_ref` to `refs/heads/claims/<request_id>`;
- records operator and authority identities when parsing reached them;
- uses result `ELIGIBLE | REJECTED | UNKNOWN`;
- always has `retry_authorized=false`;
- always has `promotion_performed=false`;
- always has `release_performed=false`;
- contains an object-valued `details`;
- is serialized as canonical JSON with sorted keys and no insignificant whitespace before hashing/committing.

Raw exception details are not allowed to escape in fail-closed receipts or service events.

### Observation

Receipt observation is read-only.

A receipt is accepted only when:

- request, claim, and receipt refs bind to the same request identity;
- the claim points at the exact request commit;
- the receipt commit has the exact request commit as its sole parent;
- `receipt.json` matches the exact receipt schema and identity fields.

Any malformed or mismatched binding becomes `UNKNOWN`.

Observation never authorizes execution or retry.

### Service loop

Polling is a runtime option, not a protocol requirement.

- valid poll interval: 5..3600 seconds;
- an empty cycle emits nothing;
- a non-empty successful cycle emits a sanitized eligible event;
- a cycle exception emits sanitized `UNKNOWN`, preserves evidence, and does not expose the raw exception;
- event-driven wake may invoke the same bounded process cycle directly.

## Explicit implementation accidents not frozen

The following are not semantic requirements for the Go runtime:

- Python;
- setuptools/wheelhouse packaging;
- CPython 3.12;
- Python exception class names;
- the current Python module/file layout;
- running the core daemon from a Git checkout;
- polling as the only wake mechanism;
- Git+Bash inside the minimal core image.

Any replacement must nevertheless preserve the externally observable protocol/security behavior above until a separately versioned protocol intentionally changes it.

## Migration gate

Go may become the default runtime only after:

1. the Python oracle still passes the conformance vectors;
2. Go passes the same vectors;
3. differential tests show equivalent request/claim/receipt outcomes;
4. cross-architecture amd64/arm64 results agree;
5. the legacy pinned-repository capability is migrated or retained through an explicit bounded compatibility adapter;
6. no authority or privilege envelope widens.
