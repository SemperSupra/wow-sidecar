package federation

import (
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

func peerTestEnvelope(now time.Time, payload []byte) PeerEnvelope {
	return PeerEnvelope{
		Schema:              PeerEnvelopeSchema,
		SourceNodeID:        "truenas-node",
		SourceGeneration:    7,
		SourceIncarnationID: "0123456789abcdef0123456789abcdef",
		DestinationNodeID:   "oci-edge-node",
		RequestID:           "peerreq-0001",
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
	if err := VerifyPeerEnvelope(env, key, "oci-edge-node", peerTestCard(now), now, 2*time.Minute, payload); err != nil {
		t.Fatalf("verify: %v", err)
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
		{"request", func(e *PeerEnvelope) { e.RequestID = "peerreq-9999" }},
		{"operation", func(e *PeerEnvelope) { e.Operation = "rendezvous.complete" }},
		{"digest", func(e *PeerEnvelope) { e.PayloadDigest = PayloadDigest([]byte("other")) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			changed := env
			tc.edit(&changed)
			if err := VerifyPeerEnvelope(changed, key, "oci-edge-node", peerTestCard(now), now, 2*time.Minute, payload); err == nil {
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
	if err := VerifyPeerEnvelope(env, []byte("abcdef0123456789abcdef0123456789"), "oci-edge-node", peerTestCard(now), now, 2*time.Minute, payload); err == nil {
		t.Fatal("wrong key unexpectedly verified")
	}
	if err := VerifyPeerEnvelope(env, key, "oci-edge-node", peerTestCard(now), now, 2*time.Minute, []byte("changed")); err == nil {
		t.Fatal("changed payload unexpectedly verified")
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
	if err := VerifyPeerEnvelope(env, key, "oci-edge-node", peerTestCard(now), now, time.Minute, payload); err == nil {
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

func TestReplayCacheRejectsDuplicateAndExpires(t *testing.T) {
	now := time.Now().UTC()
	cache := NewReplayCache()
	if err := cache.Accept("peerreq-0001", now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := cache.Accept("peerreq-0001", now.Add(10*time.Second), time.Minute); err == nil {
		t.Fatal("duplicate request unexpectedly accepted")
	}
	if err := cache.Accept("peerreq-0001", now.Add(61*time.Second), time.Minute); err != nil {
		t.Fatalf("expired replay entry not reusable: %v", err)
	}
}

func TestPeerEnvelopeRejectsWeakKey(t *testing.T) {
	now := time.Now().UTC()
	payload := []byte("{}")
	if _, err := SignPeerEnvelope(peerTestEnvelope(now, payload), []byte("short")); err == nil {
		t.Fatal("weak key unexpectedly admitted")
	}
}
