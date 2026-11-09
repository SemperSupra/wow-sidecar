-------------------------- MODULE TwoClaimantRecovery --------------------------
EXTENDS Naturals

\* Synthetic one-request safety model. Two distinct contenders share one
\* durable create-once claim. Local acknowledgments disappear on crash;
\* durable owner, quarantine, effect and receipt survive restart.
\* This is NOT an implementation refinement or a live provider qualification.
CONSTANTS ActorA, ActorB
ASSUME ActorA # ActorB
Actors == {ActorA, ActorB}

VARIABLES claimOwner, phase, acknowledged, alive, localClaim,
          effectCount, receipt, quarantine, decision

vars == <<claimOwner, phase, acknowledged, alive, localClaim,
          effectCount, receipt, quarantine, decision>>

Init ==
    /\ claimOwner = "NONE"
    /\ phase = "UNCLAIMED"
    /\ acknowledged = {}
    /\ alive = Actors
    /\ localClaim = {}
    /\ effectCount = 0
    /\ receipt = FALSE
    /\ quarantine = FALSE
    /\ decision = "NONE"

\* An acknowledged atomic claim can be used by its current process only.
Claim(a) ==
    /\ a \in alive
    /\ claimOwner = "NONE"
    /\ claimOwner' = a
    /\ phase' = "CLAIMED"
    /\ acknowledged' = acknowledged \cup {a}
    /\ localClaim' = localClaim \cup {a}
    /\ decision' = "CLAIM_ACKED"
    /\ UNCHANGED <<alive, effectCount, receipt, quarantine>>

\* Provider committed the claim but the response was lost. Readback may
\* establish persistence, never caller ownership or effect permission.
LostClaimAck(a) ==
    /\ a \in alive
    /\ claimOwner = "NONE"
    /\ claimOwner' = a
    /\ phase' = "CLAIMED"
    /\ quarantine' = TRUE
    /\ decision' = "CLAIM_UNKNOWN"
    /\ UNCHANGED <<acknowledged, alive, localClaim, effectCount, receipt>>

\* A consequential effect is permitted only from the original acknowledged
\* claimant, before quarantine. The effect immediately enters durable UNKNOWN
\* until its receipt is recorded; no second execution is allowed.
Effect(a) ==
    /\ a \in alive
    /\ claimOwner = a
    /\ a \in acknowledged
    /\ a \in localClaim
    /\ phase = "CLAIMED"
    /\ ~quarantine
    /\ phase' = "EFFECTED"
    /\ effectCount' = effectCount + 1
    /\ quarantine' = TRUE
    /\ decision' = "EFFECTED"
    /\ UNCHANGED <<claimOwner, acknowledged, alive, localClaim, receipt>>

RecordReceipt(a) ==
    /\ a \in alive
    /\ claimOwner = a
    /\ a \in localClaim
    /\ phase = "EFFECTED"
    /\ phase' = "RECEIPTED"
    /\ receipt' = TRUE
    /\ quarantine' = FALSE
    /\ decision' = "RECEIPTED"
    /\ UNCHANGED <<claimOwner, acknowledged, alive, localClaim, effectCount>>

\* Process-local knowledge is lost. All durable state is unchanged.
Crash(a) ==
    /\ a \in alive
    /\ alive' = alive \ {a}
    /\ acknowledged' = acknowledged \ {a}
    /\ localClaim' = localClaim \ {a}
    /\ decision' = "CRASH"
    /\ UNCHANGED <<claimOwner, phase, effectCount, receipt, quarantine>>

Restart(a) ==
    /\ a \notin alive
    /\ alive' = alive \cup {a}
    /\ decision' = "RESTART"
    /\ UNCHANGED <<claimOwner, phase, acknowledged, localClaim,
                  effectCount, receipt, quarantine>>

\* A different process may observe, but observation does not confer a claim.
Observe(a) ==
    /\ a \in alive
    /\ claimOwner # "NONE"
    /\ decision' = "OBSERVED"
    /\ UNCHANGED <<claimOwner, phase, acknowledged, alive, localClaim,
                  effectCount, receipt, quarantine>>

DenyDuplicate(a) ==
    /\ a \in alive
    /\ claimOwner # "NONE"
    /\ decision' = "DENIED"
    /\ UNCHANGED <<claimOwner, phase, acknowledged, alive, localClaim,
                  effectCount, receipt, quarantine>>

Next ==
    \E a \in Actors:
        Claim(a) \/ LostClaimAck(a) \/ Effect(a) \/ RecordReceipt(a)
        \/ Crash(a) \/ Restart(a) \/ Observe(a) \/ DenyDuplicate(a)

Spec == Init /\ [][Next]_vars

TypeOK ==
    /\ claimOwner \in Actors \cup {"NONE"}
    /\ phase \in {"UNCLAIMED", "CLAIMED", "EFFECTED", "RECEIPTED"}
    /\ acknowledged \subseteq Actors
    /\ alive \subseteq Actors
    /\ localClaim \subseteq Actors
    /\ effectCount \in 0..2
    /\ receipt \in BOOLEAN
    /\ quarantine \in BOOLEAN
    /\ decision \in {"NONE", "CLAIM_ACKED", "CLAIM_UNKNOWN", "EFFECTED",
                     "RECEIPTED", "CRASH", "RESTART", "OBSERVED", "DENIED"}

AtMostOneEffect == effectCount <= 1
EffectHasOwner == effectCount > 0 => claimOwner \in Actors
LocalClaimBound == localClaim \subseteq acknowledged
AcknowledgmentBound == acknowledged \subseteq {claimOwner}
ReceiptRequiresEffect == receipt => (effectCount = 1 /\ phase = "RECEIPTED")
EffectWithoutReceiptQuarantined == phase = "EFFECTED" => quarantine
LostClaimAckQuarantined == decision = "CLAIM_UNKNOWN" => quarantine
=============================================================================
