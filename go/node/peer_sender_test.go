package node

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SemperSupra/wow-sidecar/go/embodiment"
	"github.com/SemperSupra/wow-sidecar/go/federation"
)

func senderCredentials(t *testing.T, nodeID string, key []byte) *federation.PeerCredentials {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "peers.json")
	content := `{"schema":"` + federation.PeerCredentialsSchema +
		`","peers":[{"node_id":"` + nodeID + `","key":"` +
		base64.StdEncoding.EncodeToString(key) + `"}]}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	credentials, err := federation.LoadPeerCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	return credentials
}

func senderSourceCard(now time.Time) embodiment.Card {
	return embodiment.Card{
		Schema:         embodiment.CardSchema,
		Protocol:       embodiment.ProtocolVersion,
		NodeID:         "truenas-node",
		Generation:     7,
		IncarnationID:  "0123456789abcdef0123456789abcdef",
		Locality:       "sovereign",
		Endpoints:      []embodiment.Endpoint{},
		Capabilities:   []string{},
		LeaseExpiresAt: now.Add(5 * time.Minute).Format(time.RFC3339Nano),
	}
}

func newSyntheticTwoNode(t *testing.T, now time.Time) (*PeerRendezvousSender, *httptest.Server, string, []byte) {
	t.Helper()
	key := []byte("0123456789abcdef0123456789abcdef")
	handler, _, handoffID, _ := newPeerHandlerForTest(t, now, func(context.Context, string, string, string) error {
		return nil
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	sender, err := NewPeerRendezvousSender(PeerRendezvousSenderConfig{
		SourceCard:        senderSourceCard(now),
		DestinationNodeID: "oci-edge-node",
		Credentials:       senderCredentials(t, "oci-edge-node", key),
		Client:            server.Client(),
		Now:               func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return sender, server, handoffID, key
}

func TestPeerRendezvousSenderTwoNodeClaimAndComplete(t *testing.T) {
	now := time.Date(2026, 10, 4, 18, 0, 0, 0, time.UTC)
	sender, server, handoffID, _ := newSyntheticTwoNode(t, now)

	claimPayload := json.RawMessage(`{"operation":"claim","handoff_id":"` + handoffID + `","launch_id":"launch-123"}`)
	claimRequest := PeerRendezvousSendRequest{
		Endpoint:          server.URL + "/peer-rendezvous",
		RequestID:         "peerreq-1001",
		Operation:         "rendezvous.claim",
		AuthorityRecord:   peerTestAuthorityRef,
		AuthorityRevision: peerTestAuthorityRev,
		AuthorityState:    "open",
		Payload:           claimPayload,
	}
	claimed, err := sender.Send(context.Background(), claimRequest)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Status != "rendezvous-claimed" || claimed.Duplicate {
		t.Fatalf("unexpected claim: %+v", claimed)
	}
	actorID, ok := claimed.Data["actor_instance_id"].(string)
	if !ok || actorID == "" {
		t.Fatalf("missing actor id: %+v", claimed.Data)
	}

	duplicateClaim, err := sender.Send(context.Background(), claimRequest)
	if err != nil {
		t.Fatal(err)
	}
	if duplicateClaim.Status != "rendezvous-existing-claim" || !duplicateClaim.Duplicate {
		t.Fatalf("unexpected duplicate claim: %+v", duplicateClaim)
	}

	completePayload := json.RawMessage(`{"operation":"complete","handoff_id":"` + handoffID +
		`","actor_instance_id":"` + actorID + `","status":"completed","result_ref":"github:result"}`)
	completeRequest := PeerRendezvousSendRequest{
		Endpoint:          server.URL + "/peer-rendezvous",
		RequestID:         "peerreq-1002",
		Operation:         "rendezvous.complete",
		AuthorityRecord:   peerTestAuthorityRef,
		AuthorityRevision: peerTestAuthorityRev,
		AuthorityState:    "open",
		Payload:           completePayload,
	}
	completed, err := sender.Send(context.Background(), completeRequest)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "rendezvous-completed" || completed.Duplicate {
		t.Fatalf("unexpected completion: %+v", completed)
	}

	duplicateComplete, err := sender.Send(context.Background(), completeRequest)
	if err != nil {
		t.Fatal(err)
	}
	if duplicateComplete.Status != "rendezvous-already-completed" || !duplicateComplete.Duplicate {
		t.Fatalf("unexpected duplicate completion: %+v", duplicateComplete)
	}
}

