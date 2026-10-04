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

func clearPeerRendezvousRuntimeEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"WOW_PEER_RENDEZVOUS_ENABLED",
		"WOW_PEER_CREDENTIALS_FILE",
		"WOW_PEER_MAX_SKEW_SECONDS",
		"WOW_PEER_REPLAY_TTL_SECONDS",
	} {
		t.Setenv(key, "")
	}
}

func writePeerRuntimeCredentials(t *testing.T, nodeID string, key []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "peer-credentials.json")
	raw := []byte(`{"schema":"` + federation.PeerCredentialsSchema +
		`","peers":[{"node_id":"` + nodeID + `","key":"` +
		base64.StdEncoding.EncodeToString(key) + `"}]}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPeerRendezvousRuntimeConfigIsExplicitAndBounded(t *testing.T) {
	clearPeerRendezvousRuntimeEnv(t)
	cfg, err := peerRendezvousRuntimeConfigFromEnv()
	if err != nil || cfg != nil {
		t.Fatalf("disabled peer rendezvous config: cfg=%#v err=%v", cfg, err)
	}

	t.Setenv("WOW_PEER_CREDENTIALS_FILE", "/run/secrets/wow-peer-credentials.json")
	if _, err := peerRendezvousRuntimeConfigFromEnv(); err == nil {
		t.Fatal("credential path without explicit enable was accepted")
	}

	clearPeerRendezvousRuntimeEnv(t)
	t.Setenv("WOW_PEER_RENDEZVOUS_ENABLED", "true")
	t.Setenv("WOW_PEER_CREDENTIALS_FILE", "/run/secrets/wow-peer-credentials.json")
	t.Setenv("WOW_PEER_MAX_SKEW_SECONDS", "90")
	t.Setenv("WOW_PEER_REPLAY_TTL_SECONDS", "420")
	cfg, err = peerRendezvousRuntimeConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil || cfg.CredentialsFile != "/run/secrets/wow-peer-credentials.json" ||
		cfg.MaxSkew != 90*time.Second || cfg.ReplayTTL != 420*time.Second {
		t.Fatalf("unexpected peer rendezvous config: %#v", cfg)
	}

	for _, tc := range []struct {
		name string
		key  string
		val  string
	}{
		{"skew-low", "WOW_PEER_MAX_SKEW_SECONDS", "0"},
		{"skew-high", "WOW_PEER_MAX_SKEW_SECONDS", "601"},
		{"ttl-low", "WOW_PEER_REPLAY_TTL_SECONDS", "0"},
		{"ttl-high", "WOW_PEER_REPLAY_TTL_SECONDS", "1801"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearPeerRendezvousRuntimeEnv(t)
			t.Setenv("WOW_PEER_RENDEZVOUS_ENABLED", "true")
			t.Setenv("WOW_PEER_CREDENTIALS_FILE", "/run/secrets/wow-peer-credentials.json")
			t.Setenv(tc.key, tc.val)
			if _, err := peerRendezvousRuntimeConfigFromEnv(); err == nil {
				t.Fatalf("%s=%s unexpectedly accepted", tc.key, tc.val)
			}
		})
	}
}

func TestPeerRendezvousRuntimePrerequisitesFailClosed(t *testing.T) {
	key := []byte("peer-runtime-key-material-32bytes!!")
	credentialPath := writePeerRuntimeCredentials(t, "peer-source-01", key)
	credentials, err := federation.LoadPeerCredentials(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &PeerRendezvousRuntimeConfig{
		CredentialsFile: credentialPath,
		MaxSkew:         2 * time.Minute,
		ReplayTTL:       5 * time.Minute,
	}
	verify := func(context.Context, string, string, string) error { return nil }

	base := runtimeConfig(t)
	runtime, err := newRuntime(base, strings.Repeat("a", 40), "g611e-test", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.configurePeerRendezvous(cfg, credentials, verify); err == nil ||
		!strings.Contains(err.Error(), "local rendezvous") {
		t.Fatalf("missing rendezvous prerequisite accepted: %v", err)
	}

	base = runtimeConfig(t)
	base.Rendezvous = &RendezvousConfig{
		WorksetRef:   "github-file:SemperSupra/example@main:workset.json",
		DelegationID: "runtime-test",
		Lease:        time.Minute,
	}
	runtime, err = newRuntime(base, strings.Repeat("b", 40), "g611e-test", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.configurePeerRendezvous(cfg, credentials, verify); err == nil ||
		!strings.Contains(err.Error(), "configured peer") {
		t.Fatalf("missing peer prerequisite accepted: %v", err)
	}

	clearPeerRendezvousRuntimeEnv(t)
	t.Setenv("WOW_PEER_RENDEZVOUS_ENABLED", "true")
	t.Setenv("WOW_PEER_CREDENTIALS_FILE", credentialPath)
	if err := runtime.ConfigurePeerRendezvousFromEnv(); err == nil ||
		!strings.Contains(err.Error(), "durable control") {
		t.Fatalf("missing durable control prerequisite accepted: %v", err)
	}
}

func TestRuntimePeerRendezvousRouteUsesObservedPeerAndLocalDestination(t *testing.T) {
	now := time.Date(2026, 10, 4, 18, 45, 0, 0, time.UTC)
	key := []byte("0123456789abcdef0123456789abcdef")
	sourceCard := embodiment.Card{
		Schema:         embodiment.CardSchema,
		Protocol:       embodiment.ProtocolVersion,
		NodeID:         "peer-source-01",
		Generation:     7,
		IncarnationID:  "11111111111111111111111111111111",
		Locality:       "sovereign",
		Endpoints:      []embodiment.Endpoint{},
		Capabilities:   []string{},
		LeaseExpiresAt: now.Add(5 * time.Minute).Format(time.RFC3339Nano),
	}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/card" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(sourceCard)
	}))
	defer source.Close()

	config := Config{
		NodeID:              "peer-dest-01",
		Locality:            "cloud",
		GenerationStateFile: filepath.Join(t.TempDir(), "generation.json"),
		ListenAddr:          "127.0.0.1:0",
		LeaseTTL:            time.Minute,
		PeerURLs:            []string{source.URL},
		PeerPollInterval:    15 * time.Second,
		Rendezvous: &RendezvousConfig{
			WorksetRef:   "github-file:SemperSupra/example@main:workset.json",
			DelegationID: "runtime-peer-test",
			Lease:        time.Minute,
		},
	}
	runtime, err := newRuntime(
		config,
		strings.Repeat("c", 40),
		"g611e-test",
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime.peers.PollOnce(context.Background())
	if _, ok := runtime.peers.CurrentCard(sourceCard.NodeID); !ok {
		t.Fatal("source peer was not admitted by observer")
	}

	credentialPath := writePeerRuntimeCredentials(t, sourceCard.NodeID, key)
	credentials, err := federation.LoadPeerCredentials(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	authorityCalls := 0
	if err := runtime.configurePeerRendezvous(
		&PeerRendezvousRuntimeConfig{
			CredentialsFile: credentialPath,
			MaxSkew:         2 * time.Minute,
			ReplayTTL:       5 * time.Minute,
		},
		credentials,
		func(_ context.Context, record, revision, state string) error {
			authorityCalls++
			if record != peerTestAuthorityRef || revision != peerTestAuthorityRev || state != "open" {
				return context.Canceled
			}
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	if !runtime.HasPeerRendezvous() {
		t.Fatal("peer rendezvous route was not configured")
	}

	offerCtx := context.WithValue(
		context.Background(),
		rendezvousPrincipalContextKey{},
		rendezvous.Principal{
			ClientID: "durable-control",
			Subject:  peerTestAuthorityRef + "@" + peerTestAuthorityRev,
		},
	)
	offered, err := runtime.rendezvous.store.Offer(
		offerCtx,
		config.Rendezvous.WorksetRef,
		config.Rendezvous.DelegationID,
		"",
	)
	if err != nil {
		t.Fatal(err)
	}

	payload := json.RawMessage(`{"operation":"claim","handoff_id":"` +
		offered.Handoff.ID + `","launch_id":"launch-runtime"}`)
	destinationCard, err := runtime.Card()
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := federation.SignPeerEnvelope(federation.PeerEnvelope{
		Schema:                   federation.PeerEnvelopeSchema,
		SourceNodeID:             sourceCard.NodeID,
		SourceGeneration:         sourceCard.Generation,
		SourceIncarnationID:      sourceCard.IncarnationID,
		DestinationNodeID:        destinationCard.NodeID,
		DestinationGeneration:    destinationCard.Generation,
		DestinationIncarnationID: destinationCard.IncarnationID,
		RequestID:                "peerreq-runtime-01",
		IssuedAt:                 now.Format(time.RFC3339Nano),
		Operation:                "rendezvous.claim",
		PayloadDigest:            federation.PayloadDigest(payload),
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(peerRendezvousRequest{
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

	request := httptest.NewRequest(http.MethodPost, "/v1/peer/rendezvous", strings.NewReader(string(wire)))
	response := httptest.NewRecorder()
	runtime.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("runtime peer claim status=%d body=%q", response.Code, response.Body.String())
	}
	var decoded peerRendezvousResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Status != "rendezvous-claimed" || decoded.Duplicate || !decoded.MutationPerformed {
		t.Fatalf("unexpected runtime peer claim: %#v", decoded)
	}
	if authorityCalls != 1 {
		t.Fatalf("canonical authority calls=%d want=1", authorityCalls)
	}

	duplicate := httptest.NewRecorder()
	runtime.Handler().ServeHTTP(
		duplicate,
		httptest.NewRequest(http.MethodPost, "/v1/peer/rendezvous", strings.NewReader(string(wire))),
	)
	if duplicate.Code != http.StatusOK {
		t.Fatalf("duplicate status=%d body=%q", duplicate.Code, duplicate.Body.String())
	}
	if err := json.Unmarshal(duplicate.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Duplicate || decoded.MutationPerformed || decoded.Status != "rendezvous-existing-claim" {
		t.Fatalf("duplicate runtime request was not idempotent: %#v", decoded)
	}
}

func TestRuntimePeerRendezvousRejectsOldDestinationBeforeAuthority(t *testing.T) {
	now := time.Date(2026, 10, 4, 18, 50, 0, 0, time.UTC)
	key := []byte("0123456789abcdef0123456789abcdef")
	sourceCard := embodiment.Card{
		Schema:         embodiment.CardSchema,
		Protocol:       embodiment.ProtocolVersion,
		NodeID:         "peer-source-02",
		Generation:     4,
		IncarnationID:  "22222222222222222222222222222222",
		Locality:       "sovereign",
		Endpoints:      []embodiment.Endpoint{},
		Capabilities:   []string{},
		LeaseExpiresAt: now.Add(5 * time.Minute).Format(time.RFC3339Nano),
	}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(sourceCard)
	}))
	defer source.Close()

	config := Config{
		NodeID:              "peer-dest-02",
		Locality:            "cloud",
		GenerationStateFile: filepath.Join(t.TempDir(), "generation.json"),
		ListenAddr:          "127.0.0.1:0",
		LeaseTTL:            time.Minute,
		PeerURLs:            []string{source.URL},
		PeerPollInterval:    15 * time.Second,
		Rendezvous: &RendezvousConfig{
			WorksetRef:   "github-file:SemperSupra/example@main:workset.json",
			DelegationID: "runtime-peer-test-2",
			Lease:        time.Minute,
		},
	}
	runtime, err := newRuntime(config, strings.Repeat("d", 40), "g611e-test", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	runtime.peers.PollOnce(context.Background())
	path := writePeerRuntimeCredentials(t, sourceCard.NodeID, key)
	credentials, err := federation.LoadPeerCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	authorityCalls := 0
	if err := runtime.configurePeerRendezvous(
		&PeerRendezvousRuntimeConfig{CredentialsFile: path, MaxSkew: 2 * time.Minute, ReplayTTL: 5 * time.Minute},
		credentials,
		func(context.Context, string, string, string) error {
			authorityCalls++
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}

	current, err := runtime.Card()
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"operation":"claim","handoff_id":"handoff_invalid","launch_id":"launch-runtime"}`)
	envelope, err := federation.SignPeerEnvelope(federation.PeerEnvelope{
		Schema:                   federation.PeerEnvelopeSchema,
		SourceNodeID:             sourceCard.NodeID,
		SourceGeneration:         sourceCard.Generation,
		SourceIncarnationID:      sourceCard.IncarnationID,
		DestinationNodeID:        current.NodeID,
		DestinationGeneration:    current.Generation + 1,
		DestinationIncarnationID: "33333333333333333333333333333333",
		RequestID:                "peerreq-runtime-02",
		IssuedAt:                 now.Format(time.RFC3339Nano),
		Operation:                "rendezvous.claim",
		PayloadDigest:            federation.PayloadDigest(payload),
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(peerRendezvousRequest{
		Envelope: envelope,
		Authority: peerRendezvousAuthority{
			Record: peerTestAuthorityRef, Revision: peerTestAuthorityRev, State: "open",
		},
		Payload: payload,
	})
	response := httptest.NewRecorder()
	runtime.Handler().ServeHTTP(
		response,
		httptest.NewRequest(http.MethodPost, "/v1/peer/rendezvous", strings.NewReader(string(wire))),
	)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("stale destination status=%d body=%q", response.Code, response.Body.String())
	}
	if authorityCalls != 0 {
		t.Fatalf("authority called before destination fencing: %d", authorityCalls)
	}
}
