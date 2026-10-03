package wake

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func testEvent(t *testing.T) Event {
	t.Helper()
	event, err := NewEvent(
		"portfolio-heartbeat",
		"github-issue-comment:ExampleOrg/project#7:11",
		"sha256:"+strings.Repeat("a", 64),
		"2026-10-03T220000Z",
		time.Date(2026, 10, 3, 22, 0, 0, 0, time.UTC),
		"cos.reconcile",
	)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func activeAuthority(event Event) (AuthorityResult, error) {
	return AuthorityResult{
		Active: true,
		ObservedRef: event.AuthorityRef,
		ObservedRevision: event.AuthorityRevision,
		State: "open",
	}, nil
}

func TestWakeEventIsExactAndSelfBinding(t *testing.T) {
	event := testEvent(t)
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	expected, err := event.ExpectedIdempotencyKey()
	if err != nil {
		t.Fatal(err)
	}
	if event.IdempotencyKey != expected {
		t.Fatalf("got %s want %s", event.IdempotencyKey, expected)
	}

	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	value["goal"] = "must not become task state"
	raw, _ = json.Marshal(value)
	if _, err := DecodeEvent(raw); err == nil || !strings.Contains(err.Error(), "fields do not match") {
		t.Fatalf("extra task field was not rejected: %v", err)
	}
}

func TestIdempotencyKeyRejectsChangedOccurrence(t *testing.T) {
	event := testEvent(t)
	event.OccurrenceID = "different-occurrence"
	if err := event.Validate(); err == nil || !strings.Contains(err.Error(), "idempotency_key") {
		t.Fatalf("changed occurrence retained stale idempotency key: %v", err)
	}
}

func TestNewEventRequiresUTC(t *testing.T) {
	_, err := NewEvent(
		"portfolio-heartbeat",
		"github-issue-comment:ExampleOrg/project#7:11",
		"sha256:"+strings.Repeat("a", 64),
		"occurrence-001",
		time.Date(2026, 10, 3, 22, 0, 0, 0, time.FixedZone("local", 7200)),
		"cos.reconcile",
	)
	if err == nil || !strings.Contains(err.Error(), "UTC") {
		t.Fatalf("non-UTC due time was accepted: %v", err)
	}
}

func TestBeginClaimsOnceAndAmbiguousClaimNeverExecutes(t *testing.T) {
	store := NewStore()
	event := testEvent(t)
	now := time.Date(2026, 10, 3, 22, 0, 0, 0, time.UTC)

	first, err := store.Begin(event, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != "claimed" || !first.Execute {
		t.Fatalf("unexpected first claim: %#v", first)
	}
	second, err := store.Begin(event, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if second.State != "claimed-without-receipt" || second.Execute {
		t.Fatalf("ambiguous duplicate was not fenced: %#v", second)
	}

	called := false
	receipt, err := Process(
		store,
		event,
		now.Add(2*time.Second),
		activeAuthority,
		func(Event) (ActionResult, error) {
			called = true
			return ActionResult{Result: "ELIGIBLE", Status: "should-not-run"}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if called || receipt.Result != "UNKNOWN" || receipt.Status != "claimed-without-receipt" || receipt.RetryAuthorized {
		t.Fatalf("ambiguous occurrence behavior changed: called=%v receipt=%#v", called, receipt)
	}
}

func TestDueDoesNotAuthorizeStaleAuthority(t *testing.T) {
	store := NewStore()
	event := testEvent(t)
	called := false
	receipt, err := Process(
		store,
		event,
		time.Now().UTC(),
		func(event Event) (AuthorityResult, error) {
			return AuthorityResult{
				Active: false,
				ObservedRef: event.AuthorityRef,
				ObservedRevision: "sha256:"+strings.Repeat("b", 64),
				State: "closed",
			}, nil
		},
		func(Event) (ActionResult, error) {
			called = true
			return ActionResult{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("stale authority caused action invocation")
	}
	if receipt.Result != "NOOP" || receipt.Status != "authority-stale-or-inactive" || receipt.ActionInvoked {
		t.Fatalf("unexpected stale receipt: %#v", receipt)
	}
}

func TestActiveWakeExecutesExactlyOnce(t *testing.T) {
	store := NewStore()
	event := testEvent(t)
	calls := 0
	invoke := func(Event) (ActionResult, error) {
		calls++
		return ActionResult{
			Result: "ELIGIBLE",
			Status: "bounded-reconcile-complete",
			Detail: map[string]any{"evidence_ref": "github:ExampleOrg/project#7"},
		}, nil
	}

	first, err := Process(store, event, time.Now().UTC(), activeAuthority, invoke)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Process(store, event, time.Now().UTC().Add(time.Second), activeAuthority, invoke)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("duplicate wake invoked action %d times", calls)
	}
	if first.Result != "ELIGIBLE" || !first.ActionInvoked || first.RetryAuthorized {
		t.Fatalf("unexpected first receipt: %#v", first)
	}
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("duplicate delivery changed terminal receipt: %s != %s", firstJSON, secondJSON)
	}
}

func TestAuthorityFailureConsumesOccurrenceAsUnknownWithoutAction(t *testing.T) {
	store := NewStore()
	event := testEvent(t)
	called := false
	receipt, err := Process(
		store,
		event,
		time.Now().UTC(),
		func(Event) (AuthorityResult, error) {
			return AuthorityResult{}, errors.New("provider unavailable secret-detail")
		},
		func(Event) (ActionResult, error) {
			called = true
			return ActionResult{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("authority failure invoked action")
	}
	if receipt.Result != "UNKNOWN" || receipt.Status != "authority-reconciliation-failed" || receipt.RetryAuthorized {
		t.Fatalf("unexpected authority failure receipt: %#v", receipt)
	}
	raw, _ := json.Marshal(receipt)
	if strings.Contains(string(raw), "secret-detail") {
		t.Fatal("raw authority error escaped into receipt")
	}
}

func TestUnknownActionOutcomeIsNotBlindlyRetried(t *testing.T) {
	store := NewStore()
	event := testEvent(t)
	calls := 0
	invoke := func(Event) (ActionResult, error) {
		calls++
		return ActionResult{}, errors.New("ambiguous downstream state secret-detail")
	}
	first, err := Process(store, event, time.Now().UTC(), activeAuthority, invoke)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Process(store, event, time.Now().UTC().Add(time.Second), activeAuthority, invoke)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("ambiguous action was replayed: %d calls", calls)
	}
	if first.Result != "UNKNOWN" || first.Status != "action-outcome-unknown" || !first.ActionInvoked {
		t.Fatalf("unexpected ambiguous action receipt: %#v", first)
	}
	if second.Result != first.Result || second.Status != first.Status {
		t.Fatalf("duplicate did not return preserved terminal UNKNOWN: %#v", second)
	}
	raw, _ := json.Marshal(first)
	if strings.Contains(string(raw), "secret-detail") {
		t.Fatal("raw action error escaped into receipt")
	}
}

func TestReceiptRequiresClaimAndExactOccurrence(t *testing.T) {
	store := NewStore()
	event := testEvent(t)
	receipt := Receipt{
		Schema: ReceiptSchema,
		WakeID: event.WakeID,
		OccurrenceID: event.OccurrenceID,
		IdempotencyKey: event.IdempotencyKey,
		AuthorityRef: event.AuthorityRef,
		AuthorityRevision: event.AuthorityRevision,
		ActionCapability: event.ActionCapability,
		Result: "NOOP",
		Status: "test",
		RetryAuthorized: false,
		Details: map[string]any{},
	}
	if err := store.Record(event, receipt); err == nil || !strings.Contains(err.Error(), "without claim") {
		t.Fatalf("receipt without claim was accepted: %v", err)
	}

	if _, err := store.Begin(event, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	receipt.AuthorityRevision = "sha256:" + strings.Repeat("b", 64)
	if err := store.Record(event, receipt); err == nil || !strings.Contains(err.Error(), "exact occurrence") {
		t.Fatalf("mismatched receipt was accepted: %v", err)
	}
}

func TestSnapshotIsSparseTerminalEvidence(t *testing.T) {
	store := NewStore()
	event := testEvent(t)
	if _, err := Process(
		store,
		event,
		time.Now().UTC(),
		activeAuthority,
		func(Event) (ActionResult, error) {
			return ActionResult{Result: "ELIGIBLE", Status: "done"}, nil
		},
	); err != nil {
		t.Fatal(err)
	}
	snapshot := store.Snapshot()
	if len(snapshot) != 1 || snapshot[0].IdempotencyKey != event.IdempotencyKey {
		t.Fatalf("unexpected sparse evidence snapshot: %#v", snapshot)
	}
}
