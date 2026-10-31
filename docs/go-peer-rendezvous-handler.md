# G6.11c synthetic peer rendezvous handler

Authority: `SemperSupra/wow-sidecar-private#79`.

This slice composes peer authentication, canonical authority verification,
replay admission and the local rendezvous store behind an injectable HTTP
handler exercised by synthetic tests and optionally registered by the Go daemon.

Registration is **disabled by default**. Explicit environment opt-in and
credential/control prerequisites are required; source integration does not
authorize live peer mutation or deployment. See
[the runtime guide](go-peer-rendezvous-runtime.md).

Processing order is:

```text
bounded body
-> strict outer request
-> current local destination card + current peer-card lookup + pairwise credential
-> HMAC / source+destination generation/incarnation / freshness / payload digest
-> strict operation payload
-> canonical DLE authority reread
-> guarded current source+destination identity re-verification
-> replay admission
-> idempotent local rendezvous mutation
-> minimized response
```

Authority failure occurs before replay admission so an unavailable canonical
authority source does not consume a request identity or mutate local rendezvous
state.

Exact duplicate signed requests are classified as duplicates but still flow to
the already-qualified idempotent rendezvous operation. Reuse of the same replay
identity with a different signed message fails closed.

This slice admits only remote `claim` and `complete`. Offer/notice/wake,
generic capability invocation and durable peer inboxes remain out of scope.

The handler fetches current destination identity on each request and requires a
`GuardCurrentIdentity` callback after the canonical authority reread. That
callback must keep both destination and peer identities valid through replay
admission and mutation. The current runtime assigns its generation and
incarnation when constructed; its observer guard serializes the source peer.
Replacement-process overlap and cross-process destination fencing require
separate qualification before operational activation.

The authority record, revision and state are verified through the canonical
DLE authority reader but are not covered by the peer-envelope HMAC. The
signed-authority binding threat model and negative tests are an unresolved
activation gate, not a guarantee inferred from a successful synthetic run.

Daemon registration and file-backed credential wiring exist only behind
explicit opt-in. Live peer activation, production keys, remote-network changes,
image publication, host deployments and Agent Dispatch operational promotion
remain subject to separate owner admission. Independent security validation
#78 covered the underlying G6.11a primitive, not these live boundaries.
