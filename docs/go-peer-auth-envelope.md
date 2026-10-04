# G6.11 peer authentication envelope

Authority: `SemperSupra/wow-sidecar-private#72`.

This increment adds a pure Go authentication/fencing primitive for future
cross-node rendezvous operations. It deliberately does **not** add a network
mutation endpoint.

## Security model

Each peer relationship is expected to use a unique pairwise secret supplied by
a later file-backed credential adapter. The secret itself is never carried in an
embodiment card, request, receipt, environment value, or DLE record.

The signed envelope binds:

- source node ID;
- source generation;
- source incarnation;
- destination node ID;
- request ID;
- issue time;
- exact operation;
- SHA-256 digest of the exact payload.

The receiver separately compares the source identity/generation/incarnation to
the currently observed peer card. A valid transport signature therefore does
not let an old embodiment generation continue to act.

## Operations

This first primitive admits only:

- `rendezvous.claim`;
- `rendezvous.complete`.

It intentionally does not admit `offer`, `notice`, arbitrary capability
invocation, command execution, or generic POST forwarding.

The current design assumes the handoff reference is already recoverable from
project-native DLE. Wake/notification remains a later concern rather than
creating a second durable inbox inside WOW.

## Replay/freshness

The verifier requires:

- a live current peer card;
- exact destination identity;
- caller-selected bounded clock skew;
- exact payload digest;
- HMAC-SHA256 signature.

A bounded replay cache rejects a repeated request ID until its TTL expires.
At-least-once transport can therefore retry only by reconciling the original
result or by creating a new request identity after the owning operation permits
it.

## Boundary

Transport authentication proves only that the message came from the peer
holding the pairwise key and from the currently observed node generation. It
does **not** establish work/task authority.

Any future peer rendezvous endpoint must still re-read and verify the canonical
DLE authority before mutating the local rendezvous store.
