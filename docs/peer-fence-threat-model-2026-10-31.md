# Peer rendezvous replacement: threat model and acceptance matrix

Status: **source-only review plan, no peer activation**. Exact public Go
source checkpoint: `0acc094aebed1f160811fa6c538671219ce76a16`.
Draft corrective candidate: public PR #93 (OS-local generation-state lease),
not merged or admitted. Preserve current default-OFF peer mutation endpoint.

## Security boundaries

The following authorities must stay distinct:

1. A *node identity* and observed embodiment Card identify a source/destination.
2. A *pairwise key* authenticates the signed peer-envelope identity, exact
   operation, payload digest, timestamp and request ID.
3. Canonical work authority record/revision/state is independently re-read;
   it is not part of the current envelope's HMAC-covered fields.
4. The replay cache provides bounded duplicate prevention, but cannot grant
   current destination ownership or canonical work authority.
5. Runtime generation allocation currently locks the shared state file only
   during allocation. The proposed PR #93 holds an OS-local shared lock during
   a guarded operation; that lock is not distributed leadership.

The invariant required for a mutating peer handler is:

> Under current canonical authorization, a stale/superseded destination
> generation **must not admit replay** and **must not mutate rendezvous state**.
> At most one generation is admitted to an operation at a time on a shared
> local generation-state path.

A passed Go unit test for HTTP identity/readiness is not evidence for this
invariant. A model-checked abstract TLA+ invariant is not implementation proof.

## Adversarial test matrix

| Case | Falsifying stimulus | Required observation | Proof boundary |
|---|---|---|---|
| F1 | Old OS process remains up when second process allocates generation N+1 | Old process's guarded **mutation** rejects before replay; claim state unchanged | Two actual processes, shared exact state file |
| F2 | New allocation begins during in-flight old guarded mutation | New generation cannot be committed until old guarded mutation exits; bounded cancellation eventually releases | Scheduler/race and deliberately blocked callback |
| F3 | Old and new endpoints are independently reachable | Readiness may remain up; identity alone must never be interpreted as active mutation authority | Reachability distinct from authority |
| F4 | State file missing, unreadable, corrupt or node ID mismatched | Fail closed, no callback/replay/mutation | Negative storage fixtures |
| F5 | Lock path replaced, symlinked, wrong permissions or shared storage lacks required flock semantics | Reject or document unsupported substrate, never silently assert lifetime fencing | Target-native adversarial HIL; can require state-directory policy |
| F6 | Old process exits in middle of operation; replacement starts | No duplicate claim or blind retry; UNKNOWN preserved until durable reconciliation | Crash/recovery harness and durable claim/receipt evidence |
| F7 | Source peer Card changes during canonical re-read | Source and destination are revalidated under guard; no consumed replay on rejection | Existing source-observer guard plus race fixture |
| F8 | Authority record/revision/state altered while HMAC signature unchanged | Strict canonical verifier rejects before replay/mutation; no permission from HMAC alone | Synthetic #88 negative plus external-verifier policy |
| F9 | Canonical authority changes immediately after successful check | Fresh read/authority versioning and transactional mutation boundary defined, otherwise capability must stay disabled | End-to-end authority race and acceptance |
| F10 | Different state paths, container mount namespaces, two hosts or filesystems with different lock semantics | No claim of global fencing; explicit shared-state prerequisites or independent distributed lease/quorum | Target-native acceptance, not single-runner CI |
| F11 | Concurrent replay, timestamp skew, key rotation/revocation, reordered requests | Exactly bounded replay behavior and no duplicate consequential mutation | Signed-envelope and durable-state scenarios |
| F12 | Cancellation while source observer mutex and generation flock are held | No deadlock, starvation or unbounded lock retention on shutdown | Race/deadlock and bounded duration tests |

## Red-team objections to PR #93

- It fences only operations that pass through its exact guarded callback.
  Any alternate actuator/control path must have its own authority boundary.
- `Flock` on `<state>.lock` is a **local** serialization primitive.
  Independent hosts with nonshared state, unsuitable network filesystems or
  re-created lock inode paths are outside the claim.
- Holding a shared lock over a slow/blocking mutation can starve replacement.
  Cancellation/timeout must be bounded and observable; do not solve this by
  releasing the lock before the consequential operation.
- `CurrentDestination` is consulted before canonical authorization, then
  again under the guard; the second check must remain the decisive one.
- The current HMAC doesn't bind the authority object. A verifier policy that
  accepts multiple authority records must still enforce the exact operation
  and local rendezvous binding; otherwise two different valid authorities may
  authorize a single signed envelope. Consider a versioned signed-intent
  schema rather than silently altering v1 interoperability.
- Any process that can rewrite the generation-state directory or lock file
  may subvert local fencing. Treat filesystem ownership and state durability
  as deployment preconditions.

## Blue-team minimal release sequence

1. Preserve old-process/readiness regression and PR #93's synthetic guarded
   callbacks as repeatable first-failure baselines. Independently verify exact
   Go, race and native hosted CI; document what each test **does not** prove.
2. Add **full handler** cross-process stale request with ephemeral pairwise
   test key and isolated rendezvous store, checking replay non-consumption
   and zero state change. Do not run against a real peer or target.
3. Validate state-directory ownership, lock inode stability, cancellation and
   signal/shutdown behavior with failure injection.
4. Bind authority intent unambiguously (either versioned envelope fields or
   a narrow canonical verifier that includes source/destination/operation/
   local binding) with backward-compatible negotiation and negative tests.
5. Verify independent reviewer evidence and formal-model-to-code mapping.
   Record each assumption and residual UNKNOWN in product authority.
6. Only after separate release and target acceptance consider a published
   candidate, read-only HIL, private target-native mutation rehearsal and
   separately authorized peer activation/cutover.

## Existing primitives to reuse

Prefer Linux `flock(2)` or a documented target-native lifecycle lock for
same-host state; Go's standard-library `context`, atomic file updates and
`net/http` for bounded controls; the existing versioned peer envelope,
generation/Card and replay components; formal TLC for abstract invariants;
Go tests/race and public GitHub Actions for executable regression evidence.
A bespoke distributed lease, general message bus or second work authority is
not warranted without a concrete failed target-native test.

**Gate:** Until full F1–F12 qualifications and independent acceptance, any
mutable peer transport remains default-OFF. No merge, image repin, registry
publication, production credentials, live host mutations or operational
Agent Dispatch claims are implied by this document.
