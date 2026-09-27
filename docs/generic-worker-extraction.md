# Generic worker extraction

The historical Agent Dispatch host-control worker mixed four concerns:

1. generic request/claim/receipt worker mechanics;
2. credential acquisition and checkout identity;
3. site/runtime configuration;
4. admitted operator implementations, including GARM.

This extraction moves only the generic worker mechanics into WOW Sidecar core.

## Core invariants

- the worker receives an explicit control repository;
- the worker receives an explicit operator registry;
- every registered operator profile must have exactly one canonical authority binding;
- registry and authority-binding profile sets must be identical;
- record/state are always exact-bound and an optional fixed revision may further pin authority;
- canonical authority is reread before a handler executes;
- at most one pending request is processed per cycle;
- cycle failures emit sanitized UNKNOWN evidence and never authorize retry;
- polling is optional runtime behavior, not part of the request/claim/receipt protocol.

Credential loading, local checkout installation, service lifecycle and operator implementations remain outside the generic worker.

GARM will be implemented as a separate integration profile, not as worker-core policy.
