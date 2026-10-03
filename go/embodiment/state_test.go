package embodiment

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testCard() Card {
	return Card{
		Schema:        CardSchema,
		Protocol:      ProtocolVersion,
		NodeID:        "truenas-sovereign-01",
		Generation:    7,
		IncarnationID: "00112233445566778899aabbccddeeff",
		Locality:      "sovereign",
		Endpoints: []Endpoint{
			{Kind: "https", Address: "https://node.invalid/wow", Auth: "overlay"},
		},
		Capabilities: []string{"garm.observe", "truenas.observe"},
		LeaseExpiresAt: "2026-10-03T22:30:00Z",
	}
}

func TestCardValidationAndDigest(t *testing.T) {
	card := testCard()
	if err := card.Validate(); err != nil {
		t.Fatal(err)
	}
	first, err := card.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	second, err := card.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !strings.HasPrefix(first, "sha256:") || len(first) != 71 {
		t.Fatalf("unexpected digest: %q / %q", first, second)
	}

	unsorted := card
	unsorted.Capabilities = []string{"truenas.observe", "garm.observe"}
	if err := unsorted.Validate(); err == nil {
		t.Fatal("unsorted capability card was accepted")
	}
}

func TestDecodeCardRejectsUnknownFields(t *testing.T) {
	card := testCard()
	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	value["task_registry"] = map[string]any{}
	raw, _ = json.Marshal(value)
	if _, err := DecodeCard(raw); err == nil {
		t.Fatal("unknown field was accepted")
	}
}

func TestGenerationFencingAndCollision(t *testing.T) {
	tracker := NewTracker()
	now := time.Date(2026, 10, 3, 22, 0, 0, 0, time.UTC)

	card := testCard()
	first, err := tracker.Observe(card, now, Reachable)
	if err != nil {
		t.Fatal(err)
	}
	if first.Superseded || first.Duplicate {
		t.Fatalf("unexpected first observation: %#v", first)
	}

	duplicate, err := tracker.Observe(card, now.Add(time.Second), Reachable)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Duplicate {
		t.Fatal("same generation/incarnation/lease was not classified duplicate")
	}

	newer := card
	newer.Generation++
	newer.IncarnationID = "ffeeddccbbaa99887766554433221100"
	newer.LeaseExpiresAt = "2026-10-03T22:45:00Z"
	superseded, err := tracker.Observe(newer, now.Add(2*time.Second), Unknown)
	if err != nil {
		t.Fatal(err)
	}
	if !superseded.Superseded {
		t.Fatal("newer generation did not supersede old generation")
	}

	if _, err := tracker.Observe(card, now.Add(3*time.Second), Reachable); err == nil || !strings.Contains(err.Error(), "stale generation") {
		t.Fatalf("old generation was not fenced: %v", err)
	}

	collision := newer
	collision.IncarnationID = "1234567890abcdef1234567890abcdef"
	if _, err := tracker.Observe(collision, now.Add(4*time.Second), Reachable); err == nil || !strings.Contains(err.Error(), "generation collision") {
		t.Fatalf("split-brain generation collision was not rejected: %v", err)
	}
}

func TestLeaseAndReachabilityAreIndependent(t *testing.T) {
	tracker := NewTracker()
	now := time.Date(2026, 10, 3, 22, 0, 0, 0, time.UTC)
	card := testCard()
	card.LeaseExpiresAt = now.Add(10 * time.Second).Format(time.RFC3339Nano)

	result, err := tracker.Observe(card, now, Unreachable)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.State.PresenceAt(now); got != PresenceDegraded {
		t.Fatalf("got %s want %s", got, PresenceDegraded)
	}
	if got := result.State.PresenceAt(now.Add(11 * time.Second)); got != PresenceOffline {
		t.Fatalf("got %s want %s", got, PresenceOffline)
	}

	state, err := tracker.SetReachability(card.NodeID, Reachable, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if got := state.PresenceAt(now.Add(time.Second)); got != PresenceOnline {
		t.Fatalf("got %s want %s", got, PresenceOnline)
	}
}

func TestLeaseReplayCannotShortenCurrentLease(t *testing.T) {
	tracker := NewTracker()
	now := time.Date(2026, 10, 3, 22, 0, 0, 0, time.UTC)
	card := testCard()
	card.LeaseExpiresAt = now.Add(time.Minute).Format(time.RFC3339Nano)
	if _, err := tracker.Observe(card, now, Reachable); err != nil {
		t.Fatal(err)
	}

	replayed := card
	replayed.LeaseExpiresAt = now.Add(30 * time.Second).Format(time.RFC3339Nano)
	if _, err := tracker.Observe(replayed, now.Add(time.Second), Reachable); err == nil || !strings.Contains(err.Error(), "stale lease") {
		t.Fatalf("stale lease replay was not rejected: %v", err)
	}
}

func TestGenerationAndIncarnationHelpers(t *testing.T) {
	next, err := NextGeneration(41)
	if err != nil || next != 42 {
		t.Fatalf("next generation: %d %v", next, err)
	}
	id, err := NewIncarnationIDFrom(bytes.NewReader(make([]byte, 16)))
	if err != nil {
		t.Fatal(err)
	}
	if id != strings.Repeat("0", 32) {
		t.Fatalf("unexpected deterministic incarnation: %q", id)
	}
}
