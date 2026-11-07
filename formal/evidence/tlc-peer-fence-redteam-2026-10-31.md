# Adversarial model-sensitivity qualification — 2026-10-31

Source-only PR #89 exact head
`66b6661aaac9a487a6222f7f25cd4f3b865bef35`
qualified in [public run 41240905545](https://github.com/SemperSupra/wow-sidecar/actions/runs/41240905545),
TLC job `124654409796`. First-party job log independently inspected.

- SHA-checked upstream `tla2tools.jar`:
  `sha256:011267fca25d0752cf18894dd585cd28b96960c112ea8a20b823ea770e7e10cd`.
- Baseline `PeerFence.cfg`: **PASS**, TLC reports
  `Model checking completed. No error has been found.`;
  29 generated states, 8 distinct, 0 queued.
- Injected `PeerFenceUnsafe.tla` adds the deliberately incorrect
  `BadAcceptStale` transition, which labels a stale request rejected
  while setting replay-consumed and mutation-performed to TRUE.
- `PeerFenceUnsafe.cfg`: **EXPECTED COUNTEREXAMPLE observed**, TLC emits
  `Error: Invariant StaleNeverMutates is violated.`.
- The workflow explicitly requires the injected model's TLC process to
  return failure **for that named invariant**, then succeeds as a red-team
  falsification check. A clean unsafe model or an unrelated syntax/tool
  failure makes the workflow fail.
- Publication, provider claims, private hosted minutes, peer listener and
  host lifecycle mutation: **not performed**.

The model-checker's invariant has now shown sensitivity to one realistic
unsafe transition. This remains a **bounded abstract model**, with no
automatic proof of the Go implementation, OS-local inode stability,
network-file-system coordination, process cancellation or signed canonical
authority binding. The live source must still pass a full handler regression
and separate independent security review before any source promotion.
