------------------------ MODULE TwoClaimantRecoveryUnsafe ------------------------
EXTENDS TwoClaimantRecovery

\* Intentionally unsafe retry after an effect, lost receipt acknowledgment,
\* process crash and restart. The durable UNKNOWN fence is ignored.
\* TLC must find an AtMostOneEffect counterexample; this is a negative control.
UnsafeRetryAfterRestart(a) ==
    /\ a \in alive
    /\ claimOwner = a
    /\ phase = "EFFECTED"
    /\ a \notin localClaim
    /\ effectCount' = effectCount + 1
    /\ decision' = "EFFECTED"
    /\ UNCHANGED <<claimOwner, phase, acknowledged, alive, localClaim,
                  receipt, quarantine>>

UnsafeNext == Next \/ (\E a \in Actors : UnsafeRetryAfterRestart(a))
UnsafeSpec == Init /\ [][UnsafeNext]_vars
=============================================================================
