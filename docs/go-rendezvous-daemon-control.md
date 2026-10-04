# Go rendezvous daemon control

Authority: `SemperSupra/wow-sidecar-private#66`.

This slice composes the existing rendezvous handoff state machine into the Go daemon through the already-qualified durable-control -> typed-capability path.

## Admission

Rendezvous is optional. It is admitted only when all three local values are present and normalized:

- `WOW_RENDEZVOUS_WORKSET_REF`
- `WOW_RENDEZVOUS_DELEGATION_ID`
- `WOW_RENDEZVOUS_LEASE_SECONDS` (5-3600)

The configured workset and delegation are product-local admission state. A durable request cannot replace either value.

When admitted, the runtime registers `rendezvous.handoff` as:

- actuator;
- bounded mutation;
- authenticated disclosure;
- authority required.

The minimal embodiment card may name the capability ID, but the unauthenticated capability index does not disclose its descriptor.

## Operations

The capability accepts exactly one of:

- `offer` with an optional bounded note;
- `claim` with `handoff_id` and `launch_id`;
- `complete` with `handoff_id`, `actor_instance_id`, terminal status, and optional bounded result reference/note.

Unknown fields fail closed. There is no input field for workset, delegation, capability, authority verifier, code, command, path, transport, or target.

Each invocation derives a request-local rendezvous principal from the already-verified durable-control authority and passes that principal through the request context to the #65-qualified rendezvous authorization seam. No ambient mutable principal exists.

## Receipts

Capability data is bounded to routing/result metadata such as:

- receipt status;
- handoff ID/status;
- actor instance ID and lease expiry when claimed;
- completion timestamp when terminal.

It intentionally omits workset/delegation values, offered-by principal, notes, raw authority bodies, tokens, and provider responses.

Idempotent `existing`, `existing-claim`, and `already-completed` outcomes report no new mutation. Ambiguous actuator failure remains `UNKNOWN` with retry unauthorized.

## Boundary

This does not add a generic HTTP POST/invoke endpoint and does not claim cross-node E3 complete. Peer notice/claim transport remains a later bounded slice after this local typed composition passes its exact public qualification.
