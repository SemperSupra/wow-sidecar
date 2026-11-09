# Two-claimant durable UNKNOWN safety rep (synthetic)

This source-free, public-safe **abstract** model explores two competing
claimants for one exact immutable request, a provider claim committed before
its acknowledgment is lost, observation from another process, an effect
before receipt, process crash/restart, and durable UNKNOWN quarantine.

The safety model permits an effect only for an acknowledged, process-local
winning claimant. A committed claim whose acknowledgment was lost is
quarantined and **readback does not confer ownership**. An effect enters
durable UNKNOWN until a receipt is written. Crash discards local knowledge,
not the durable owner, quarantine, receipt, or effect counter.

The unsafe model deliberately ignores quarantine after an effect, crash and
restart. TLC must produce an **AtMostOneEffect** counterexample. A normal
successful TLC run plus the targeted negative control are required.

Run using the existing workflow's SHA-256-verified TLA+ v1.8.0 JAR:

~~~sh
java -cp tla2tools.jar tla2sany.SANY formal/TwoClaimantRecovery.tla
java -XX:+UseParallelGC -jar tla2tools.jar -workers 1 \
  -config formal/TwoClaimantRecovery.cfg formal/TwoClaimantRecovery.tla
java -XX:+UseParallelGC -jar tla2tools.jar -workers 1 \
  -config formal/TwoClaimantRecoveryUnsafe.cfg formal/TwoClaimantRecoveryUnsafe.tla
~~~

**Scope limits:** finite state/safety only; no temporal fairness or liveness,
real GitHub CAS, real HTTP retries, provider authentication, independently
delivered wake, native Go/Python refinement, physical crash durability,
two-writer file locking/fsync, host apply, owner acceptance or successor
readiness. In particular, the modeled effect counter and quarantine are
idealized durable state, not evidence that a real runtime persists them.

This is an independent F1 characterization candidate, not a release gate
clearance or production authorization. Keep source-to-code/test mapping and
owner decisions in their project-native private DLE. No private source,
credentials, user data or protected host facts are included.
