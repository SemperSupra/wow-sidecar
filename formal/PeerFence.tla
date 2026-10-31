----------------------------- MODULE PeerFence -----------------------------
EXTENDS Naturals

\* Bounded abstract reference model: successful transport authentication and
\* exact canonical authority verification are assumed by AcceptCurrent.
\* This spec does not model a process-wide lease, network, time or credentials.
CONSTANT MaxGeneration
VARIABLES liveGeneration, requestGeneration, replayConsumed, mutationPerformed, decision

vars == <<liveGeneration, requestGeneration, replayConsumed,
          mutationPerformed, decision>>

Init ==
    /\ liveGeneration = 1
    /\ requestGeneration = 1
    /\ replayConsumed = FALSE
    /\ mutationPerformed = FALSE
    /\ decision = "NONE"

\* The old request remains generation 1 after a new embodiment is allocated.
AdvanceGeneration ==
    /\ liveGeneration < MaxGeneration
    /\ liveGeneration' = liveGeneration + 1
    /\ UNCHANGED requestGeneration
    /\ replayConsumed' = FALSE
    /\ mutationPerformed' = FALSE
    /\ decision' = "NONE"

\* Each new request starts with no previous replay/mutation outcome.
SetRequestGeneration ==
    /\ requestGeneration' \in 1..MaxGeneration
    /\ UNCHANGED liveGeneration
    /\ replayConsumed' = FALSE
    /\ mutationPerformed' = FALSE
    /\ decision' = "NONE"

RejectStale ==
    /\ requestGeneration # liveGeneration
    /\ UNCHANGED <<liveGeneration, requestGeneration>>
    /\ replayConsumed' = FALSE
    /\ mutationPerformed' = FALSE
    /\ decision' = "REJECTED_STALE"

AcceptCurrent ==
    /\ requestGeneration = liveGeneration
    /\ UNCHANGED <<liveGeneration, requestGeneration>>
    /\ replayConsumed' = TRUE
    /\ mutationPerformed' = TRUE
    /\ decision' = "ACCEPTED"

Next ==
    \/ AdvanceGeneration
    \/ SetRequestGeneration
    \/ RejectStale
    \/ AcceptCurrent

Spec == Init /\ [][Next]_vars

TypeOK ==
    /\ liveGeneration \in 1..MaxGeneration
    /\ requestGeneration \in 1..MaxGeneration
    /\ replayConsumed \in BOOLEAN
    /\ mutationPerformed \in BOOLEAN
    /\ decision \in {"NONE", "REJECTED_STALE", "ACCEPTED"}

StaleNeverMutates ==
    decision = "REJECTED_STALE" => ~mutationPerformed /\ ~replayConsumed

AcceptedIsCurrent ==
    decision = "ACCEPTED" => requestGeneration = liveGeneration
=============================================================================
