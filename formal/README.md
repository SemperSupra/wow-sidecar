# Peer-generation fence reference model (G6)

Authority: private product DLE #26, owner-approved G2/G6.

This deliberately small **abstract** TLA+ model covers exactly one invariant:
a stale generation must be rejected **before replay admission or mutation**.
It checks two generations and any request generation in that finite domain.
An accepted request must match the current generation. It is not a proof
that the current Go runtime implements the modeled fencing, that the runtime
has an exclusive generation lease, or that peer activation is safe.

## Execute and retain evidence

The companion public pull-request workflow downloads the *official* TLA+
`tlaplus/tlaplus` v1.8.0 **prerelease** tool JAR by its immutable version URL
and validates the GH-published artifact SHA-256
`011267fca25d0752cf18894dd585cd28b96960c112ea8a20b823ea770e7e10cd`.
Do not substitute a rolling URL or unverified JAR.

With that JAR and Java >= 11:

```sh
java -cp tla2tools.jar tla2sany.SANY formal/PeerFence.tla
java -XX:+UseParallelGC -jar tla2tools.jar -config formal/PeerFence.cfg formal/PeerFence.tla
```

The workflow must show TLC's *model checking completed; no error* outcome.
Until a successful exact-head run is read back, label the model **UNEXECUTED**.
The full stdout, stderr, exact tool SHA, candidate commit and job result are
part of the evidence. A failure is preserved without changing the model
merely to get a green check.

## Code/trace mapping (not equivalence)

| Model action / property | Go source under review | Executable evidence |
|---|---|---|
| `AdvanceGeneration` | `go/node/generation_linux.go:allocateGeneration` | `generation_linux_test.go`; `generation_cross_process_linux_test.go` |
| `SetRequestGeneration` | `go/federation` envelope generation binding | `peer_rendezvous_test.go`, current source verification |
| `RejectStale` | `go/node/peer_rendezvous.go:ServeHTTP` and `GuardCurrentIdentity` | existing source/peer tests, missing cross-process *mutation* negative |
| `StaleNeverMutates` | `Replay.Admit` followed by `Rendezvous.invoke` | NEW needed: old process after generation 2, same request ID, zero replay and zero mutation |
| `AcceptedIsCurrent` | `VerifyPeerEnvelope` source/destination checks | G6.11 handler tests; **not** a globally enforced lifetime lock |

**Open refinement gaps:** no process-wide exclusive lease or shutdown/drain
model; no authority HMAC coverage; no distributed clocks, loss, partition,
credential lifecycle, network endpoint, dual-writer workset, or persistent
replay. Implement and test those separately before changing operational
peer-activation holds.

No credentials, private HIL, host addresses, or deployment permissions belong
in this public model.
