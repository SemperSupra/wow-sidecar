package node

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SemperSupra/wow-sidecar/go/capability"
)

func TestCapabilityIndexAdvertisesOnlyPublicTypedSensor(t *testing.T) {
	config := runtimeConfig(t)
	now := time.Date(2026, 10, 3, 22, 15, 0, 0, time.UTC)
	runtime, err := newRuntime(
		config,
		strings.Repeat("d", 40),
		"0.1.0-test",
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil)
	rec := httptest.NewRecorder()
	runtime.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("capability index returned %d: %s", rec.Code, rec.Body.String())
	}

	var value struct {
		Schema       string `json:"schema"`
		Capabilities []struct {
			Descriptor capability.Descriptor `json:"descriptor"`
			State      capability.State      `json:"state"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value.Schema != capabilityIndexSchema || len(value.Capabilities) != 1 {
		t.Fatalf("unexpected capability index: %#v", value)
	}
	item := value.Capabilities[0]
	if item.Descriptor.ID != "system.identity" ||
		item.Descriptor.Kind != capability.KindSensor ||
		item.Descriptor.Effect != capability.EffectReadOnly ||
		item.Descriptor.Disclosure != capability.DisclosurePublic ||
		item.Descriptor.AuthorityRequired {
		t.Fatalf("unexpected descriptor: %#v", item.Descriptor)
	}
	if item.State.CapabilityID != "system.identity" ||
		item.State.Readiness != capability.ReadinessReady ||
		item.State.ObservedAt.IsZero() {
		t.Fatalf("unexpected sensor readiness: %#v", item.State)
	}
}

func TestSystemIdentitySensorReturnsExactNonMutatingRuntimeFacts(t *testing.T) {
	config := runtimeConfig(t)
	now := time.Date(2026, 10, 3, 22, 15, 0, 0, time.UTC)
	source := strings.Repeat("e", 40)
	runtime, err := newRuntime(config, source, "0.1.0-test", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	card, err := runtime.Card()
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/capabilities/system.identity", nil)
	rec := httptest.NewRecorder()
	runtime.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("identity sensor returned %d: %s", rec.Code, rec.Body.String())
	}
	var outcome capability.Outcome
	if err := json.Unmarshal(rec.Body.Bytes(), &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.Result != "ELIGIBLE" ||
		outcome.Status != "system-identity-observed" ||
		outcome.MutationPerformed ||
		outcome.RetryAuthorized {
		t.Fatalf("unexpected identity outcome: %#v", outcome)
	}
	expected := map[string]any{
		"node_id":         card.NodeID,
		"generation":      float64(card.Generation),
		"incarnation_id":  card.IncarnationID,
		"locality":        card.Locality,
		"source_revision": source,
		"build_version":   "0.1.0-test",
	}
	for key, want := range expected {
		if got := outcome.Data[key]; got != want {
			t.Fatalf("identity field %s = %#v want %#v", key, got, want)
		}
	}
	raw := rec.Body.String()
	for _, forbidden := range []string{"private_key", "token", "password", "secret"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("identity sensor exposed secret-shaped field %q: %s", forbidden, raw)
		}
	}
}

func TestCapabilitySurfaceIsGETOnlyAndNotGenericInvocation(t *testing.T) {
	config := runtimeConfig(t)
	runtime, err := newRuntime(
		config,
		strings.Repeat("f", 40),
		"0.1.0-test",
		time.Now,
	)
	if err != nil {
		t.Fatal(err)
	}
	handler := runtime.Handler()

	for _, path := range []string{"/v1/capabilities", "/v1/capabilities/system.identity"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{\"command\":\"id\"}"))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodGet {
			t.Fatalf("POST %s not rejected: code=%d allow=%q", path, rec.Code, rec.Header().Get("Allow"))
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/capabilities/arbitrary.exec", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("generic capability route unexpectedly exists: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRuntimeRejectsCallerConfiguredCapabilityClaims(t *testing.T) {
	config := runtimeConfig(t)
	config.Capabilities = []string{"system.identity"}
	_, err := newRuntime(
		config,
		strings.Repeat("a", 40),
		"0.1.0-test",
		time.Now,
	)
	if err == nil || !strings.Contains(err.Error(), "product-owned") {
		t.Fatalf("caller-configured capability claim accepted: %v", err)
	}
}
