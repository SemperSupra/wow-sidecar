# Lifecycle and migration contract

WOW Sidecar core defines lifecycle **planning and recovery evidence**, not platform installation mechanics.

The generic contract is:

`observe/discover -> plan -> apply -> verify`

The core currently implements the first two stages plus secret-safe recovery-copy verification. Platform adapters own the actual `apply` and platform-specific `verify` operations.

## Invariants

- First install and upgrade are distinct observed states.
- An unexpected pre-existing target is never overwritten; it requires explicit `import-existing`.
- Upgrade, repair, import-existing and rollback require verified recovery material before mutation.
- Migration is copy-first and retains original source state through cutover.
- Failed first install may remove only the newly created managed target.
- Failed upgrade/repair must restore the exact prior managed revision.
- Uninstall preserves data by default; destructive data removal must be explicit.
- Recovery receipts contain opaque logical names, sizes and SHA-256 digests, never secret/key/config values.
- The generic lifecycle plan contains no GARM, Agent Dispatch, TrueNAS, `/opt`, or systemd policy.

## Platform ownership

Linux packaging/service integration and TrueNAS App packaging are consumers of this contract. They must not expand core authority or mutate durable request/claim/receipt state.
