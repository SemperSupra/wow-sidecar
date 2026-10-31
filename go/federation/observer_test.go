package federation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

func TestObserverCurrentCardRequiresCurrentlyObservedBinding(t *testing.T) {
	now := time.Date(2026, 10, 4, 18, 30, 0, 0, time.UTC)
	source := &mutableCardServer{
		card: card("peer-node-01", 3, strings.Repeat("a", 32), now.Add(time.Minute)),
	}
	server := httptest.NewServer(source)
	defer server.Close()

	observer, err := NewObserver([]string{server.URL}, server.Client(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := observer.CurrentCard("peer-node-01"); ok {
		t.Fatal("unobserved peer unexpectedly returned a current card")
	}

	observer.PollOnce(context.Background())
	got, ok := observer.CurrentCard("peer-node-01")
	if !ok || got.NodeID != "peer-node-01" || got.Generation != 3 {
		t.Fatalf("current observed card unavailable: ok=%v card=%#v", ok, got)
	}
	guardCalls := 0
	if err := observer.WithCurrentCard("peer-node-01", func(card embodiment.Card) error {
		guardCalls++
		if card.NodeID != "peer-node-01" || card.Generation != 3 {
			t.Fatalf("guard returned unexpected card: %#v", card)
		}
		return nil
	}); err != nil {
		t.Fatalf("current-card guard rejected observed peer: %v", err)
	}
	if guardCalls != 1 {
		t.Fatalf("current-card guard calls=%d want=1", guardCalls)
	}

	source.oversize = true
	observer.PollOnce(context.Background())
	if _, ok := observer.CurrentCard("peer-node-01"); ok {
		t.Fatal("fetch-failed peer remained eligible as a current mutation peer")
	}
	guardCalls = 0
	if err := observer.WithCurrentCard("peer-node-01", func(embodiment.Card) error {
		guardCalls++
		return nil
	}); err == nil {
		t.Fatal("fetch-failed peer remained eligible for guarded mutation")
	}
	if guardCalls != 0 {
		t.Fatalf("guard callback ran for fetch-failed peer: %d", guardCalls)
	}
}

func TestObserverRejectsRedirectsWithDefaultAndCustomClients(t *testing.T) {
	var hits atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(card("redirected-node", 1, strings.Repeat("f", 32), time.Now().Add(time.Minute)))
	}))
	defer foreign.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/v1/card", http.StatusFound)
	}))
	defer source.Close()

	var redirectCallbacks atomic.Int32
	custom := &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		redirectCallbacks.Add(1)
		return nil
	}}
	for _, tc := range []struct {
		name   string
		client *http.Client
	}{
		{"default", nil}, {"custom_allow_redirects", custom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hits.Store(0)
			observer, err := NewObserver([]string{source.URL}, tc.client, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			observer.PollOnce(context.Background())
			view := observer.Snapshot()
			if hits.Load() != 0 {
				t.Errorf("redirect target contacted %d times", hits.Load())
			}
			if len(view.Peers) != 1 || view.Peers[0].Status != StatusFetchFailed || view.ObservedPeers != 0 {
				t.Errorf("redirect should be FETCH_FAILED: %#v", view)
			}
		})
	}
	if custom.CheckRedirect == nil || custom.Timeout != time.Second {
		t.Error("caller-owned client was modified")
	}
	before := redirectCallbacks.Load()
	if err := custom.CheckRedirect(nil, nil); err != nil || redirectCallbacks.Load() != before+1 {
		t.Error("caller-owned redirect callback was modified")
	}
}

func TestObserverRejectsAmbientProxyAndDialsDirectly(t *testing.T) {
	var proxyHits atomic.Int32
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		http.Error(w, "ambient proxy must not be contacted", http.StatusBadGateway)
	}))
	defer proxyServer.Close()

	var peerHits atomic.Int32
	peerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerHits.Add(1)
		_ = json.NewEncoder(w).Encode(card("direct-peer-node", 1, strings.Repeat("e", 32), time.Now().Add(time.Minute)))
	}))
	defer peerServer.Close()

	t.Setenv("HTTP_PROXY", proxyServer.URL)
	t.Setenv("http_proxy", proxyServer.URL)

	custom := &http.Client{Timeout: time.Second}
	for _, tc := range []struct {
		name   string
		client *http.Client
	}{
		{"default", nil},
		{"custom_default_transport", custom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxyHits.Store(0)
			peerHits.Store(0)
			observer, err := NewObserver([]string{peerServer.URL}, tc.client, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			if tr, ok := observer.client.Transport.(*http.Transport); !ok || tr.Proxy != nil {
				t.Errorf("observer transport Proxy is not nil: %v", observer.client.Transport)
			}
			observer.PollOnce(context.Background())
			view := observer.Snapshot()
			if proxyHits.Load() != 0 {
				t.Errorf("ambient proxy was contacted %d times", proxyHits.Load())
			}
			if peerHits.Load() != 1 {
				t.Errorf("direct peer was contacted %d times, want 1", peerHits.Load())
			}
			if len(view.Peers) != 1 || view.Peers[0].Status != StatusObserved || view.ObservedPeers != 1 {
				t.Errorf("peer should be OBSERVED: %#v", view)
			}
		})
	}
	if custom.Transport != nil {
		t.Error("caller-owned client Transport was mutated")
	}

	callerTransport := &http.Transport{}
	callerClient := &http.Client{Transport: callerTransport}
	obsWithTransport, err := NewObserver([]string{peerServer.URL}, callerClient, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if obsWithTransport.client.Transport != callerTransport {
		t.Error("caller-supplied transport was not preserved")
	}
}
