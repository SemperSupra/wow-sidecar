package node

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/SemperSupra/wow-sidecar/go/capability"
	"github.com/SemperSupra/wow-sidecar/go/githubapp"
	"github.com/SemperSupra/wow-sidecar/go/protocol"
)

func clearRendezvousEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"WOW_RENDEZVOUS_WORKSET_REF",
		"WOW_RENDEZVOUS_DELEGATION_ID",
		"WOW_RENDEZVOUS_LEASE_SECONDS",
	} {
		t.Setenv(key, "")
	}
}

func TestRendezvousConfigOptionalAndFailClosedPartial(t *testing.T) {
	clearRendezvousEnv(t)
	config, err := rendezvousConfigFromEnv()
	if err != nil || config != nil {
		t.Fatalf("disabled rendezvous changed runtime: config=%#v err=%v", config, err)
	}

	t.Setenv("WOW_RENDEZVOUS_WORKSET_REF", "github:workset")
	if _, err := rendezvousConfigFromEnv(); err == nil {
		t.Fatal("partial rendezvous config was accepted")
	}

	clearRendezvousEnv(t)
	t.Setenv("WOW_RENDEZVOUS_WORKSET_REF", " github:workset ")
	t.Setenv("WOW_RENDEZVOUS_DELEGATION_ID", "handoff-proof")
	t.Setenv("WOW_RENDEZVOUS_LEASE_SECONDS", "30")
	if _, err := rendezvousConfigFromEnv(); err == nil {
		t.Fatal("non-normalized rendezvous config was accepted")
	}
}

func TestRendezvousConfigComplete(t *testing.T) {
	clearRendezvousEnv(t)
	t.Setenv("WOW_RENDEZVOUS_WORKSET_REF", "github:workset")
	t.Setenv("WOW_RENDEZVOUS_DELEGATION_ID", "handoff-proof")
	t.Setenv("WOW_RENDEZVOUS_LEASE_SECONDS", "30")
	config, err := rendezvousConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config == nil ||
		config.WorksetRef != "github:workset" ||
		config.DelegationID != "handoff-proof" ||
		config.Lease != 30*time.Second {
		t.Fatalf("unexpected rendezvous config: %#v", config)
	}
}

func testRendezvousRuntime(
	t *testing.T,
	clock func() time.Time,
	lease time.Duration,
) *Runtime {
	t.Helper()
	config := Config{
		NodeID:              "test-sovereign",
		Locality:            "sovereign",
		GenerationStateFile: filepath.Join(t.TempDir(), "generation.json"),
		ListenAddr:          "127.0.0.1:0",
		LeaseTTL:            time.Minute,
		PeerPollInterval:    time.Second,
		Rendezvous: &RendezvousConfig{
			WorksetRef:   "github:workset",
			DelegationID: "handoff-proof",
			Lease:        lease,
		},
	}
	runtime, err := newRuntime(config, controlTestSourceSHA, "g610-test", clock)
	if err != nil {
		t.Fatal(err)
	}
	nextHandoff := 0
	nextActor := 0
	runtime.rendezvous.store.SetIDFactory(func(prefix string) string {
		if prefix == "handoff" {
			nextHandoff++
			return "handoff_test_000" + string(rune('0'+nextHandoff))
		}
		nextActor++
		return "actor_test_000" + string(rune('0'+nextActor))
	})
	return runtime
}

func rendezvousRequest(t *testing.T, input map[string]any) capability.Request {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return capability.Request{
		CapabilityID:      rendezvousCapabilityID,
		AuthorityRef:      controlTestRecord,
		AuthorityRevision: githubapp.AuthorityRevision(controlTestRecord, controlTestBody),
		AuthorityState:    "open",
		Input:             raw,
	}
}

func invokeRendezvous(t *testing.T, runtime *Runtime, input map[string]any) capability.Outcome {
	t.Helper()
	outcome, err := runtime.capabilities.Invoke(
		context.Background(),
		rendezvousRequest(t, input),
		func(context.Context, capability.Request) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	return outcome
}

func dataString(t *testing.T, data map[string]any, key string) string {
	t.Helper()
	value, ok := data[key].(string)
	if !ok || value == "" {
		t.Fatalf("missing string %s in %#v", key, data)
	}
	return value
}

func TestConfiguredRendezvousIsAuthenticatedCapabilityOnly(t *testing.T) {
	now := func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }
	runtime := testRendezvousRuntime(t, now, 30*time.Second)

	card, err := runtime.Card()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range card.Capabilities {
		if id == rendezvousCapabilityID {
			found = true
		}
	}
	if !found {
		t.Fatalf("rendezvous capability absent from minimal card identifiers: %#v", card.Capabilities)
	}
	for _, descriptor := range runtime.capabilities.Advertise(false) {
		if descriptor.ID == rendezvousCapabilityID {
			t.Fatal("authenticated rendezvous descriptor leaked through public capability index")
		}
	}
	authenticated := runtime.capabilities.Advertise(true)
	found = false
	for _, descriptor := range authenticated {
		if descriptor.ID == rendezvousCapabilityID {
			found = true
			if descriptor.Kind != capability.KindActuator ||
				descriptor.Effect != capability.EffectBoundedMutation ||
				!descriptor.AuthorityRequired {
				t.Fatalf("unsafe rendezvous descriptor: %#v", descriptor)
			}
		}
	}
	if !found {
		t.Fatal("authenticated capability discovery omitted rendezvous")
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/capabilities/rendezvous.handoff", nil)
	runtime.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("generic rendezvous HTTP mutation surface appeared: %d", recorder.Code)
	}
}

