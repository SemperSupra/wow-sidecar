package federation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SemperSupra/wow-sidecar/go/embodiment"
)

type mutableCardServer struct {
	mu       sync.Mutex
	card     embodiment.Card
	oversize bool
	status   int
}

func (s *mutableCardServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/card" || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status != 0 && s.status != http.StatusOK {
		w.WriteHeader(s.status)
		return
	}
	if s.oversize {
		_, _ = w.Write([]byte(strings.Repeat("x", maxCardBytes+1)))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.card)
}

func (s *mutableCardServer) setCard(card embodiment.Card) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.card = card
}

func card(node string, generation uint64, incarnation string, lease time.Time) embodiment.Card {
	return embodiment.Card{
		Schema:         embodiment.CardSchema,
		Protocol:       embodiment.ProtocolVersion,
		NodeID:         node,
		Generation:     generation,
		IncarnationID:  incarnation,
		Locality:       "cloud",
		Endpoints:      []embodiment.Endpoint{},
		Capabilities:   []string{},
		LeaseExpiresAt: lease.UTC().Format(time.RFC3339Nano),
	}
}

func TestParsePeerURLsIsBoundedAndStrict(t *testing.T) {
	got, err := ParsePeerURLs("https://b.example,http://127.0.0.1:8080/")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "http://127.0.0.1:8080" || got[1] != "https://b.example" {
		t.Fatalf("unexpected normalized peers: %#v", got)
	}

	invalid := []string{
		"ftp://peer.example",
		"https://user:pass@peer.example",
		"https://peer.example/path",
		"https://peer.example?x=1",
		"https://peer.example#fragment",
		" https://peer.example",
		"https://peer.example,https://peer.example/",
	}
	for _, raw := range invalid {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParsePeerURLs(raw); err == nil {
				t.Fatalf("invalid peer URL set accepted: %q", raw)
			}
		})
	}

	tooMany := make([]string, maxPeers+1)
	for i := range tooMany {
		tooMany[i] = "https://peer-" + string(rune('a'+i)) + ".example"
	}
	if _, err := ParsePeerURLs(strings.Join(tooMany, ",")); err == nil {
		t.Fatal("peer ceiling was not enforced")
	}
}

