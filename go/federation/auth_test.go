package federation

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SemperSupra/wow-sidecar/go/embodiment"
)

func peerTestCard(now time.Time) embodiment.Card {
	return embodiment.Card{
		Schema:         embodiment.CardSchema,
		Protocol:       embodiment.ProtocolVersion,
		NodeID:         "truenas-node",
		Generation:     7,
		IncarnationID:  "0123456789abcdef0123456789abcdef",
		Locality:       "sovereign",
		Endpoints:      []embodiment.Endpoint{},
		Capabilities:   []string{},
		LeaseExpiresAt: now.Add(time.Minute).UTC().Format(time.RFC3339Nano),
	}
}

func peerDestinationTestCard(now time.Time) embodiment.Card {
	return embodiment.Card{
		Schema:         embodiment.CardSchema,
		Protocol:       embodiment.ProtocolVersion,
		NodeID:         "oci-edge-node",
		Generation:     11,
		IncarnationID:  "abcdefabcdefabcdefabcdefabcdefab",
		Locality:       "cloud",
		Endpoints:      []embodiment.Endpoint{},
		Capabilities:   []string{},
		LeaseExpiresAt: now.Add(time.Minute).UTC().Format(time.RFC3339Nano),
	}
}

func peerTestEnvelope(now time.Time, payload []byte) PeerEnvelope {
	return PeerEnvelope{
		Schema:                   PeerEnvelopeSchema,
		SourceNodeID:             "truenas-node",
		SourceGeneration:         7,
		SourceIncarnationID:      "0123456789abcdef0123456789abcdef",
		DestinationNodeID:        "oci-edge-node",
		DestinationGeneration:    11,
		DestinationIncarnationID: "abcdefabcdefabcdefabcdefabcdefab",
		RequestID:                "peerreq-0001",
		IssuedAt:            now.UTC().Format(time.RFC3339Nano),
		Operation:           "rendezvous.claim",
		PayloadDigest:       PayloadDigest(payload),
	}
}

func TestPeerEnvelopeSignVerify(t *testing.T) {
	now := time.Date(2026, 10, 4, 16, 0, 0, 0, time.UTC)
	payload := []byte(`{"handoff_id":"handoff_abc","launch_id":"launch-123"}`)
	key := []byte("0123456789abcdef0123456789abcdef")
	env, err := SignPeerEnvelope(peerTestEnvelope(now, payload), key)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPeerEnvelope(env, key, peerDestinationTestCard(now), peerTestCard(now), now, 2*time.Minute, payload); err != nil {
		t.Fatalf("verify: %v", err)
	}
}


func TestDecodePeerEnvelopeStrict(t *testing.T) {
	now := time.Now().UTC()
	payload := []byte("{}")
	key := []byte("0123456789abcdef0123456789abcdef")
	env, err := SignPeerEnvelope(peerTestEnvelope(now, payload), key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePeerEnvelope(raw)
	if err != nil {
		t.Fatalf("decode valid envelope: %v", err)
	}
	if decoded.RequestID != env.RequestID || decoded.Signature != env.Signature {
		t.Fatal("decoded envelope did not preserve signed identity")
	}

	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	object["unexpected"] = true
	unknown, _ := json.Marshal(object)
	if _, err := DecodePeerEnvelope(unknown); err == nil {
		t.Fatal("unknown field unexpectedly accepted")
	}
	if _, err := DecodePeerEnvelope(append(raw, []byte("{}")...)); err == nil {
		t.Fatal("trailing JSON unexpectedly accepted")
	}

	unsigned := env
	unsigned.Signature = ""
	unsignedRaw, _ := json.Marshal(unsigned)
	if _, err := DecodePeerEnvelope(unsignedRaw); err == nil {
		t.Fatal("unsigned envelope unexpectedly accepted")
	}
}

func TestPeerEnvelopeRejectsTampering(t *testing.T) {
	now := time.Now().UTC()
	payload := []byte("{}")
	key := []byte("0123456789abcdef0123456789abcdef")
	env, err := SignPeerEnvelope(peerTestEnvelope(now, payload), key)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		edit func(*PeerEnvelope)
	}{
		{"source", func(e *PeerEnvelope) { e.SourceNodeID = "other-node" }},
		{"generation", func(e *PeerEnvelope) { e.SourceGeneration++ }},
		{"incarnation", func(e *PeerEnvelope) { e.SourceIncarnationID = "abcdefabcdefabcdefabcdefabcdefab" }},
		{"destination", func(e *PeerEnvelope) { e.DestinationNodeID = "other-edge" }},
		{"destination-generation", func(e *PeerEnvelope) { e.DestinationGeneration++ }},
		{"destination-incarnation", func(e *PeerEnvelope) { e.DestinationIncarnationID = "11111111111111111111111111111111" }},
		{"request", func(e *PeerEnvelope) { e.RequestID = "peerreq-9999" }},
		{"operation", func(e *PeerEnvelope) { e.Operation = "rendezvous.complete" }},
		{"digest", func(e *PeerEnvelope) { e.PayloadDigest = PayloadDigest([]byte("other")) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			changed := env
			tc.edit(&changed)
			if err := VerifyPeerEnvelope(changed, key, peerDestinationTestCard(now), peerTestCard(now), now, 2*time.Minute, payload); err == nil {
				t.Fatal("tampered envelope unexpectedly verified")
			}
		})
	}
}