func TestRendezvousTypedCapabilityLifecycleAndIdempotency(t *testing.T) {
	nowValue := time.Unix(1_800_000_000, 0).UTC()
	runtime := testRendezvousRuntime(t, func() time.Time { return nowValue }, 30*time.Second)

	offered := invokeRendezvous(t, runtime, map[string]any{
		"operation": "offer",
		"note":      "reference-only handoff",
	})
	if offered.Result != "ELIGIBLE" || offered.Status != "rendezvous-offered" || !offered.MutationPerformed {
		t.Fatalf("unexpected offer: %#v", offered)
	}
	handoffID := dataString(t, offered.Data, "handoff_id")
	if _, leaked := offered.Data["workset_ref"]; leaked {
		t.Fatalf("workset reference leaked through capability data: %#v", offered.Data)
	}
	if _, leaked := offered.Data["offered_by"]; leaked {
		t.Fatalf("principal leaked through capability data: %#v", offered.Data)
	}

	duplicateOffer := invokeRendezvous(t, runtime, map[string]any{
		"operation": "offer",
		"note":      "reference-only handoff",
	})
	if duplicateOffer.Status != "rendezvous-existing" ||
		duplicateOffer.MutationPerformed ||
		dataString(t, duplicateOffer.Data, "handoff_id") != handoffID {
		t.Fatalf("offer idempotency drift: %#v", duplicateOffer)
	}

	claimed := invokeRendezvous(t, runtime, map[string]any{
		"operation":  "claim",
		"handoff_id": handoffID,
		"launch_id":  "launch-0001",
	})
	if claimed.Status != "rendezvous-claimed" || !claimed.MutationPerformed {
		t.Fatalf("unexpected claim: %#v", claimed)
	}
	actorID := dataString(t, claimed.Data, "actor_instance_id")

	sameClaim := invokeRendezvous(t, runtime, map[string]any{
		"operation":  "claim",
		"handoff_id": handoffID,
		"launch_id":  "launch-0001",
	})
	if sameClaim.Status != "rendezvous-existing-claim" ||
		sameClaim.MutationPerformed ||
		dataString(t, sameClaim.Data, "actor_instance_id") != actorID {
		t.Fatalf("same-launch claim was not idempotent: %#v", sameClaim)
	}

	steal := invokeRendezvous(t, runtime, map[string]any{
		"operation":  "claim",
		"handoff_id": handoffID,
		"launch_id":  "launch-0002",
	})
	if steal.Result != "UNKNOWN" ||
		steal.Status != "capability-outcome-unknown" ||
		!steal.MutationPerformed ||
		steal.RetryAuthorized {
		t.Fatalf("active lease was not conservatively protected: %#v", steal)
	}

	completed := invokeRendezvous(t, runtime, map[string]any{
		"operation":         "complete",
		"handoff_id":        handoffID,
		"actor_instance_id": actorID,
		"status":            "completed",
		"result_ref":        "github:evidence/ref",
		"result_note":       "bounded result",
	})
	if completed.Status != "rendezvous-completed" || !completed.MutationPerformed {
		t.Fatalf("unexpected completion: %#v", completed)
	}

	replayedCompletion := invokeRendezvous(t, runtime, map[string]any{
		"operation":         "complete",
		"handoff_id":        handoffID,
		"actor_instance_id": actorID,
		"status":            "completed",
		"result_ref":        "github:evidence/ref",
		"result_note":       "bounded result",
	})
	if replayedCompletion.Status != "rendezvous-already-completed" || replayedCompletion.MutationPerformed {
		t.Fatalf("completion replay duplicated consequence: %#v", replayedCompletion)
	}
}

func TestRendezvousExpiredActorCannotCompleteAndCanBeReclaimed(t *testing.T) {
	nowValue := time.Unix(1_800_000_000, 0).UTC()
	runtime := testRendezvousRuntime(t, func() time.Time { return nowValue }, 5*time.Second)

	offered := invokeRendezvous(t, runtime, map[string]any{"operation": "offer"})
	handoffID := dataString(t, offered.Data, "handoff_id")
	first := invokeRendezvous(t, runtime, map[string]any{
		"operation": "claim", "handoff_id": handoffID, "launch_id": "launch-0001",
	})
	firstActor := dataString(t, first.Data, "actor_instance_id")
	nowValue = nowValue.Add(6 * time.Second)

	expiredComplete := invokeRendezvous(t, runtime, map[string]any{
		"operation":         "complete",
		"handoff_id":        handoffID,
		"actor_instance_id": firstActor,
		"status":            "completed",
	})
	if expiredComplete.Result != "UNKNOWN" || expiredComplete.RetryAuthorized {
		t.Fatalf("expired actor completion did not fail closed: %#v", expiredComplete)
	}

	second := invokeRendezvous(t, runtime, map[string]any{
		"operation": "claim", "handoff_id": handoffID, "launch_id": "launch-0002",
	})
	if second.Result != "ELIGIBLE" || second.Status != "rendezvous-claimed" {
		t.Fatalf("expired claim was not reclaimable: %#v", second)
	}
	if dataString(t, second.Data, "actor_instance_id") == firstActor {
		t.Fatal("reclaim reused expired actor identity")
	}
}