func TestObserverTracksReachabilityLeaseAndHidesEndpoint(t *testing.T) {
	now := time.Date(2026, 10, 3, 21, 0, 0, 0, time.UTC)
	source := &mutableCardServer{
		card: card("cloud-node-01", 1, strings.Repeat("a", 32), now.Add(30*time.Second)),
	}
	server := httptest.NewServer(source)

	observer, err := NewObserver([]string{server.URL}, server.Client(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	observer.PollOnce(context.Background())
	first := observer.Snapshot()
	if first.ConfiguredPeers != 1 || first.ObservedPeers != 1 || len(first.Peers) != 1 {
		t.Fatalf("unexpected first snapshot: %#v", first)
	}
	peer := first.Peers[0]
	if peer.NodeID != "cloud-node-01" || peer.Presence != embodiment.PresenceOnline ||
		peer.Reachability != embodiment.Reachable || peer.Status != StatusObserved {
		t.Fatalf("unexpected observed peer: %#v", peer)
	}
	raw, _ := json.Marshal(first)
	if strings.Contains(string(raw), server.URL) {
		t.Fatal("snapshot leaked raw configured peer URL")
	}

	server.Close()
	now = now.Add(5 * time.Second)
	observer.PollOnce(context.Background())
	degraded := observer.Snapshot().Peers[0]
	if degraded.Status != StatusFetchFailed || degraded.Reachability != embodiment.Unreachable ||
		degraded.Presence != embodiment.PresenceDegraded {
		t.Fatalf("fetch failure did not degrade reachable lease: %#v", degraded)
	}

	now = now.Add(30 * time.Second)
	offline := observer.Snapshot().Peers[0]
	if offline.Presence != embodiment.PresenceOffline {
		t.Fatalf("expired lease did not become offline: %#v", offline)
	}
}

func TestObserverFencesIdentityGenerationAndIncarnation(t *testing.T) {
	now := time.Date(2026, 10, 3, 21, 0, 0, 0, time.UTC)
	source := &mutableCardServer{
		card: card("sovereign-node-01", 2, strings.Repeat("a", 32), now.Add(time.Minute)),
	}
	server := httptest.NewServer(source)
	defer server.Close()

	observer, err := NewObserver([]string{server.URL}, server.Client(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	observer.PollOnce(context.Background())

	source.setCard(card("sovereign-node-01", 1, strings.Repeat("a", 32), now.Add(2*time.Minute)))
	observer.PollOnce(context.Background())
	stale := observer.Snapshot().Peers[0]
	if stale.Status != StatusCardRejected || stale.Presence != embodiment.PresenceUnknown {
		t.Fatalf("lower generation was not fenced: %#v", stale)
	}

	source.setCard(card("sovereign-node-01", 2, strings.Repeat("b", 32), now.Add(2*time.Minute)))
	observer.PollOnce(context.Background())
	collision := observer.Snapshot().Peers[0]
	if collision.Status != StatusCardRejected || collision.Presence != embodiment.PresenceUnknown {
		t.Fatalf("generation collision was not fenced: %#v", collision)
	}

	source.setCard(card("sovereign-node-01", 3, strings.Repeat("c", 32), now.Add(2*time.Minute)))
	observer.PollOnce(context.Background())
	restarted := observer.Snapshot().Peers[0]
	if restarted.Status != StatusObserved || restarted.Generation != 3 ||
		restarted.IncarnationID != strings.Repeat("c", 32) {
		t.Fatalf("higher generation did not supersede: %#v", restarted)
	}

	source.setCard(card("replacement-node-01", 4, strings.Repeat("d", 32), now.Add(2*time.Minute)))
	observer.PollOnce(context.Background())
	mismatch := observer.Snapshot().Peers[0]
	if mismatch.Status != StatusIdentityMismatch || mismatch.NodeID != "sovereign-node-01" ||
		mismatch.Presence != embodiment.PresenceUnknown {
		t.Fatalf("endpoint identity change was not fenced: %#v", mismatch)
	}
}

func TestObserverRejectsOversizeAndDuplicateNodeBinding(t *testing.T) {
	now := time.Date(2026, 10, 3, 21, 0, 0, 0, time.UTC)
	oversize := &mutableCardServer{oversize: true}
	oversizeServer := httptest.NewServer(oversize)
	defer oversizeServer.Close()

	observer, err := NewObserver([]string{oversizeServer.URL}, oversizeServer.Client(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	observer.PollOnce(context.Background())
	if got := observer.Snapshot().Peers[0]; got.Status != StatusFetchFailed || got.NodeID != "" {
		t.Fatalf("oversize card was not rejected before identity binding: %#v", got)
	}

	shared := card("same-node-01", 1, strings.Repeat("e", 32), now.Add(time.Minute))
	a := httptest.NewServer(&mutableCardServer{card: shared})
	defer a.Close()
	b := httptest.NewServer(&mutableCardServer{card: shared})
	defer b.Close()

	observer, err = NewObserver([]string{a.URL, b.URL}, http.DefaultClient, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	observer.PollOnce(context.Background())
	snapshot := observer.Snapshot()
	if snapshot.ObservedPeers != 1 {
		t.Fatalf("same node identity bound to multiple endpoints: %#v", snapshot)
	}
	statuses := map[string]bool{}
	for _, peer := range snapshot.Peers {
		statuses[peer.Status] = true
	}
	if !statuses[StatusObserved] || !statuses[StatusNodeAlreadyBound] {
		t.Fatalf("duplicate node binding did not fail closed: %#v", snapshot)
	}
}
