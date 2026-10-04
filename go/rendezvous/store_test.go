package rendezvous

import (
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
	authorize := func(worksetRef, delegationID string) (Principal, error) {
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
	first, err := store.Offer("durable:workset", "read-proof", "inspect frontier")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Offer("durable:workset", "read-proof", "inspect frontier")
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
	if _, err := store.Offer("durable:workset", "read-proof", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Offer("durable:workset", "read-proof", "second"); err == nil || !strings.Contains(err.Error(), "only one active handoff") {
		t.Fatalf("distinct active offer was not rejected: %v", err)
	}
}

func TestClaimSingleOwnerAndSameLaunchIdempotent(t *testing.T) {
	store, _, _, _ := newTestStore(t)
	offered, err := store.Offer("durable:workset", "read-proof", "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Claim(offered.Handoff.ID, "launch-0001")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Claim(offered.Handoff.ID, "launch-0001")
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
	if _, err := store.Claim(offered.Handoff.ID, "launch-0002"); err == nil || !strings.Contains(err.Error(), "active leased claim") {
		t.Fatalf("second active claim was not rejected: %v", err)
	}
}

func TestWrongPrincipalCannotClaim(t *testing.T) {
	store, _, principal, _ := newTestStore(t)
	offered, err := store.Offer("durable:workset", "read-proof", "")
	if err != nil {
		t.Fatal(err)
	}
	*principal = Principal{ClientID: "other-client", Subject: "other-client"}
	if _, err := store.Claim(offered.Handoff.ID, "launch-0001"); err == nil || !strings.Contains(err.Error(), "does not own") {
		t.Fatalf("wrong principal was not rejected: %v", err)
	}
}

func TestUnauthorizedDelegationCannotOfferOrClaim(t *testing.T) {
	store, _, _, allowed := newTestStore(t)
	if _, err := store.Offer("durable:workset", "other-delegation", ""); err == nil || !strings.Contains(err.Error(), "not admitted") {
		t.Fatalf("unauthorized offer was not rejected: %v", err)
	}
	offered, err := store.Offer("durable:workset", "read-proof", "")
	if err != nil {
		t.Fatal(err)
	}
	clear(allowed)
	if _, err := store.Claim(offered.Handoff.ID, "launch-0001"); err == nil || !strings.Contains(err.Error(), "not admitted") {
		t.Fatalf("unauthorized claim was not rejected: %v", err)
	}
}

func TestExpiredClaimReleasesRoutingOnlyAndCanBeReclaimed(t *testing.T) {
	store, clock, _, _ := newTestStore(t)
	offered, err := store.Offer("durable:workset", "read-proof", "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Claim(offered.Handoff.ID, "launch-0001")
	if err != nil {
		t.Fatal(err)
	}
	firstActor := first.Handoff.Claim.ActorInstanceID
	clock.Advance(31 * time.Second)

	observed, err := store.Get(offered.Handoff.ID)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Status != "offered" || observed.Claim != nil || observed.LastExpiredClaim == nil ||
		observed.LastExpiredClaim.ActorInstanceID != firstActor {
		t.Fatalf("unexpected expiry state: %#v", observed)
	}
	if _, err := store.Complete(offered.Handoff.ID, firstActor, "completed", "git:old", ""); err == nil || !strings.Contains(err.Error(), "no active claim") {
		t.Fatalf("expired actor completed handoff: %v", err)
	}

	second, err := store.Claim(offered.Handoff.ID, "launch-0002")
	if err != nil {
		t.Fatal(err)
	}
	if second.Handoff.Claim.ActorInstanceID == firstActor {
		t.Fatal("reclaimed handoff reused expired actor identity")
	}
}

func TestOnlyActiveActorCanCompleteAndRetryIsIdempotent(t *testing.T) {
	store, _, _, _ := newTestStore(t)
	offered, err := store.Offer("durable:workset", "read-proof", "")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(offered.Handoff.ID, "launch-0001")
	if err != nil {
		t.Fatal(err)
	}
	actor := claimed.Handoff.Claim.ActorInstanceID

	if _, err := store.Complete(offered.Handoff.ID, "actor-wrong", "completed", "", ""); err == nil || !strings.Contains(err.Error(), "does not own") {
		t.Fatalf("wrong actor completed handoff: %v", err)
	}
	first, err := store.Complete(
		offered.Handoff.ID,
		actor,
		"completed",
		"github:proof/ref",
		"bounded read-only result",
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Complete(
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
	if _, err := store.Offer("durable:workset", "read-proof", strings.Repeat("x", NoteLimit+1)); err == nil {
		t.Fatal("oversized offer note was accepted")
	}
}
