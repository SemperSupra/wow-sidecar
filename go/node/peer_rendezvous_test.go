package node

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SemperSupra/wow-sidecar/go/embodiment"
	"github.com/SemperSupra/wow-sidecar/go/federation"
	"github.com/SemperSupra/wow-sidecar/go/rendezvous"
)

const (
	peerTestAuthorityRef = "github-issue-comment:SemperSupra/example#1:123"
	peerTestAuthorityRev = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func peerHandlerCredentials(t *testing.T, key []byte) *federation.PeerCredentials {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "peers.json")
	content := `{"schema":"` + federation.PeerCredentialsSchema +
		`","peers":[{"node_id":"truenas-node","key":"` +
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

func peerHandlerCard(now time.Time) embodiment.Card {
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

func peerHandlerService(t *testing.T, now time.Time) (*RendezvousService, string) {
	t.Helper()
	service, err := newRendezvousService(&RendezvousConfig{
		WorksetRef:   "github-file:SemperSupra/example@main:workset.json",
		DelegationID: "peer-test",
		Lease:        time.Minute,
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(
		context.Background(),
		rendezvousPrincipalContextKey{},
		rendezvous.Principal{
			ClientID: "durable-control",
			Subject:  peerTestAuthorityRef + "@" + peerTestAuthorityRev,
		},
	)
	result, err := service.store.Offer(ctx, service.config.WorksetRef, service.config.DelegationID, "")
	if err != nil {
		t.Fatal(err)
	}
	return service, result.Handoff.ID
}

func newPeerHandlerForTest(
	t *testing.T,
	now time.Time,
	verify PeerAuthorityVerifier,
) (*PeerRendezvousHandler, *RendezvousService, string, []byte) {
	t.Helper()
	key := []byte("0123456789abcdef0123456789abcdef")
	service, handoffID := peerHandlerService(t, now)
	handler, err := NewPeerRendezvousHandler(PeerRendezvousHandlerConfig{
		CurrentDestination: func() (embodiment.Card, bool) {
			return peerDestinationTestCard(now), true
		},
		CurrentPeer: func(nodeID string) (embodiment.Card, bool) {
			if nodeID != "truenas-node" {
				return embodiment.Card{}, false
			}
			return peerHandlerCard(now), true
		},
		Credentials:     peerHandlerCredentials(t, key),
		VerifyAuthority: verify,
		Rendezvous:      service,
		Replay:          federation.NewReplayCache(),
		Now:             func() time.Time { return now },
		MaxSkew:         2 * time.Minute,
		ReplayTTL:       5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler, service, handoffID, key
}

func signedPeerWireRequest(
	t *testing.T,
	now time.Time,
	key []byte,
	requestID string,
	operation string,
	payload json.RawMessage,
) []byte {
	t.Helper()
	envelope, err := federation.SignPeerEnvelope(federation.PeerEnvelope{
		Schema:              federation.PeerEnvelopeSchema,
		SourceNodeID:        "truenas-node",
		SourceGeneration:    7,
		SourceIncarnationID: "0123456789abcdef0123456789abcdef",
		DestinationNodeID:        "oci-edge-node",
		DestinationGeneration:    11,
		DestinationIncarnationID: "abcdefabcdefabcdefabcdefabcdefab",
		RequestID:                requestID,
		IssuedAt:            now.Format(time.RFC3339Nano),
		Operation:           operation,
		PayloadDigest:       federation.PayloadDigest(payload),
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(peerRendezvousRequest{
		Envelope: envelope,
		Authority: peerRendezvousAuthority{
			Record:   peerTestAuthorityRef,
			Revision: peerTestAuthorityRev,
			State:    "open",
		},
		Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func servePeerRequest(handler http.Handler, body []byte) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/synthetic-peer-rendezvous", strings.NewReader(string(body)))
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decodePeerResponse(t *testing.T, recorder *httptest.ResponseRecorder) peerRendezvousResponse {
	t.Helper()
	var response peerRendezvousResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v body=%q", err, recorder.Body.String())
	}
	return response
}

func TestPeerRendezvousHandlerClaimDuplicateAndComplete(t *testing.T) {
	now := time.Date(2026, 10, 4, 17, 0, 0, 0, time.UTC)
	handler, _, handoffID, key := newPeerHandlerForTest(t, now, func(context.Context, string, string, string) error {
		return nil
	})

	claimPayload := json.RawMessage(`{"operation":"claim","handoff_id":"` + handoffID + `","launch_id":"launch-123"}`)
	claim := signedPeerWireRequest(t, now, key, "peerreq-0001", "rendezvous.claim", claimPayload)

	first := servePeerRequest(handler, claim)
	if first.Code != http.StatusOK {
		t.Fatalf("claim status=%d body=%q", first.Code, first.Body.String())
	}
	firstResponse := decodePeerResponse(t, first)
	if firstResponse.Status != "rendezvous-claimed" || firstResponse.Duplicate {
		t.Fatalf("unexpected first claim response: %+v", firstResponse)
	}
	actorID, ok := firstResponse.Data["actor_instance_id"].(string)
	if !ok || actorID == "" {
		t.Fatalf("missing actor_instance_id: %+v", firstResponse.Data)
	}

	repeated := servePeerRequest(handler, claim)
	if repeated.Code != http.StatusOK {
		t.Fatalf("duplicate claim status=%d body=%q", repeated.Code, repeated.Body.String())
	}
	repeatResponse := decodePeerResponse(t, repeated)
	if repeatResponse.Status != "rendezvous-existing-claim" || !repeatResponse.Duplicate || repeatResponse.MutationPerformed {
		t.Fatalf("unexpected duplicate claim response: %+v", repeatResponse)
	}

	completePayload := json.RawMessage(`{"operation":"complete","handoff_id":"` + handoffID +
		`","actor_instance_id":"` + actorID + `","status":"completed","result_ref":"github:result"}`)
	complete := signedPeerWireRequest(t, now, key, "peerreq-0002", "rendezvous.complete", completePayload)
	done := servePeerRequest(handler, complete)
	if done.Code != http.StatusOK {
		t.Fatalf("complete status=%d body=%q", done.Code, done.Body.String())
	}
	doneResponse := decodePeerResponse(t, done)
	if doneResponse.Status != "rendezvous-completed" || doneResponse.Duplicate || !doneResponse.MutationPerformed {
		t.Fatalf("unexpected completion response: %+v", doneResponse)
	}

	doneAgain := servePeerRequest(handler, complete)
	if doneAgain.Code != http.StatusOK {
		t.Fatalf("duplicate complete status=%d body=%q", doneAgain.Code, doneAgain.Body.String())
	}
	againResponse := decodePeerResponse(t, doneAgain)
	if againResponse.Status != "rendezvous-already-completed" || !againResponse.Duplicate || againResponse.MutationPerformed {
		t.Fatalf("unexpected duplicate completion response: %+v", againResponse)
	}
}

func TestPeerRendezvousHandlerAuthorityFailureDoesNotConsumeReplayOrMutate(t *testing.T) {
	now := time.Now().UTC()
	attempts := 0
	handler, service, handoffID, key := newPeerHandlerForTest(t, now, func(context.Context, string, string, string) error {
		attempts++
		if attempts == 1 {
			return context.DeadlineExceeded
		}
		return nil
	})
	payload := json.RawMessage(`{"operation":"claim","handoff_id":"` + handoffID + `","launch_id":"launch-123"}`)
	wire := signedPeerWireRequest(t, now, key, "peerreq-0003", "rendezvous.claim", payload)

	blocked := servePeerRequest(handler, wire)
	if blocked.Code != http.StatusServiceUnavailable {
		t.Fatalf("authority failure status=%d body=%q", blocked.Code, blocked.Body.String())
	}
	ctx := context.WithValue(context.Background(), rendezvousPrincipalContextKey{}, rendezvous.Principal{
		ClientID: "durable-control",
		Subject:  peerTestAuthorityRef + "@" + peerTestAuthorityRev,
	})
	state, err := service.store.Get(ctx, handoffID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "offered" || state.Claim != nil {
		t.Fatalf("authority failure mutated handoff: %+v", state)
	}

	retry := servePeerRequest(handler, wire)
	if retry.Code != http.StatusOK {
		t.Fatalf("authority-recovered retry status=%d body=%q", retry.Code, retry.Body.String())
	}
	response := decodePeerResponse(t, retry)
	if response.Duplicate {
		t.Fatal("authority-failed request identity was incorrectly consumed")
	}
}

func TestPeerRendezvousHandlerRejectsStalePeerBeforeAuthority(t *testing.T) {
	now := time.Now().UTC()
	key := []byte("0123456789abcdef0123456789abcdef")
	service, handoffID := peerHandlerService(t, now)
	authorityCalls := 0
	handler, err := NewPeerRendezvousHandler(PeerRendezvousHandlerConfig{
		CurrentDestination: func() (embodiment.Card, bool) {
			return peerDestinationTestCard(now), true
		},
		CurrentPeer: func(string) (embodiment.Card, bool) {
			card := peerHandlerCard(now)
			card.Generation = 8
			return card, true
		},
		Credentials: peerHandlerCredentials(t, key),
		VerifyAuthority: func(context.Context, string, string, string) error {
			authorityCalls++
			return nil
		},
		Rendezvous: service,
		Replay:     federation.NewReplayCache(),
		Now:        func() time.Time { return now },
		MaxSkew:    2 * time.Minute,
		ReplayTTL:  5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"operation":"claim","handoff_id":"` + handoffID + `","launch_id":"launch-123"}`)
	wire := signedPeerWireRequest(t, now, key, "peerreq-0004", "rendezvous.claim", payload)
	response := servePeerRequest(handler, wire)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("stale peer status=%d body=%q", response.Code, response.Body.String())
	}
	if authorityCalls != 0 {
		t.Fatal("authority verifier called before peer authentication/fencing completed")
	}
}

func TestPeerRendezvousHandlerRejectsStaleDestinationBeforeAuthority(t *testing.T) {
	now := time.Now().UTC()
	key := []byte("0123456789abcdef0123456789abcdef")
	service, handoffID := peerHandlerService(t, now)
	authorityCalls := 0
	handler, err := NewPeerRendezvousHandler(PeerRendezvousHandlerConfig{
		CurrentDestination: func() (embodiment.Card, bool) {
			card := peerDestinationTestCard(now)
			card.Generation++
			card.IncarnationID = "11111111111111111111111111111111"
			return card, true
		},
		CurrentPeer: func(string) (embodiment.Card, bool) {
			return peerHandlerCard(now), true
		},
		Credentials: peerHandlerCredentials(t, key),
		VerifyAuthority: func(context.Context, string, string, string) error {
			authorityCalls++
			return nil
		},
		Rendezvous: service,
		Replay:     federation.NewReplayCache(),
		Now:        func() time.Time { return now },
		MaxSkew:    2 * time.Minute,
		ReplayTTL:  5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"operation":"claim","handoff_id":"` + handoffID + `","launch_id":"launch-123"}`)
	wire := signedPeerWireRequest(t, now, key, "peerreq-0009", "rendezvous.claim", payload)
	response := servePeerRequest(handler, wire)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("stale destination status=%d body=%q", response.Code, response.Body.String())
	}
	if authorityCalls != 0 {
		t.Fatal("authority verifier called before destination fencing completed")
	}
}

func TestPeerRendezvousHandlerRejectsRequestIDReuseWithDifferentMessage(t *testing.T) {
	now := time.Now().UTC()
	handler, _, handoffID, key := newPeerHandlerForTest(t, now, func(context.Context, string, string, string) error {
		return nil
	})
	firstPayload := json.RawMessage(`{"operation":"claim","handoff_id":"` + handoffID + `","launch_id":"launch-123"}`)
	first := signedPeerWireRequest(t, now, key, "peerreq-0005", "rendezvous.claim", firstPayload)
	if response := servePeerRequest(handler, first); response.Code != http.StatusOK {
		t.Fatalf("initial status=%d body=%q", response.Code, response.Body.String())
	}

	secondPayload := json.RawMessage(`{"operation":"claim","handoff_id":"` + handoffID + `","launch_id":"launch-999"}`)
	second := signedPeerWireRequest(t, now, key, "peerreq-0005", "rendezvous.claim", secondPayload)
	response := servePeerRequest(handler, second)
	if response.Code != http.StatusConflict {
		t.Fatalf("request-id reuse status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestPeerRendezvousHandlerRejectsMalformedOversizeAndOperationMismatch(t *testing.T) {
	now := time.Now().UTC()
	handler, _, handoffID, key := newPeerHandlerForTest(t, now, func(context.Context, string, string, string) error {
		return nil
	})

	oversize := servePeerRequest(handler, []byte(strings.Repeat("x", maxPeerRendezvousRequestBytes+1)))
	if oversize.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status=%d", oversize.Code)
	}

	payload := json.RawMessage(`{"operation":"complete","handoff_id":"` + handoffID +
		`","actor_instance_id":"actor_invalid","status":"completed"}`)
	mismatch := signedPeerWireRequest(t, now, key, "peerreq-0006", "rendezvous.claim", payload)
	if response := servePeerRequest(handler, mismatch); response.Code != http.StatusBadRequest {
		t.Fatalf("operation mismatch status=%d body=%q", response.Code, response.Body.String())
	}

	validPayload := json.RawMessage(`{"operation":"claim","handoff_id":"` + handoffID + `","launch_id":"launch-123"}`)
	valid := signedPeerWireRequest(t, now, key, "peerreq-0007", "rendezvous.claim", validPayload)
	var object map[string]any
	if err := json.Unmarshal(valid, &object); err != nil {
		t.Fatal(err)
	}
	object["extra"] = true
	unknown, _ := json.Marshal(object)
	if response := servePeerRequest(handler, unknown); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d body=%q", response.Code, response.Body.String())
	}
	if response := servePeerRequest(handler, append(valid, []byte("{}")...)); response.Code != http.StatusBadRequest {
		t.Fatalf("trailing json status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestPeerRendezvousResponseIsDataMinimized(t *testing.T) {
	now := time.Now().UTC()
	handler, _, handoffID, key := newPeerHandlerForTest(t, now, func(context.Context, string, string, string) error {
		return nil
	})
	payload := json.RawMessage(`{"operation":"claim","handoff_id":"` + handoffID + `","launch_id":"launch-123"}`)
	wire := signedPeerWireRequest(t, now, key, "peerreq-0008", "rendezvous.claim", payload)
	response := servePeerRequest(handler, wire)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, forbidden := range []string{
		"workset_ref", "delegation_id", "offered_by", "durable-control",
		peerTestAuthorityRef, peerTestAuthorityRev, "0123456789abcdef0123456789abcdef",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, body)
		}
	}
}

func TestPeerRendezvousHandlerIsNotRegisteredInRuntime(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "generation.json")
	runtime, err := NewRuntime(Config{
		NodeID:              "synthetic-node",
		Locality:            "sovereign",
		GenerationStateFile: stateFile,
		ListenAddr:          "127.0.0.1:0",
		LeaseTTL:            time.Minute,
		PeerPollInterval:    15 * time.Second,
	}, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "test")
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/peer/rendezvous", strings.NewReader("{}"))
	runtime.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("synthetic peer handler unexpectedly registered: status=%d", recorder.Code)
	}
}
