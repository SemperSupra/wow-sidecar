# Exact-head TLC model qualification — 2026-10-31

PR #89 candidate `413c177c190e36158138c2d8480e3d91b9df553d`
passed public GitHub Actions workflow
[41238216547](https://github.com/SemperSupra/wow-sidecar/actions/runs/41238216547),
job `tlc` **124646374502**.

Evidence independently retrieved from hosted job log:

- Checkout HEAD: `413c177c190e36158138c2d8480e3d91b9df553d`.
- GitHub release asset `tlaplus/tlaplus/v1.8.0/tla2tools.jar` checksum
  verified before execution:
  `sha256:011267fca25d0752cf18894dd585cd28b96960c112ea8a20b823ea770e7e10cd`.
- TLC: `TLC2 Version 2026.10.30.154139 (rev 090e85c)`.
- The configured `PeerFence.cfg` finite model was parsed and checked.
- TLC output: **"Model checking completed. No error has been found."**
- **29 states generated, 8 distinct states, 0 states left on queue.**
- Checked `TypeOK`, `StaleNeverMutates`, `AcceptedIsCurrent`.
- Release/publish/deployment: not attempted. Private hosted minutes: zero.

**Scientific scope:** This is a successful abstract state-space check only.
The model assumes a correct current-generation source of truth and canonical
authority admission. It does not prove the Go locking implementation, process
shutdown/drain, distributed filesystem coherence, signed authority fields,
lease expiry, or zero replay consumption under a real old-generation peer
mutation. The real Go candidate requires independent exact-head race/negative
tests, review, and approved merge; operational activation remains held.

This evidence is retained with public-safe model source. The private product
DLE owns owner decisions, release acceptance and target-specific evidence.
