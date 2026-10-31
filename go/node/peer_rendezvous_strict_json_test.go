package node

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SemperSupra/wow-sidecar/go/rendezvous"
)

func TestPeerRendezvousRejectsAmbiguousAuthorityWireKeys(t *testing.T) {
	revision := "sha256:" + strings.Repeat("a", 64)
	authority := `"record":"issue-comment:123","revision":"` + revision + `"`
	valid := `{"envelope":{},"authority":{` + authority + `,"state":"open"},"payload":{}}`
	if _, err := decodePeerRendezvousRequest([]byte(valid)); err != nil {
		t.Fatalf("canonical unique-key control rejected: %v", err)
	}
	tests := []struct{ name, wire string }{
		{"duplicate_state", `{"envelope":{},"authority":{` + authority + `,"state":"closed","state":"open"},"payload":{}}`},
		{"escaped_equivalent_state", `{"envelope":{},"authority":{` + authority + `,"st\u0061te":"closed","state":"open"},"payload":{}}`},
		{"case_variant_state", `{"envelope":{},"authority":{` + authority + `,"STATE":"closed","state":"open"},"payload":{}}`},
		{"duplicate_authority_object", `{"envelope":{},"authority":{"state":"closed"},"authority":{` + authority + `,"state":"open"},"payload":{}}`},
		{"duplicate_nested_envelope_key", `{"envelope":{"source_node_id":"node-a","source_node_id":"node-b"},"authority":{` + authority + `,"state":"open"},"payload":{}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodePeerRendezvousRequest([]byte(tc.wire)); err == nil {
				t.Fatal("ambiguous authority-bearing JSON was accepted")
			}
		})
	}
}

func TestPeerRendezvousAmbiguousAuthorityDoesNotReachVerifierOrConsumeReplay(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	authorityCalls := 0
	handler, service, handoffID, key := newPeerHandlerForTest(t, now, func(_ context.Context, _, _, _ string) error {
		authorityCalls++
		return nil
	})
	payload := json.RawMessage(`{"operation":"claim","handoff_id":"` + handoffID + `","launch_id":"launch-ambiguous"}`)
	valid := signedPeerWireRequest(t, now, key, "peerreq-ambig-01", "rendezvous.claim", payload)
	ambiguous := bytes.Replace(valid, []byte(`"state":"open"`), []byte(`"state":"closed","state":"open"`), 1)
	if bytes.Equal(ambiguous, valid) {
		t.Fatal("test failed to inject duplicate authority state")
	}
	if response := servePeerRequest(handler, ambiguous); response.Code != http.StatusBadRequest {
		t.Fatalf("ambiguous authority status=%d body=%q", response.Code, response.Body.String())
	}
	if authorityCalls != 0 {
		t.Fatalf("authority verifier ran for rejected wire data: calls=%d", authorityCalls)
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
		t.Fatalf("ambiguous request changed durable rendezvous state: %+v", state)
	}
	if response := servePeerRequest(handler, valid); response.Code != http.StatusOK {
		t.Fatalf("valid same-id request after malformed input status=%d body=%q", response.Code, response.Body.String())
	}
	if authorityCalls != 1 {
		t.Fatalf("valid request did not reach authority exactly once: calls=%d", authorityCalls)
	}
}

func TestPeerRendezvousRejectsAmbiguousTypedOperationKeys(t *testing.T) {
	if err := validatePeerRendezvousPayload("rendezvous.claim", []byte(`{"operation":"claim","handoff_id":"handoff-01","launch_id":"launch-01"}`)); err != nil {
		t.Fatalf("canonical unique-key claim rejected: %v", err)
	}
	tests := []struct{ name, body string }{
		{"duplicate_operation", `{"operation":"complete","operation":"claim","handoff_id":"handoff-01","launch_id":"launch-01"}`},
		{"escaped_equivalent_operation", `{"oper\u0061tion":"complete","operation":"claim","handoff_id":"handoff-01","launch_id":"launch-01"}`},
		{"case_variant_operation", `{"OPERATION":"complete","operation":"claim","handoff_id":"handoff-01","launch_id":"launch-01"}`},
		{"duplicate_launch_id", `{"operation":"claim","handoff_id":"handoff-01","launch_id":"first","launch_id":"launch-01"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := validatePeerRendezvousPayload("rendezvous.claim", []byte(tc.body)); err == nil {
				t.Fatal("ambiguous typed peer-operation JSON was accepted")
			}
		})
	}
}
