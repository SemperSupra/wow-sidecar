package node

import (
	"strings"
	"testing"
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
