# Worker service entrypoint

The deployable WOW Sidecar worker entrypoint is:

`wow-sidecar-worker`

It is intentionally thin. It:

1. loads one or more declarative operator-profile JSON files;
2. composes the exact registry + authority bindings;
3. initializes the GitHub App client and canonical authority reader;
4. derives `operator_revision` from a clean exact Git checkout;
5. runs at most one request with `--once` or the bounded polling loop with `--serve`.

The entrypoint does not contain GARM, Agent Dispatch, TrueNAS, scheduling, workset, Jules, or provider policy.

## Deployment identity

For the current compatibility generation, the installed runtime must execute from a clean Git checkout and the request's `operator_revision` must equal that checkout's exact HEAD. A future immutable package/image identity may replace this only after it is separately specified and qualified; the installer must not silently substitute a self-asserted version string.

This keeps Linux deployment compatible with the already-qualified request/claim/receipt revision contract while TrueNAS image identity is worked as a separate packaging integration.