func TestPeerEnvelopeRejectsWrongKeyAndPayload(t *testing.T) {
	now := time.Now().UTC()
	payload := []byte("payload")
	key := []byte("0123456789abcdef0123456789abcdef")
	env, err := SignPeerEnvelope(peerTestEnvelope(now, payload), key)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPeerEnvelope(env, []byte("abcdef0123456789abcdef0123456789"), peerDestinationTestCard(now), peerTestCard(now), now, 2*time.Minute, payload); err == nil {
		t.Fatal("wrong key unexpectedly verified")
	}
	if err := VerifyPeerEnvelope(env, key, peerDestinationTestCard(now), peerTestCard(now), now, 2*time.Minute, []byte("changed")); err == nil {
		t.Fatal("changed payload unexpectedly verified")
	}
}

func TestPeerEnvelopeRejectsDestinationRestart(t *testing.T) {
	now := time.Now().UTC()
	payload := []byte("{}")
	key := []byte("0123456789abcdef0123456789abcdef")
	env, err := SignPeerEnvelope(peerTestEnvelope(now, payload), key)
	if err != nil {
		t.Fatal(err)
	}

	destination := peerDestinationTestCard(now)
	destination.Generation++
	destination.IncarnationID = "11111111111111111111111111111111"
	if err := VerifyPeerEnvelope(env, key, destination, peerTestCard(now), now, 2*time.Minute, payload); err == nil {
		t.Fatal("request signed for prior destination generation unexpectedly verified after restart")
	}
}

func TestPeerEnvelopeRejectsStaleGenerationAndIncarnation(t *testing.T) {
	now := time.Now().UTC()
	payload := []byte("{}")
	key := []byte("0123456789abcdef0123456789abcdef")
	env, err := SignPeerEnvelope(peerTestEnvelope(now, payload), key)
	if err != nil {
		t.Fatal(err)
	}
	card := peerTestCard(now)
	card.Generation = 8
	if err := VerifyPeerEnvelope(env, key, "oci-edge-node", card, now, 2*time.Minute, payload); err == nil {
		t.Fatal("stale generation unexpectedly verified")
	}

	card = peerTestCard(now)
	card.IncarnationID = "abcdefabcdefabcdefabcdefabcdefab"
	if err := VerifyPeerEnvelope(env, key, "oci-edge-node", card, now, 2*time.Minute, payload); err == nil {
		t.Fatal("stale incarnation unexpectedly verified")
	}
}

func TestPeerEnvelopeRejectsExpiredPeerAndTimestampSkew(t *testing.T) {
	now := time.Now().UTC()
	payload := []byte("{}")
	key := []byte("0123456789abcdef0123456789abcdef")
	env, err := SignPeerEnvelope(peerTestEnvelope(now.Add(-5*time.Minute), payload), key)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPeerEnvelope(env, key, peerDestinationTestCard(now), peerTestCard(now), now, time.Minute, payload); err == nil {
		t.Fatal("stale timestamp unexpectedly verified")
	}

	env, err = SignPeerEnvelope(peerTestEnvelope(now, payload), key)
	if err != nil {
		t.Fatal(err)
	}
	card := peerTestCard(now)
	card.LeaseExpiresAt = now.Add(-time.Second).Format(time.RFC3339Nano)
	if err := VerifyPeerEnvelope(env, key, "oci-edge-node", card, now, time.Minute, payload); err == nil {
		t.Fatal("expired peer card unexpectedly verified")
	}
}

