# G6.11c synthetic peer rendezvous handler

Qualification projection for the bounded G6.11c handler primitive.

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
-> atomic current-source re-fence under observer guard
-> replay admission
-> idempotent local rendezvous mutation
-> minimized response
```

Authority failure occurs before replay admission so an unavailable canonical
authority source does not consume a request identity or mutate local rendezvous
state. Because authority reread can take time, the source peer is re-read after
authority succeeds. Final source verification, replay admission, and local
mutation execute under the observer current-peer guard; the authority network
read remains outside that guard.

Exact duplicate signed requests are classified as duplicates but still flow to
the already-qualified idempotent rendezvous operation. A destination restart or
re-embodiment changes the current destination generation/incarnation and
invalidates pre-restart signed requests before authority lookup or mutation. Reuse of the same replay
identity with a different signed message fails closed.

This slice admits only remote `claim` and `complete`. Offer/notice/wake,
generic capability invocation and durable peer inboxes remain out of scope.

This handler began as a synthetic-only primitive. Runtime registration is a
separate fail-closed composition step; live networking and deployment remain
separate qualification boundaries.
