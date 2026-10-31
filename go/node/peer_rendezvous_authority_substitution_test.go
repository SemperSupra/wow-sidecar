package node

import (
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "testing"
    "time"

    "github.com/SemperSupra/wow-sidecar/go/rendezvous"
)

// TestPeerAuthoritySubstitutionFailsBeforeReplayOrMutation exercises the
// separate canonical-authorization barrier. The HMAC envelope does NOT cover
// the authority object, so untrusted peers may substitute its fields without
// invalidating the envelope signature. A changed record/revision must fail
// canonical authority verification and must not consume the replay identity.
//
// This is a fail-closed test of the *current* verifier, not a claim that the
// authority fields are HMAC-bound or that all cross-record substitutions are
// impossible under every externally supplied authority policy.
func TestPeerAuthoritySubstitutionFailsBeforeReplayOrMutation(t *testing.T) {
    cases := []struct {
        name string
        tamper func(*peerRendezvousRequest)
    }{
        {"record-substitution", func(w *peerRendezvousRequest) {
            w.Authority.Record = "github-issue-comment:SemperSupra/example#2:456"
        }},
        {"revision-substitution", func(w *peerRendezvousRequest) {
            w.Authority.Revision = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
        }},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            now := time.Date(2026, 10, 31, 19, 0, 0, 0, time.UTC)
            checks := 0
            verify := func(_ context.Context, record, revision, state string) error {
                checks++
                if record != peerTestAuthorityRef || revision != peerTestAuthorityRev || state != "open" {
                    return fmt.Errorf("canonical authority mismatch")
                }
                return nil
            }
            handler, service, handoffID, key := newPeerHandlerForTest(t, now, verify)
            payload := json.RawMessage(`{"operation":"claim","handoff_id":"` + handoffID + `","launch_id":"launch-authority-negative"}`)
            original := signedPeerWireRequest(t, now, key, "peerreq-auth-negative", "rendezvous.claim", payload)
            var changed peerRendezvousRequest
            if err := json.Unmarshal(original, &changed); err != nil {
                t.Fatal(err)
            }
            signature := changed.Envelope.Signature
            tc.tamper(&changed)
            if changed.Envelope.Signature != signature {
                t.Fatal("test mutated the HMAC signature")
            }
            tampered, err := json.Marshal(changed)
            if err != nil {
                t.Fatal(err)
            }
            rejected := servePeerRequest(handler, tampered)
            if rejected.Code != http.StatusServiceUnavailable {
                t.Fatalf("tampered authority status=%d body=%q", rejected.Code, rejected.Body.String())
            }
            ctx := context.WithValue(context.Background(), rendezvousPrincipalContextKey{}, rendezvous.Principal{
                ClientID: "durable-control",
                Subject: peerTestAuthorityRef + "@" + peerTestAuthorityRev,
            })
            state, err := service.store.Get(ctx, handoffID)
            if err != nil {
                t.Fatal(err)
            }
            if state.Status != "offered" || state.Claim != nil {
                t.Fatalf("tampered authority mutated state: %+v", state)
            }
            accepted := servePeerRequest(handler, original)
            if accepted.Code != http.StatusOK {
                t.Fatalf("valid original status=%d body=%q", accepted.Code, accepted.Body.String())
            }
            result := decodePeerResponse(t, accepted)
            if result.Duplicate {
                t.Fatalf("rejected request consumed replay identity: %+v", result)
            }
            if checks != 2 {
                t.Fatalf("expected fresh canonical checks for tampered and original; got %d", checks)
            }
        })
    }
}
