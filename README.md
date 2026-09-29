# WOW Sidecar

WOW Sidecar is a trusted-host actuator for bounded, durable automation.

It consumes explicitly authorized requests, verifies canonical authority, executes only registered operator profiles, and emits immutable claim/receipt evidence. It is designed to let controllers such as Agent Dispatch safely use capabilities available on trusted local or remote hosts without exposing a generic remote shell.

## Product boundary

WOW Sidecar is an independent product. It is not:

- a scheduler;
- a general workflow engine;
- a GARM controller or provider;
- a TrueNAS App materializer;
- an Agent Dispatch internal daemon.

Agent Dispatch is one controller/client of WOW Sidecar. GARM/TrueNAS is one integration domain.

## Core invariants

- request publication is commit-before-ref;
- claim is create-once and precedes consequential execution;
- claim-without-receipt is UNKNOWN and is never blindly replayed;
- canonical authority is reread before an admitted operator executes;
- arbitrary command/shell input is not an operator interface;
- operator authority is profile-specific and explicitly bounded;
- receipts are sanitized and never contain secrets;
- credentials remain local to the trusted host;
- deployment and execution are separate transitions;
- adapters do not silently become authorities for systems they operate.

## Repository model

- `SemperSupra/wow-sidecar-private` — private development, adversarial testing, qualification, failure knowledge, release-candidate preparation, and site-sensitive evidence.
- `SemperSupra/wow-sidecar` — public source/release authority produced through reviewed constructive projection of qualified candidates.

The historical implementation currently remains under `SemperSupra/agent-dispatch-private/prototype/sidecar` while migration is qualified. It must not be deleted or treated as superseded until the new product reproduces its admitted behavior and tests.

## Integrations

Integrations are thin bounded adapters over the generic trusted-host substrate.

Examples:

- Agent Dispatch request/claim/receipt transport;
- GARM/TrueNAS qualification and operations profiles;
- future infrastructure-control profiles.

Domain-specific scheduling, application lifecycle policy, or product authority remains in the owning domain.

## Status

Generic extraction and public-safe non-live qualification are complete through the protocol/runtime, bounded integrations, immutable App-native artifact, and public TrueNAS App render. The historical Agent Dispatch implementation remains the admitted live compatibility bridge while private TrueNAS HIL and state-preserving cutover/rollback remain pending.

No live installation, migration, cutover, or private-HIL claim should be inferred from the public render/artifact evidence alone.
