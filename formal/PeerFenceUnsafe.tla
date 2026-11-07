--------------------------- MODULE PeerFenceUnsafe ---------------------------
EXTENDS PeerFence

\* Intentional adversarial mutation: accepts a stale request as rejected but
\* incorrectly spends replay identity and mutates durable state. TLC MUST
\* find a counterexample to StaleNeverMutates for this *unsafe* transition.
BadAcceptStale ==
    /\ requestGeneration # liveGeneration
    /\ UNCHANGED <<liveGeneration, requestGeneration>>
    /\ replayConsumed' = TRUE
    /\ mutationPerformed' = TRUE
    /\ decision' = "REJECTED_STALE"

UnsafeNext == Next \/ BadAcceptStale
UnsafeSpec == Init /\ [][UnsafeNext]_vars
=============================================================================
