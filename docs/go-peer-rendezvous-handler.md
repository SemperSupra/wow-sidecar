# G6.11c synthetic peer rendezvous handler

Authority: `SemperSupra/wow-sidecar-private#79`.

This slice composes peer authentication, canonical authority verification,
replay admission and the local rendezvous store behind an injectable HTTP
handler used only by synthetic tests.

The handler is deliberately **not registered** in the WOW daemon.

Processing order is:

```text
bounded body
-> strict outer request
-> current source peer-card lookup + pairwise credential
-> current local destination-card binding
-> HMAC / source+destination generation/incarnation / freshness / payload digest
-> strict operation payload
-> canonical DLE authority reread
-> replay admission
-> idempotent local rendezvous mutation
-> minimized response
```

Authority failure occurs before replay admission so an unavailable canonical
authority source does not consume a request identity or mutate local rendezvous
state.

Exact duplicate signed requests are classified as duplicates but still flow to
the already-qualified idempotent rendezvous operation. A destination restart or
re-embodiment changes the current destination generation/incarnation and
invalidates pre-restart signed requests before authority lookup or mutation. Reuse of the same replay
identity with a different signed message fails closed.

This slice admits only remote `claim` and `complete`. Offer/notice/wake,
generic capability invocation and durable peer inboxes remain out of scope.

Registration in `Runtime.Handler`, environment credential wiring, a sender
client, and all live networking/deployment remain blocked until independent
security validation #78 passes and #72 explicitly releases the next boundary.
