//go:build linux

package node

import (
    "bufio"
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "net/http/httptest"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "testing"
    "time"

    "github.com/SemperSupra/wow-sidecar/go/federation"
    "github.com/SemperSupra/wow-sidecar/go/rendezvous"
)

// TestSignedOldPeerHandlerIsFencedAcrossProcesses is an OS-process boundary
// regression of the REAL signed peer handler and its local replay/store
// effects. Every input is synthetic and private to disposable test processes.
func TestSignedOldPeerHandlerIsFencedAcrossProcesses(t *testing.T) {
    const mode = "WOW_TEST_SIGNED_PEER_CHILD"
    const stateEnv = "WOW_TEST_SIGNED_PEER_STATE"
    if os.Getenv(mode) == "old" {
        statePath := os.Getenv(stateEnv)
        if statePath == "" { t.Fatal("missing child state path") }
        now := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
        key := []byte("0123456789abcdef0123456789abcdef")
        sourceCard := peerHandlerCard(now)
        source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if r.Method != http.MethodGet || r.URL.Path != "/v1/card" {
                http.NotFound(w, r)
                return
            }
            _ = json.NewEncoder(w).Encode(sourceCard)
        }))
        defer source.Close()
        config := Config{
            NodeID: "oci-edge-node",
            Locality: "cloud",
            GenerationStateFile: statePath,
            ListenAddr: "127.0.0.1:0",
            LeaseTTL: time.Minute,
            PeerURLs: []string{source.URL},
            PeerPollInterval: 15 * time.Second,
            Rendezvous: &RendezvousConfig{
                WorksetRef: "github-file:SemperSupra/example@main:workset.json",
                DelegationID: "peer-fencing-process-proof",
                Lease: time.Minute,
            },
        }
        runtime, err := newRuntime(config, strings.Repeat("a", 40),
            "synthetic-fence", func() time.Time { return now })
        if err != nil { t.Fatal(err) }
        runtime.peers.PollOnce(context.Background())
        if _, ok := runtime.peers.CurrentCard(sourceCard.NodeID); !ok {
            t.Fatal("test source peer not observed")
        }
        credentialsPath := writePeerRuntimeCredentials(t, sourceCard.NodeID, key)
        credentials, err := federation.LoadPeerCredentials(credentialsPath)
        if err != nil { t.Fatal(err) }
        verifyCalls := 0
        if err := runtime.configurePeerRendezvous(
            &PeerRendezvousRuntimeConfig{
                CredentialsFile: credentialsPath,
                MaxSkew: 2 * time.Minute,
                ReplayTTL: 5 * time.Minute,
            },
            credentials,
            func(_ context.Context, record, revision, state string) error {
                verifyCalls++
                if record != peerTestAuthorityRef ||
                    revision != peerTestAuthorityRev || state != "open" {
                    return fmt.Errorf("canonical authority changed")
                }
                return nil
            },
        ); err != nil { t.Fatal(err) }

        ctx := context.WithValue(context.Background(),
            rendezvousPrincipalContextKey{}, rendezvous.Principal{
                ClientID: "durable-control",
                Subject: peerTestAuthorityRef + "@" + peerTestAuthorityRev,
            })
        offered, err := runtime.rendezvous.store.Offer(ctx,
            config.Rendezvous.WorksetRef, config.Rendezvous.DelegationID, "")
        if err != nil { t.Fatal(err) }
        destination, err := runtime.Card()
        if err != nil { t.Fatal(err) }
        payload := json.RawMessage(`{"operation":"claim","handoff_id":"` +
            offered.Handoff.ID + `","launch_id":"launch-old-process"}`)
        envelope, err := federation.SignPeerEnvelope(federation.PeerEnvelope{
            Schema: federation.PeerEnvelopeSchema,
            SourceNodeID: sourceCard.NodeID,
            SourceGeneration: sourceCard.Generation,
            SourceIncarnationID: sourceCard.IncarnationID,
            DestinationNodeID: destination.NodeID,
            DestinationGeneration: destination.Generation,
            DestinationIncarnationID: destination.IncarnationID,
            RequestID: "peerreq-old-process-01",
            IssuedAt: now.Format(time.RFC3339Nano),
            Operation: "rendezvous.claim",
            PayloadDigest: federation.PayloadDigest(payload),
        }, key)
        if err != nil { t.Fatal(err) }
        wire, err := json.Marshal(peerRendezvousRequest{
            Envelope: envelope,
            Authority: peerRendezvousAuthority{
                Record: peerTestAuthorityRef,
                Revision: peerTestAuthorityRev,
                State: "open",
            },
            Payload: payload,
        })
        if err != nil { t.Fatal(err) }

        // Parent creates a fresh generation while this old process stays
        // alive with the authenticated peer route mounted.
        fmt.Printf("OLD_PEER_READY:%d\n", runtime.generation)
        if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
            t.Fatalf("parent signal failed: %v", err)
        }
        response := httptest.NewRecorder()
        runtime.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost,
            "/v1/peer/rendezvous", bytes.NewReader(wire)))
        if response.Code != http.StatusUnauthorized {
            t.Fatalf("old signed peer mutation accepted: code=%d body=%q",
                response.Code, response.Body.String())
        }
        if verifyCalls != 1 {
            t.Fatalf("canonical authority must be checked once, got %d", verifyCalls)
        }
        result, err := runtime.rendezvous.store.Get(ctx, offered.Handoff.ID)
        if err != nil { t.Fatal(err) }
        if result.Status != "offered" || result.Claim != nil {
            t.Fatalf("old process mutated handoff: %+v", result)
        }
        duplicate, err := runtime.peerRendezvous.config.Replay.Admit(
            envelope.SourceNodeID, envelope.SourceGeneration,
            envelope.RequestID, envelope.Signature, now, 5*time.Minute)
        if err != nil || duplicate {
            t.Fatalf("old process consumed replay identity: %v %v", duplicate, err)
        }
        fmt.Printf("OLD_PEER_FENCED:replay=0:mutation=0\n")
        return
    }

    statePath := filepath.Join(t.TempDir(), "generation.json")
    ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
    defer cancel()
    cmd := exec.CommandContext(ctx, os.Args[0],
        "-test.run=^TestSignedOldPeerHandlerIsFencedAcrossProcesses$")
    cmd.Env = append(os.Environ(), mode+"=old", stateEnv+"="+statePath)
    stdin, err := cmd.StdinPipe()
    if err != nil { t.Fatal(err) }
    stdout, err := cmd.StdoutPipe()
    if err != nil { t.Fatal(err) }
    var stderr bytes.Buffer
    cmd.Stderr = &stderr
    if err := cmd.Start(); err != nil { t.Fatal(err) }
    defer func() { cancel(); _ = cmd.Process.Kill() }()
    reader := bufio.NewReader(stdout)
    ready, err := reader.ReadString('\n')
    if err != nil {
        t.Fatalf("old child failed startup: %v stderr=%q", err, stderr.String())
    }
    if strings.TrimSpace(ready) != "OLD_PEER_READY:1" {
        t.Fatalf("unexpected old child: %q stderr=%q", ready, stderr.String())
    }
    config := runtimeConfig(t)
    config.NodeID = "oci-edge-node"
    config.Locality = "cloud"
    config.GenerationStateFile = statePath
    replacement, err := newRuntime(config, strings.Repeat("a", 40),
        "synthetic-replacement", time.Now)
    if err != nil { t.Fatal(err) }
    if replacement.generation != 2 {
        t.Fatalf("expected generation 2, got %d", replacement.generation)
    }
    if _, err := fmt.Fprintln(stdin, "send-signed-old-claim"); err != nil {
        t.Fatal(err)
    }
    output, err := reader.ReadString('\n')
    if err != nil {
        t.Fatalf("old child returned no evidence: %v stderr=%q", err, stderr.String())
    }
    if strings.TrimSpace(output) != "OLD_PEER_FENCED:replay=0:mutation=0" {
        t.Fatalf("incorrect old-handler evidence %q stderr=%q", output, stderr.String())
    }
    _ = stdin.Close()
    if err := cmd.Wait(); err != nil {
        t.Fatalf("old child process failed: %v stderr=%q", err, stderr.String())
    }
}