func TestPeerRendezvousSenderDoesNotRetryTransport(t *testing.T) {
	now := time.Now().UTC()
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, "temporary", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	key := []byte("0123456789abcdef0123456789abcdef")
	sender, err := NewPeerRendezvousSender(PeerRendezvousSenderConfig{
		SourceCard:        senderSourceCard(now),
		DestinationNodeID: "oci-edge-node",
		Credentials:       senderCredentials(t, "oci-edge-node", key),
		Client:            server.Client(),
		Now:               func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"operation":"claim","handoff_id":"handoff_abc","launch_id":"launch-123"}`)
	_, err = sender.Send(context.Background(), PeerRendezvousSendRequest{
		Endpoint:          server.URL + "/peer-rendezvous",
		RequestID:         "peerreq-1003",
		Operation:         "rendezvous.claim",
		AuthorityRecord:   peerTestAuthorityRef,
		AuthorityRevision: peerTestAuthorityRev,
		AuthorityState:    "open",
		Payload:           payload,
	})
	if err == nil {
		t.Fatal("server failure unexpectedly succeeded")
	}
	if attempts != 1 {
		t.Fatalf("sender retried transport attempts=%d", attempts)
	}
}

func TestPeerRendezvousSenderRejectsUnsafeEndpointAndOperation(t *testing.T) {
	now := time.Now().UTC()
	key := []byte("0123456789abcdef0123456789abcdef")
	sender, err := NewPeerRendezvousSender(PeerRendezvousSenderConfig{
		SourceCard:        senderSourceCard(now),
		DestinationNodeID: "oci-edge-node",
		Credentials:       senderCredentials(t, "oci-edge-node", key),
		Now:               func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"operation":"claim","handoff_id":"handoff_abc","launch_id":"launch-123"}`)
	for _, endpoint := range []string{
		"http://user:pass@example.invalid/peer",
		"http://example.invalid/peer?query=1",
		"http://example.invalid/peer#fragment",
		"file:///tmp/socket",
		"http://example.invalid/",
	} {
		_, err := sender.Send(context.Background(), PeerRendezvousSendRequest{
			Endpoint:          endpoint,
			RequestID:         "peerreq-1004",
			Operation:         "rendezvous.claim",
			AuthorityRecord:   peerTestAuthorityRef,
			AuthorityRevision: peerTestAuthorityRev,
			AuthorityState:    "open",
			Payload:           payload,
		})
		if err == nil {
			t.Fatalf("unsafe endpoint %q unexpectedly accepted", endpoint)
		}
	}

	_, err = sender.Send(context.Background(), PeerRendezvousSendRequest{
		Endpoint:          "http://example.invalid/peer",
		RequestID:         "peerreq-1005",
		Operation:         "rendezvous.offer",
		AuthorityRecord:   peerTestAuthorityRef,
		AuthorityRevision: peerTestAuthorityRev,
		AuthorityState:    "open",
		Payload:           payload,
	})
	if err == nil {
		t.Fatal("unsupported operation unexpectedly accepted")
	}
}

