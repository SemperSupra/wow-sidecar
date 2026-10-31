package node

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestReplacementGenerationDoesNotRetirePriorRuntime documents the exact
// observable generation-lifetime gap at the source boundary. It exercises
// identity/readiness only: NO peer credential, control, claim, or mutation.
//
// Generation-state flock serializes increments but is released by
// allocateGeneration. A newer runtime can be constructed while an earlier
// runtime's HTTP handler still serves the older identity.
func TestReplacementGenerationDoesNotRetirePriorRuntime(t *testing.T) {
	config := runtimeConfig(t)
	now := time.Date(2026, 10, 31, 19, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	first, err := newRuntime(config, strings.Repeat("a", 40), "overlap-test", clock)
	if err != nil {
		t.Fatal(err)
	}
	// Keep an independent reference to the handler that would already be
	// mounted on the old process before allocating the new generation.
	oldHandler := first.Handler()
	oldBefore := httptest.NewRecorder()
	oldHandler.ServeHTTP(oldBefore, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if oldBefore.Code != http.StatusOK {
		t.Fatalf("old instance not ready before replacement: %d", oldBefore.Code)
	}

	second, err := newRuntime(config, strings.Repeat("a", 40), "overlap-test", clock)
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
		t.Fatalf("expected old/new generations 1/2; got %d/%d",
			firstCard.Generation, secondCard.Generation)
	}
	if firstCard.IncarnationID == secondCard.IncarnationID {
		t.Fatal("distinct runtime generations reused incarnation")
	}

	// This remains 200 after generation 2 exists. That is an *observability
	// proof*, not an exploit and not an assertion that peer mutation succeeds.
	oldAfter := httptest.NewRecorder()
	oldHandler.ServeHTTP(oldAfter, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if oldAfter.Code != http.StatusOK {
		t.Fatalf("old instance unexpectedly stopped serving: %d", oldAfter.Code)
	}
	var ready struct {
		Status string `json:"status"`
		NodeID string `json:"node_id"`
		Generation uint64 `json:"generation"`
	}
	if err := json.Unmarshal(oldAfter.Body.Bytes(), &ready); err != nil {
		t.Fatal(err)
	}
	if ready.Status != "ready" || ready.NodeID != config.NodeID || ready.Generation != 1 {
		t.Fatalf("old runtime identity after generation 2 started: %+v", ready)
	}
}