func TestRendezvousInputCannotOverrideLocalBinding(t *testing.T) {
	now := func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }
	runtime := testRendezvousRuntime(t, now, 30*time.Second)

	override := invokeRendezvous(t, runtime, map[string]any{
		"operation":     "offer",
		"workset_ref":   "github:other-workset",
		"delegation_id": "other-delegation",
	})
	if override.Result != "UNKNOWN" || override.Status != "capability-outcome-unknown" || override.RetryAuthorized {
		t.Fatalf("request-selected rendezvous binding was admitted: %#v", override)
	}

	valid := invokeRendezvous(t, runtime, map[string]any{"operation": "offer"})
	if valid.Status != "rendezvous-offered" {
		t.Fatalf("rejected override mutated rendezvous state: %#v", valid)
	}
}

func TestRendezvousOfferTraversesDurableControlPath(t *testing.T) {
	server := newSyntheticControlServer(t, "open")
	revision := githubapp.AuthorityRevision(controlTestRecord, controlTestBody)
	req := protocol.Request{
		Schema:           protocol.RequestSchema,
		RequestID:        "abc-123",
		OperatorProfile:  "rendezvous-control",
		OperatorRevision: controlTestSourceSHA,
		Authority: protocol.Authority{
			Record:   controlTestRecord,
			Revision: revision,
			State:    "open",
		},
		Inputs: map[string]any{
			"operation": "offer",
			"note":      "synthetic control handoff",
		},
		Constraints: map[string]any{
			"no_retry":            true,
			"promotion_performed": false,
			"release_performed":   false,
		},
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	server.requestRaw = raw

	config := Config{
		NodeID:              "test-sovereign",
		Locality:            "sovereign",
		GenerationStateFile: filepath.Join(t.TempDir(), "generation.json"),
		ListenAddr:          "127.0.0.1:0",
		LeaseTTL:            time.Minute,
		PeerPollInterval:    time.Second,
		Control: &ControlConfig{
			Repository:        "SemperSupra/control",
			Profile:           "rendezvous-control",
			CapabilityID:      rendezvousCapabilityID,
			AuthorityRecord:   controlTestRecord,
			AuthorityRevision: revision,
			PollInterval:      10 * time.Millisecond,
		},
		Rendezvous: &RendezvousConfig{
			WorksetRef:   "github:workset",
			DelegationID: "handoff-proof",
			Lease:        30 * time.Second,
		},
	}
	runtime, err := newRuntime(
		config,
		controlTestSourceSHA,
		"g610-test",
		func() time.Time { return time.Unix(1_800_000_000, 0).UTC() },
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime.rendezvous.store.SetIDFactory(func(prefix string) string {
		if prefix == "handoff" {
			return "handoff_control0001"
		}
		return "actor_control0001"
	})

	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	client, err := githubapp.New(githubapp.Config{
		AppID:      "12345",
		PrivateKey: key,
		APIBase:    server.server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	client.Now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	worker, err := newControlWorker(runtime, client)
	if err != nil {
		t.Fatal(err)
	}
	runtime.control = worker

	outcomes, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || !outcomes[0].Executed || outcomes[0].Result != "ELIGIBLE" || outcomes[0].RetryAuthorized {
		t.Fatalf("rendezvous control request did not execute safely: %#v", outcomes)
	}
	if !server.claimCreated || !server.receiptCreated || server.receiptWrites != 1 {
		t.Fatalf("durable control sequencing missing: claim=%v receipt=%v writes=%d", server.claimCreated, server.receiptCreated, server.receiptWrites)
	}
	if server.receipt["status"] != "rendezvous-offered" ||
		server.receipt["workspace_mutation_performed"] != true ||
		server.receipt["retry_authorized"] != false {
		t.Fatalf("unexpected rendezvous receipt: %#v", server.receipt)
	}
	details, ok := server.receipt["details"].(map[string]any)
	if !ok ||
		details["capability_id"] != rendezvousCapabilityID ||
		details["authority_verified"] != true {
		t.Fatalf("rendezvous control details drift: %#v", server.receipt["details"])
	}
	data, ok := details["capability_data"].(map[string]any)
	if !ok || data["receipt_status"] != "offered" || data["handoff_id"] != "handoff_control0001" {
		t.Fatalf("bounded rendezvous capability data missing: %#v", details["capability_data"])
	}
	if _, leaked := data["workset_ref"]; leaked {
		t.Fatalf("workset reference leaked through durable receipt: %#v", data)
	}
}
