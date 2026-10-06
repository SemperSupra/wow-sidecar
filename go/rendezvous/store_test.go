package rendezvous

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

type testClock struct{ value time.Time }

func (c *testClock) Now() time.Time { return c.value }
func (c *testClock) Advance(d time.Duration) { c.value = c.value.Add(d) }

func newTestStore(t *testing.T) (*Store, *testClock, *Principal, map[[2]string]bool) {
	t.Helper()
	clock := &testClock{value: time.Unix(1_800_000_000, 0).UTC()}
	principal := &Principal{ClientID: "webui-tunnel", Subject: "webui-tunnel"}
	allowed := map[[2]string]bool{{"durable:workset", "read-proof"}: true}
	authorize := func(_ context.Context, worksetRef, delegationID string) (Principal, error) {
		if !allowed[[2]string{worksetRef, delegationID}] {
			return Principal{}, fmt.Errorf("caller is not admitted to durable delegation")
		}
		return *principal, nil
	}
	store, err := New(authorize, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	store.SetClock(clock.Now)
	ids := []string{
		"handoff_test0001",
		"actor_test0001",
		"actor_test0002",
		"handoff_test0002",
		"actor_test0003",
	}
	index := 0
	store.SetIDFactory(func(_ string) string {
		if index >= len(ids) {
			t.Fatal("test id supply exhausted")
		}
		id := ids[index]
		index++
		return id
	})
	return store, clock, principal, allowed
}

func TestOfferIsReferenceOnlyAndIdempotent(t *testing.T) {
	store, _, _, _ := newTestStore(t)
	first, err := store.Offer(context.Background(), "durable:workset", "read-proof", "inspect frontier")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Offer(context.Background(), "durable:workset", "read-proof", "inspect frontier")
	if err != nil {
		t.Fatal(err)
	}
	if first.ReceiptStatus != "offered" || second.ReceiptStatus != "existing" {
		t.Fatalf("unexpected receipts: %q / %q", first.ReceiptStatus, second.ReceiptStatus)
	}
	if first.Handoff.ID != second.Handoff.ID {
		t.Fatal("idempotent offer changed handoff identity")
	}
	if first.Handoff.WorksetRef != "durable:workset" || first.Handoff.DelegationID != "read-proof" {
		t.Fatalf("durable references changed: %#v", first.Handoff)
	}
}

func TestConcurrencyOneRejectsDistinctActiveOffer(t *testing.T) {
	store, _, _, _ := newTestStore(t)
	if _, err := store.Offer(context.Background(), "durable:workset", "read-proof", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Offer(context.Background(), "durable:workset", "read-proof", "second"); err == nil || !strings.Contains(err.Error(), "only one active handoff") {
		t.Fatalf("distinct active offer was not rejected: %v", err)
	}
}

func TestClaimSingleOwnerAndSameLaunchIdempotent(t *testing.T) {
	store, _, _, _ := newTestStore(t)
	offered, err := store.Offer(context.Background(), "durable:workset", "read-proof", "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Claim(context.Background(), offered.Handoff.ID, "launch-0001")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Claim(context.Background(), offered.Handoff.ID, "launch-0001")
	if err != nil {
		t.Fatal(err)
	}
	if first.ReceiptStatus != "claimed" || second.ReceiptStatus != "existing-claim" {
		t.Fatalf("unexpected claim receipts: %q / %q", first.ReceiptStatus, second.ReceiptStatus)
	}
	if first.Handoff.Claim == nil || second.Handoff.Claim == nil ||
		first.Handoff.Claim.ActorInstanceID != second.Handoff.Claim.ActorInstanceID {
		t.Fatal("idempotent claim changed actor identity")
	}
	if _, err := store.Claim(context.Background(), offered.Handoff.ID, "launch-0002"); err == nil || !strings.Contains(err.Error(), "active leased claim") {
		t.Fatalf("second active claim was not rejected: %v", err)
	}
}

func TestWrongPrincipalCannotClaim(t *testing.T) {
	store, _, principal, _ := newTestStore(t)
	offered, err := store.Offer(context.Background(), "durable:workset", "read-proof", "")
	if err != nil {
		t.Fatal(err)
	}
	*principal = Principal{ClientID: "other-client", Subject: "other-client"}
	if _, err := store.Claim(context.Background(), offered.Handoff.ID, "launch-0001"); err == nil || !strings.Contains(err.Error(), "does not own") {
		t.Fatalf("wrong principal was not rejected: %v", err)
	}
}

func TestUnauthorizedDelegationCannotOfferOrClaim(t *testing.T) {
	store, _, _, allowed := newTestStore(t)
	if _, err := store.Offer(context.Background(), "durable:workset", "other-delegation", ""); err == nil || !strings.Contains(err.Error(), "not admitted") {
		t.Fatalf("unauthorized offer was not rejected: %v", err)
	}
	offered, err := store.Offer(context.Background(), "durable:workset", "read-proof", "")
	if err != nil {
		t.Fatal(err)
	}
	clear(allowed)
	if _, err := store.Claim(context.Background(), offered.Handoff.ID, "launch-0001"); err == nil || !strings.Contains(err.Error(), "not admitted") {
		t.Fatalf("unauthorized claim was not rejected: %v", err)
	}
}

func TestExpiredClaimReleasesRoutingOnlyAndCanBeReclaimed(t *testing.T) {
	store, clock, _, _ := newTestStore(t)
	offered, err := store.Offer(context.Background(), "durable:workset", "read-proof", "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Claim(context.Background(), offered.Handoff.ID, "launch-0001")
	if err != nil {
		t.Fatal(err)
	}
	firstActor := first.Handoff.Claim.ActorInstanceID
	clock.Advance(31 * time.Second)

	observed, err := store.Get(context.Background(), offered.Handoff.ID)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Status != "offered" || observed.Claim != nil || observed.LastExpiredClaim == nil ||
		observed.LastExpiredClaim.ActorInstanceID != firstActor {
		t.Fatalf("unexpected expiry state: %#v", observed)
	}
	if _, err := store.Complete(context.Background(), offered.Handoff.ID, firstActor, "completed", "git:old", ""); err == nil || !strings.Contains(err.Error(), "no active claim") {
		t.Fatalf("expired actor completed handoff: %v", err)
	}

	second, err := store.Claim(context.Background(), offered.Handoff.ID, "launch-0002")
	if err != nil {
		t.Fatal(err)
	}
	if second.Handoff.Claim.ActorInstanceID == firstActor {
		t.Fatal("reclaimed handoff reused expired actor identity")
	}
}

func TestOnlyActiveActorCanCompleteAndRetryIsIdempotent(t *testing.T) {
	store, _, _, _ := newTestStore(t)
	offered, err := store.Offer(context.Background(), "durable:workset", "read-proof", "")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(context.Background(), offered.Handoff.ID, "launch-0001")
	if err != nil {
		t.Fatal(err)
	}
	actor := claimed.Handoff.Claim.ActorInstanceID

	if _, err := store.Complete(context.Background(), offered.Handoff.ID, "actor-wrong", "completed", "", ""); err == nil || !strings.Contains(err.Error(), "does not own") {
		t.Fatalf("wrong actor completed handoff: %v", err)
	}
	first, err := store.Complete(context.Background(), 
		offered.Handoff.ID,
		actor,
		"completed",
		"github:proof/ref",
		"bounded read-only result",
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Complete(context.Background(), 
		offered.Handoff.ID,
		actor,
		"completed",
		"github:proof/ref",
		"bounded read-only result",
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.ReceiptStatus != "completed" || second.ReceiptStatus != "already-completed" {
		t.Fatalf("unexpected completion receipts: %q / %q", first.ReceiptStatus, second.ReceiptStatus)
	}
	if first.Handoff.Completion == nil || first.Handoff.Completion.ResultRef != "github:proof/ref" {
		t.Fatalf("completion result reference missing: %#v", first.Handoff)
	}
}

func TestNotesAndResultReferencesAreBounded(t *testing.T) {
	store, _, _, _ := newTestStore(t)
	if _, err := store.Offer(context.Background(), "durable:workset", "read-proof", strings.Repeat("x", NoteLimit+1)); err == nil {
		t.Fatal("oversized offer note was accepted")
	}
}


type requestPrincipalKey struct{}

func TestAuthorizationPrincipalIsRequestContextScoped(t *testing.T) {
	owner := Principal{ClientID: "federation-client", Subject: "owner-node"}
	other := Principal{ClientID: "federation-client", Subject: "other-node"}
	authorize := func(ctx context.Context, worksetRef, delegationID string) (Principal, error) {
		if ctx == nil {
			return Principal{}, fmt.Errorf("missing context")
		}
		value, ok := ctx.Value(requestPrincipalKey{}).(Principal)
		if !ok {
			return Principal{}, fmt.Errorf("missing request principal")
		}
		if worksetRef != "durable:workset" || delegationID != "read-proof" {
			return Principal{}, fmt.Errorf("caller is not admitted to durable delegation")
		}
		return value, nil
	}
	store, err := New(authorize, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	store.SetIDFactory(func(prefix string) string {
		if prefix == "handoff" {
			return "handoff_context0001"
		}
		return "actor_context0001"
	})
	ownerCtx := context.WithValue(context.Background(), requestPrincipalKey{}, owner)
	otherCtx := context.WithValue(context.Background(), requestPrincipalKey{}, other)

	offered, err := store.Offer(ownerCtx, "durable:workset", "read-proof", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ownerCtx, offered.Handoff.ID); err != nil {
		t.Fatalf("owner could not observe own handoff: %v", err)
	}
	if _, err := store.Get(otherCtx, offered.Handoff.ID); err == nil || !strings.Contains(err.Error(), "does not own") {
		t.Fatalf("different request principal observed owner handoff: %v", err)
	}
	if _, err := store.Claim(otherCtx, offered.Handoff.ID, "launch-0002"); err == nil || !strings.Contains(err.Error(), "does not own") {
		t.Fatalf("different request principal claimed owner handoff: %v", err)
	}
}

func TestConcurrentAuthorizationDoesNotUseAmbientPrincipal(t *testing.T) {
	owner := Principal{ClientID: "shared-client", Subject: "owner"}
	other := Principal{ClientID: "shared-client", Subject: "other"}
	authorize := func(ctx context.Context, worksetRef, delegationID string) (Principal, error) {
		value, ok := ctx.Value(requestPrincipalKey{}).(Principal)
		if !ok {
			return Principal{}, fmt.Errorf("missing request principal")
		}
		return value, nil
	}
	store, err := New(authorize, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	store.SetIDFactory(func(string) string { return "handoff_concurrent0001" })
	ownerCtx := context.WithValue(context.Background(), requestPrincipalKey{}, owner)
	otherCtx := context.WithValue(context.Background(), requestPrincipalKey{}, other)
	offered, err := store.Offer(ownerCtx, "durable:workset", "read-proof", "")
	if err != nil {
		t.Fatal(err)
	}

	type observation struct {
		owner bool
		err   error
	}
	results := make(chan observation, 100)
	for i := 0; i < 50; i++ {
		go func() {
			_, err := store.Get(ownerCtx, offered.Handoff.ID)
			results <- observation{owner: true, err: err}
		}()
		go func() {
			_, err := store.Get(otherCtx, offered.Handoff.ID)
			results <- observation{owner: false, err: err}
		}()
	}
	for i := 0; i < 100; i++ {
		got := <-results
		if got.owner && got.err != nil {
			t.Fatalf("owner request was contaminated by another principal: %v", got.err)
		}
		if !got.owner && (got.err == nil || !strings.Contains(got.err.Error(), "does not own")) {
			t.Fatalf("non-owner request inherited owner principal: %v", got.err)
		}
	}
}

func TestCancelledRequestContextDoesNotMutateRendezvous(t *testing.T) {
	principal := Principal{ClientID: "request-client", Subject: "request-client"}
	authorize := func(ctx context.Context, _, _ string) (Principal, error) {
		if err := ctx.Err(); err != nil {
			return Principal{}, err
		}
		return principal, nil
	}
	store, err := New(authorize, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	store.SetIDFactory(func(string) string { return "handoff_cancel0001" })

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Offer(cancelled, "durable:workset", "read-proof", "cancelled"); err == nil {
		t.Fatal("cancelled request context mutated rendezvous")
	}
	if _, err := store.Offer(context.Background(), "durable:workset", "read-proof", "live"); err != nil {
		t.Fatalf("cancelled request consumed active handoff state: %v", err)
	}
}

func TestNilRequestContextFailsClosed(t *testing.T) {
	store, err := New(func(context.Context, string, string) (Principal, error) {
		return Principal{ClientID: "client"}, nil
	}, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Offer(nil, "durable:workset", "read-proof", ""); err == nil || !strings.Contains(err.Error(), "context") {
		t.Fatalf("nil context was admitted: %v", err)
	}
}
