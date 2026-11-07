//go:build linux

package node

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "path/filepath"
    "strings"
    "testing"
    "time"

    "github.com/SemperSupra/wow-sidecar/go/embodiment"
    "github.com/SemperSupra/wow-sidecar/go/federation"
    "github.com/SemperSupra/wow-sidecar/go/rendezvous"
)

// A signed, correctly formed peer mutation must be rejected by the old
// handler after a different runtime allocates the newer generation. This
// exercises the REAL handler's replay/store path rather than only a callback.
// Both runtime objects share the same OS-local generation state directory;
// cross-host or independent-process handler operation is NOT claimed here.
func TestOldRuntimeSignedPeerMutationRejectsBeforeReplayAndStore(t *testing.T) {
    now := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
    clock := func() time.Time { return now }
    key := []byte("0123456789abcdef0123456789abcdef")
    sourceCard := peerHandlerCard(now)
    source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/v1/card" { http.NotFound(w, r); return }
        _ = json.NewEncoder(w).Encode(sourceCard)
    }))
    defer source.Close()

    cfg := Config{
        NodeID: "oci-edge-node",
        Locality: "cloud",
        GenerationStateFile: filepath.Join(t.TempDir(), "generation.json"),
        ListenAddr: "127.0.0.1:0",
        LeaseTTL: time.Minute,
        PeerURLs: []string{source.URL},
        PeerPollInterval: 15 * time.Second,
        Rendezvous: &RendezvousConfig{
            WorksetRef: "github-file:SemperSupra/example@main:workset.json",
            DelegationID: "peer-fencing-proof",
            Lease: time.Minute,
        },
    }
    old, err := newRuntime(cfg, strings.Repeat("a", 40), "peer-fence-test", clock)
    if err != nil { t.Fatal(err) }
    old.peers.PollOnce(context.Background())
    if _, ok := old.peers.CurrentCard(sourceCard.NodeID); !ok {
        t.Fatal("source peer was not observed")
    }
    credPath := writePeerRuntimeCredentials(t, sourceCard.NodeID, key)
    credentials, err := federation.LoadPeerCredentials(credPath)
    if err != nil { t.Fatal(err) }
    verifyCalls := 0
    if err := old.configurePeerRendezvous(
        &PeerRendezvousRuntimeConfig{
            CredentialsFile: credPath, MaxSkew: 2 * time.Minute, ReplayTTL: 5 * time.Minute,
        },
        credentials,
        func(_ context.Context, record, revision, state string) error {
            verifyCalls++
            if record != peerTestAuthorityRef || revision != peerTestAuthorityRev || state != "open" {
                return context.Canceled
            }
            return nil
        },
    ); err != nil { t.Fatal(err) }
    ctx := context.WithValue(context.Background(), rendezvousPrincipalContextKey{}, rendezvous.Principal{
        ClientID: "durable-control",
        Subject: peerTestAuthorityRef + "@" + peerTestAuthorityRev,
    })
    offered, err := old.rendezvous.store.Offer(
        ctx, cfg.Rendezvous.WorksetRef, cfg.Rendezvous.DelegationID, "",
    )
    if err != nil { t.Fatal(err) }
    oldCard, err := old.Card()
    if err != nil { t.Fatal(err) }
    payload := json.RawMessage(`{"operation":"claim","handoff_id":"` + offered.Handoff.ID + `","launch_id":"launch-stale-old"}`)
    envelope, err := federation.SignPeerEnvelope(federation.PeerEnvelope{
        Schema: federation.PeerEnvelopeSchema,
        SourceNodeID: sourceCard.NodeID,
        SourceGeneration: sourceCard.Generation,
        SourceIncarnationID: sourceCard.IncarnationID,
        DestinationNodeID: oldCard.NodeID,
        DestinationGeneration: oldCard.Generation,
        DestinationIncarnationID: oldCard.IncarnationID,
        RequestID: "peerreq-stale-signed-01",
        IssuedAt: now.Format(time.RFC3339Nano),
        Operation: "rendezvous.claim",
        PayloadDigest: federation.PayloadDigest(payload),
    }, key)
    if err != nil { t.Fatal(err) }
    wire, err := json.Marshal(peerRendezvousRequest{
        Envelope: envelope,
        Authority: peerRendezvousAuthority{
            Record: peerTestAuthorityRef, Revision: peerTestAuthorityRev, State: "open",
        },
        Payload: payload,
    })
    if err != nil { t.Fatal(err) }

    // Supersede old destination without shutting down its HTTP handler.
    replacement, err := newRuntime(cfg, strings.Repeat("a", 40), "peer-fence-test", clock)
    if err != nil { t.Fatal(err) }
    if replacement.generation != old.generation+1 { t.Fatal("generation was not advanced") }
    response := httptest.NewRecorder()
    old.Handler().ServeHTTP(response,
        httptest.NewRequest(http.MethodPost, "/v1/peer/rendezvous", strings.NewReader(string(wire))))
    if response.Code != http.StatusUnauthorized {
        t.Fatalf("old signed handler status=%d body=%q", response.Code, response.Body.String())
    }
    if verifyCalls != 1 {
        t.Fatalf("expected one canonical authority verification followed by fence rejection, got %d", verifyCalls)
    }

    stored, err := old.rendezvous.store.Get(ctx, offered.Handoff.ID)
    if err != nil { t.Fatal(err) }
    if stored.Status != "offered" || stored.Claim != nil {
        t.Fatalf("old handler mutated durable handoff after generation replacement: %+v", stored)
    }
    // Fresh admission into the old handler's replay cache must remain possible
    // after the stale request was rejected. We probe the cache exactly once.
    duplicate, err := old.peerRendezvous.config.Replay.Admit(
        envelope.SourceNodeID, envelope.SourceGeneration,
        envelope.RequestID, envelope.Signature, now, 5*time.Minute,
    )
    if err != nil || duplicate {
        t.Fatalf("stale request consumed replay identity: duplicate=%v err=%v", duplicate, err)
    }
    // Explicitly verify the source identity used by this negative fixture.
    if sourceCard.Schema != embodiment.CardSchema { t.Fatal("invalid fixture") }
}
