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
-> current local destination card + current peer-card lookup + pairwise credential
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
the already-qualified idempotent rendezvous operation. Reuse of the same replay
identity with a different signed message fails closed.

This slice admits only remote `claim` and `complete`. Offer/notice/wake,
generic capability invocation and durable peer inboxes remain out of scope.

The handler takes the current local destination card as an injected dependency
on every request so receiver restart/re-embodiment fencing cannot be satisfied
from stale construction-time identity.

Registration in `Runtime.Handler`, environment credential wiring, a sender
client, and all live networking/deployment remain blocked until #72 explicitly
releases the next boundary. Independent security validation #78 has passed the
underlying G6.11a primitive.