func TestPeerEnvelopeOnlyAdmitsBoundedRendezvousOperations(t *testing.T) {
	now := time.Now().UTC()
	payload := []byte("{}")
	key := []byte("0123456789abcdef0123456789abcdef")
	for _, operation := range []string{"notice", "offer", "invoke", "shell", "rendezvous.notice"} {
		env := peerTestEnvelope(now, payload)
		env.Operation = operation
		if _, err := SignPeerEnvelope(env, key); err == nil {
			t.Fatalf("operation %q unexpectedly admitted", operation)
		}
	}
}

func TestReplayCacheClassifiesExactDuplicateAndExpires(t *testing.T) {
	now := time.Now().UTC()
	cache := NewReplayCache()
	signature := "hmac-sha256:" + strings.Repeat("a", 64)
	duplicate, err := cache.Admit("truenas-node", 7, "peerreq-0001", signature, now, time.Minute)
	if err != nil || duplicate {
		t.Fatalf("first request duplicate=%v err=%v", duplicate, err)
	}
	duplicate, err = cache.Admit("truenas-node", 7, "peerreq-0001", signature, now.Add(10*time.Second), time.Minute)
	if err != nil || !duplicate {
		t.Fatalf("exact duplicate not classified duplicate=%v err=%v", duplicate, err)
	}
	duplicate, err = cache.Admit("truenas-node", 7, "peerreq-0001", signature, now.Add(61*time.Second), time.Minute)
	if err != nil || duplicate {
		t.Fatalf("expired replay entry not reusable duplicate=%v err=%v", duplicate, err)
	}
}

func TestReplayCacheRejectsRequestIDReuseWithDifferentSignedMessage(t *testing.T) {
	now := time.Now().UTC()
	cache := NewReplayCache()
	first := "hmac-sha256:" + strings.Repeat("a", 64)
	second := "hmac-sha256:" + strings.Repeat("b", 64)
	if _, err := cache.Admit("truenas-node", 7, "peerreq-0001", first, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Admit("truenas-node", 7, "peerreq-0001", second, now.Add(time.Second), time.Minute); err == nil {
		t.Fatal("request_id reuse with different signed message unexpectedly accepted")
	}
}

func TestReplayCacheScopesIdentityByPeerAndGeneration(t *testing.T) {
	now := time.Now().UTC()
	cache := NewReplayCache()
	signature := "hmac-sha256:" + strings.Repeat("a", 64)
	if _, err := cache.Admit("truenas-node", 7, "peerreq-0001", signature, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if duplicate, err := cache.Admit("other-node", 7, "peerreq-0001", signature, now, time.Minute); err != nil || duplicate {
		t.Fatalf("other peer collided duplicate=%v err=%v", duplicate, err)
	}
	if duplicate, err := cache.Admit("truenas-node", 8, "peerreq-0001", signature, now, time.Minute); err != nil || duplicate {
		t.Fatalf("new generation collided duplicate=%v err=%v", duplicate, err)
	}
}

func TestReplayCacheIsCapacityBounded(t *testing.T) {
	now := time.Now().UTC()
	cache := NewReplayCache()
	signature := "hmac-sha256:" + strings.Repeat("a", 64)
	for i := 0; i < maxReplayEntries; i++ {
		requestID := fmt.Sprintf("peerreq-%04d", i)
		if _, err := cache.Admit("truenas-node", 7, requestID, signature, now, time.Minute); err != nil {
			t.Fatalf("fill entry %d: %v", i, err)
		}
	}
	if _, err := cache.Admit("truenas-node", 7, "peerreq-overflow", signature, now, time.Minute); err == nil {
		t.Fatal("replay cache accepted entry beyond capacity")
	}
}

func TestPeerEnvelopeRejectsWeakKey(t *testing.T) {
	now := time.Now().UTC()
	payload := []byte("{}")
	if _, err := SignPeerEnvelope(peerTestEnvelope(now, payload), []byte("short")); err == nil {
		t.Fatal("weak key unexpectedly admitted")
	}
}
