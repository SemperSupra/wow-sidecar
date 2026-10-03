package node

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runtimeConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		NodeID: "test-node-01",
		Locality: "sovereign",
		GenerationStateFile: filepath.Join(t.TempDir(), "generation.json"),
		ListenAddr: "127.0.0.1:8080",
		LeaseTTL: 30 * time.Second,
		PublicEndpoint: "https://node.invalid/wow",
		EndpointKind: "https",
		EndpointAuth: "overlay",
		Capabilities: []string{"system.identity"},
	}
}

func TestRuntimeAllocatesNewGenerationAndIncarnationPerStart(t *testing.T) {
	config := runtimeConfig(t)
	now := time.Date(2026, 10, 3, 22, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	first, err := newRuntime(config, strings.Repeat("a", 40), "0.1.0-test", clock)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newRuntime(config, strings.Repeat("a", 40), "0.1.0-test", clock)
	if err != nil {
		t.Fatal(err)
	}
	firstCard, err := first.Card()
	if err != nil {
		t.Fatal(err)
	}
	secondCard, err := second.Card()
	if err != nil {
		t.Fatal(err)
	}
	if firstCard.Generation != 1 || secondCard.Generation != 2 {
		t.Fatalf("unexpected generations: %d %d", firstCard.Generation, secondCard.Generation)
	}
	if firstCard.IncarnationID == secondCard.IncarnationID {
		t.Fatal("separate runtime starts reused incarnation identity")
	}
	if firstCard.LeaseExpiresAt != "2026-10-03T22:00:30Z" {
		t.Fatalf("unexpected lease: %s", firstCard.LeaseExpiresAt)
	}
	if len(firstCard.Capabilities) != 1 || firstCard.Capabilities[0] != "system.identity" {
		t.Fatalf("unexpected capabilities: %#v", firstCard.Capabilities)
	}
}

func TestRuntimeRejectsNonExactSourceRevisionBeforeAllocation(t *testing.T) {
	config := runtimeConfig(t)
	if _, err := newRuntime(config, "latest", "test", time.Now); err == nil || !strings.Contains(err.Error(), "40-hex") {
		t.Fatalf("non-exact source revision accepted: %v", err)
	}
}

func TestRuntimeCardLeaseRenewsFromCurrentClock(t *testing.T) {
	config := runtimeConfig(t)
	now := time.Date(2026, 10, 3, 22, 0, 0, 0, time.UTC)
	runtime, err := newRuntime(
		config,
		strings.Repeat("b", 40),
		"0.1.0-test",
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := runtime.Card()
	now = now.Add(15 * time.Second)
	second, _ := runtime.Card()
	if first.LeaseExpiresAt == second.LeaseExpiresAt {
		t.Fatal("lease did not renew from current clock")
	}
	if second.Generation != first.Generation || second.IncarnationID != first.IncarnationID {
		t.Fatal("lease renewal changed incarnation identity")
	}
}

func TestHealthReadyAndCardEndpointsAreMinimalGETSurfaces(t *testing.T) {
	config := runtimeConfig(t)
	now := time.Date(2026, 10, 3, 22, 0, 0, 0, time.UTC)
	runtime, err := newRuntime(
		config,
		strings.Repeat("c", 40),
		"0.1.0-test",
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}
	handler := runtime.Handler()

	for _, path := range []string{"/healthz", "/readyz", "/v1/card"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s returned %d: %s", path, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("%s missing JSON content type", path)
		}
		var value map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &value); err != nil {
			t.Fatalf("%s invalid JSON: %v", path, err)
		}
		if _, exists := value["private_key"]; exists {
			t.Fatalf("%s exposed secret-shaped field", path)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/card", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("POST /v1/card not rejected correctly: code=%d allow=%q", rec.Code, rec.Header().Get("Allow"))
	}
}

func TestConfigFromEnvIsLoopbackByDefaultAndDoesNotSelfAssertCapabilities(t *testing.T) {
	t.Setenv("WOW_NODE_ID", "cloud-node-01")
	t.Setenv("WOW_LOCALITY", "cloud")
	t.Setenv("WOW_GENERATION_STATE_FILE", "")
	t.Setenv("WOW_LISTEN_ADDR", "")
	t.Setenv("WOW_LEASE_SECONDS", "")
	t.Setenv("WOW_PUBLIC_ENDPOINT", "")
	t.Setenv("WOW_ENDPOINT_KIND", "")
	t.Setenv("WOW_ENDPOINT_AUTH", "")

	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.ListenAddr != defaultListenAddr || config.GenerationStateFile != defaultGenerationStateFile {
		t.Fatalf("unexpected defaults: %#v", config)
	}
	if len(config.Capabilities) != 0 {
		t.Fatalf("environment unexpectedly self-asserted capabilities: %#v", config.Capabilities)
	}
}

func TestConfigRequiresExplicitEndpointContext(t *testing.T) {
	t.Setenv("WOW_NODE_ID", "cloud-node-01")
	t.Setenv("WOW_LOCALITY", "cloud")
	t.Setenv("WOW_PUBLIC_ENDPOINT", "")
	t.Setenv("WOW_ENDPOINT_KIND", "https")
	t.Setenv("WOW_ENDPOINT_AUTH", "")
	if _, err := ConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "require WOW_PUBLIC_ENDPOINT") {
		t.Fatalf("orphan endpoint metadata accepted: %v", err)
	}
}
