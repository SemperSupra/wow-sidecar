# G6.11e peer rendezvous runtime composition

Qualification projection for the bounded G6.11e runtime-composition slice.

The Go daemon can conditionally register the already-qualified peer rendezvous
handler. The default runtime remains unchanged: no peer mutation route exists
unless peer rendezvous is explicitly enabled.

## Admission

Required opt-in:

- `WOW_PEER_RENDEZVOUS_ENABLED=true`
- `WOW_PEER_CREDENTIALS_FILE=/absolute/path/to/0600/credentials.json`

Optional bounded timing:

- `WOW_PEER_MAX_SKEW_SECONDS`: 1-600, default 120
- `WOW_PEER_REPLAY_TTL_SECONDS`: 1-1800, default 300

Setting any peer-rendezvous option without explicit enablement fails closed.
`false` is accepted only when no peer-rendezvous options are present.

The credential environment value is a path only. Pairwise key material remains
inside the G6.11b file-backed credential document and is never placed in
environment values, cards, status documents, receipts, or logs.

When enabled, startup also requires:

- durable control successfully configured;
- local rendezvous configured;
- at least one configured peer observer endpoint;
- a valid local embodiment card;
- a valid file-backed credential document.

A missing prerequisite aborts startup before the HTTP server begins serving.

## Runtime route

Only one route is added:

- `POST /v1/peer/rendezvous`

It is the G6.11c bounded handler; there is no generic invoke route.

The runtime binds:

- destination identity to the current local node ID, generation and incarnation;
- source identity to the current validated peer card from the existing observer;
- pairwise authentication to the file-backed credential lookup;
- work authority to the existing durable-control GitHub authority verifier;
- mutation to the existing locally admitted G6.10 rendezvous service;
- replay state to a fresh bounded in-memory cache.

The mutation ordering remains:

`strict/bounded decode -> source+destination authentication/fencing -> strict operation payload -> canonical authority reread -> atomic current-source re-fence -> replay admission -> idempotent local mutation -> minimized response`

Peer authentication does not establish task authority.

## Current-peer rule

The observer exposes a peer card for mutation only while the peer's configured
endpoint is in the currently `OBSERVED` state. A never-observed, fetch-failed,
identity-mismatched, card-rejected, or duplicate-bound endpoint is not admitted
as a current mutation peer. Envelope verification additionally rejects an
expired peer-card lease.

Because canonical-authority reread can involve network I/O, the handler does not
trust the source-card snapshot taken before that read. After authority succeeds,
it acquires the observer current-peer guard, re-reads and re-verifies source
generation/incarnation/lease and request freshness, then performs replay
admission and the local idempotent rendezvous mutation while that observer
binding remains stable. No network operation occurs while the observer guard is
held. A peer that advances, fails observation, or expires during authority
verification is rejected before replay or mutation.

## Boundary

This runtime composition does not authorize:

- enabling the endpoint on a deployed TrueNAS or OCI node;
- listener, firewall, reverse-proxy, Cloudflare, Headscale, or overlay changes;
- production key creation, distribution, rotation, or revocation;
- image publication/release;
- live federated dogfood;
- wake/notice;
- generic RPC or arbitrary capability invocation.

Those remain separate deployment/admission gates.