func TestPeerRendezvousSenderRejectsWrongDestinationCredential(t *testing.T) {
	now := time.Now().UTC()
	key := []byte("0123456789abcdef0123456789abcdef")
	sender, err := NewPeerRendezvousSender(PeerRendezvousSenderConfig{
		SourceCard:        senderSourceCard(now),
		DestinationNodeID: "oci-edge-node",
		Credentials:       senderCredentials(t, "different-node", key),
		Now:               func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"operation":"claim","handoff_id":"handoff_abc","launch_id":"launch-123"}`)
	_, err = sender.Send(context.Background(), PeerRendezvousSendRequest{
		Endpoint:          "http://example.invalid/peer",
		RequestID:         "peerreq-1006",
		Operation:         "rendezvous.claim",
		AuthorityRecord:   peerTestAuthorityRef,
		AuthorityRevision: peerTestAuthorityRev,
		AuthorityState:    "open",
		Payload:           payload,
	})
	if err == nil {
		t.Fatal("missing destination credential unexpectedly accepted")
	}
}

func TestPeerRendezvousSenderRejectsMalformedResponseAndBoundsSize(t *testing.T) {
	now := time.Now().UTC()
	key := []byte("0123456789abcdef0123456789abcdef")
	cases := []string{
		`{"result":"ELIGIBLE","status":"ok","mutation_performed":false,"duplicate":false,"data":{},"extra":true}`,
		`{"result":"ELIGIBLE","status":"ok","mutation_performed":false,"duplicate":false,"data":{}}{}`,
		strings.Repeat("x", maxPeerRendezvousResponseBytes+1),
	}
	for index, body := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
		sender, err := NewPeerRendezvousSender(PeerRendezvousSenderConfig{
			SourceCard:        senderSourceCard(now),
			DestinationNodeID: "oci-edge-node",
			Credentials:       senderCredentials(t, "oci-edge-node", key),
			Client:            server.Client(),
			Now:               func() time.Time { return now },
		})
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		payload := json.RawMessage(`{"operation":"claim","handoff_id":"handoff_abc","launch_id":"launch-123"}`)
		_, err = sender.Send(context.Background(), PeerRendezvousSendRequest{
			Endpoint:          server.URL + "/peer",
			RequestID:         "peerreq-1007",
			Operation:         "rendezvous.claim",
			AuthorityRecord:   peerTestAuthorityRef,
			AuthorityRevision: peerTestAuthorityRev,
			AuthorityState:    "open",
			Payload:           payload,
		})
		server.Close()
		if err == nil {
			t.Fatalf("malformed response case %d unexpectedly accepted", index)
		}
	}
}

func TestPeerRendezvousSenderWireContainsNoTranscriptOrSecrets(t *testing.T) {
	now := time.Now().UTC()
	key := []byte("0123456789abcdef0123456789abcdef")
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"ELIGIBLE","status":"rendezvous-existing-claim","mutation_performed":false,"duplicate":true,"data":{"handoff_id":"handoff_abc","handoff_status":"claimed"}}`))
	}))
	defer server.Close()

	sender, err := NewPeerRendezvousSender(PeerRendezvousSenderConfig{
		SourceCard:        senderSourceCard(now),
		DestinationNodeID: "oci-edge-node",
		Credentials:       senderCredentials(t, "oci-edge-node", key),
		Client:            server.Client(),
		Now:               func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"operation":"claim","handoff_id":"handoff_abc","launch_id":"launch-123"}`)
	if _, err := sender.Send(context.Background(), PeerRendezvousSendRequest{
		Endpoint:          server.URL + "/peer",
		RequestID:         "peerreq-1008",
		Operation:         "rendezvous.claim",
		AuthorityRecord:   peerTestAuthorityRef,
		AuthorityRevision: peerTestAuthorityRev,
		AuthorityState:    "open",
		Payload:           payload,
	}); err != nil {
		t.Fatal(err)
	}
	wire := string(captured)
	if strings.Contains(wire, string(key)) || strings.Contains(wire, "transcript") || strings.Contains(wire, "conversation") {
		t.Fatalf("wire exposed secret/transcript material: %s", wire)
	}
}
