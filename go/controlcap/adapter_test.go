package controlcap

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/SemperSupra/wow-sidecar/go/capability"
	"github.com/SemperSupra/wow-sidecar/go/control"
	"github.com/SemperSupra/wow-sidecar/go/protocol"
)

const (
	testRequestSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testLocalSHA   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testRevision   = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

type memoryStore struct {
	requests map[string]string
	claims   map[string]string
	receipts map[string]string
	raw      map[string][]byte
	receipt  map[string]any
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		requests: map[string]string{},
		claims:   map[string]string{},
		receipts: map[string]string{},
		raw:      map[string][]byte{},
	}
}

func (s *memoryStore) RefMap(kind string) (map[string]string, error) {
	var source map[string]string
	switch kind {
	case "requests":
		source = s.requests
	case "claims":
		source = s.claims
	case "receipts":
		source = s.receipts
	default:
		return nil, errors.New("unexpected ref kind")
	}
	out := map[string]string{}
	for key, value := range source {
		out[key] = value
	}
	return out, nil
}

func (s *memoryStore) ReadRequest(sha string) ([]byte, error) {
	raw, ok := s.raw[sha]
	if !ok {
		return nil, errors.New("missing request")
	}
	return raw, nil
}

func (s *memoryStore) CreateClaim(id, sha string) error {
	if _, exists := s.claims[id]; exists {
		return errors.New("claim exists")
	}
	s.claims[id] = sha
	return nil
}

func (s *memoryStore) CreateReceipt(id, requestSHA string, receipt map[string]any) (string, string, error) {
	s.receipt = receipt
	s.receipts[id] = "dddddddddddddddddddddddddddddddddddddddd"
	digest, err := protocol.CanonicalJSON(receipt)
	if err != nil {
		return "", "", err
	}
	_ = digest
	return s.receipts[id], "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", nil
}

func actuatorRegistry(t *testing.T, verifySeen *bool, handlerSeen *bool, gotInput *map[string]any) *capability.Registry {
	t.Helper()
	registry := capability.NewRegistry()
	descriptor := capability.Descriptor{
		Schema:            capability.DescriptorSchema,
		ID:                "test.actuate",
		Version:           "v1",
		Kind:              capability.KindActuator,
		Effect:            capability.EffectBoundedMutation,
		Disclosure:        capability.DisclosureAuthenticated,
		AuthorityRequired: true,
	}
	err := registry.Register(capability.Registration{
		Descriptor: descriptor,
		Probe: func(context.Context) (capability.State, error) {
			return capability.State{
				CapabilityID: descriptor.ID,
				Readiness:    capability.ReadinessReady,
				ObservedAt:   time.Unix(1800000000, 0).UTC(),
			}, nil
		},
		Handler: func(_ context.Context, req capability.Request) (capability.Outcome, error) {
			if verifySeen != nil && !*verifySeen {
				t.Fatal("capability handler ran before capability authority verifier")
			}
			if handlerSeen != nil {
				*handlerSeen = true
			}
			var input map[string]any
			if err := json.Unmarshal(req.Input, &input); err != nil {
				t.Fatalf("decode input: %v", err)
			}
			if gotInput != nil {
				*gotInput = input
			}
			return capability.Outcome{
				Result:            "ELIGIBLE",
				Status:            "typed-actuator-complete",
				MutationPerformed: true,
				RetryAuthorized:   false,
				Data:              map[string]any{"receipt": "ok"},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func request(profile string, inputs map[string]any) protocol.Request {
	return protocol.Request{
		Schema:           protocol.RequestSchema,
		RequestID:        "abc-123",
		OperatorProfile:  profile,
		OperatorRevision: testLocalSHA,
		Authority: protocol.Authority{
			Record:   "github-issue-comment:SemperSupra/example#7:11",
			Revision: testRevision,
			State:    "open",
		},
		Inputs: inputs,
		Constraints: map[string]any{
			"no_retry":           true,
			"promotion_performed": false,
			"release_performed":   false,
		},
	}
}

func TestBindingInvokesOnlyStaticCapability(t *testing.T) {
	var verified, handled bool
	var gotInput map[string]any
	registry := actuatorRegistry(t, &verified, &handled, &gotInput)
	handlers, err := BuildHandlers(
		registry,
		[]Binding{{Profile: "approved-profile", CapabilityID: "test.actuate"}},
		func(_ context.Context, req capability.Request) error {
			verified = true
			if req.CapabilityID != "test.actuate" {
				t.Fatalf("unexpected capability: %s", req.CapabilityID)
			}
			if req.AuthorityRef != "github-issue-comment:SemperSupra/example#7:11" ||
				req.AuthorityRevision != testRevision ||
				req.AuthorityState != "open" {
				t.Fatalf("authority drift: %#v", req)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(handlers) != 1 {
		t.Fatalf("unexpected handler count: %d", len(handlers))
	}

	details, err := handlers["approved-profile"](request("approved-profile", map[string]any{
		"value":         7,
		"capability_id": "evil.override",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !verified || !handled {
		t.Fatalf("verified=%v handled=%v", verified, handled)
	}
	if gotInput["capability_id"] != "evil.override" {
		t.Fatalf("input was not preserved: %#v", gotInput)
	}
	if details["capability_id"] != "test.actuate" {
		t.Fatalf("request input overrode static binding: %#v", details)
	}
	if details["result"] != "ELIGIBLE" ||
		details["status"] != "typed-actuator-complete" ||
		details["workspace_mutation_performed"] != true ||
		details["retry_authorized"] != false {
		t.Fatalf("unexpected details: %#v", details)
	}
}

func TestBuildRejectsUnsafeBindings(t *testing.T) {
	registry := actuatorRegistry(t, nil, nil, nil)
	verify := func(context.Context, capability.Request) error { return nil }

	cases := []struct {
		name     string
		bindings []Binding
		verifier capability.AuthorityVerifier
	}{
		{"empty", nil, verify},
		{"profile-whitespace", []Binding{{Profile: " bad ", CapabilityID: "test.actuate"}}, verify},
		{"unknown-capability", []Binding{{Profile: "bad", CapabilityID: "missing.actuate"}}, verify},
		{"duplicate-profile", []Binding{
			{Profile: "same", CapabilityID: "test.actuate"},
			{Profile: "same", CapabilityID: "test.actuate"},
		}, verify},
		{"missing-verifier", []Binding{{Profile: "approved-profile", CapabilityID: "test.actuate"}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := BuildHandlers(registry, tc.bindings, tc.verifier); err == nil {
				t.Fatal("unsafe binding accepted")
			}
		})
	}
}

func TestControlAuthorityRunsBeforeTypedCapability(t *testing.T) {
	controlVerified := false
	capabilityVerified := false
	handlerSeen := false

	registry := actuatorRegistry(t, &capabilityVerified, &handlerSeen, nil)
	handlers, err := BuildHandlers(
		registry,
		[]Binding{{Profile: "approved-profile", CapabilityID: "test.actuate"}},
		func(_ context.Context, _ capability.Request) error {
			if !controlVerified {
				t.Fatal("capability authority check ran before control canonical verifier")
			}
			capabilityVerified = true
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	req := request("approved-profile", map[string]any{"value": 9})
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	store := newMemoryStore()
	store.requests[req.RequestID] = testRequestSHA
	store.raw[testRequestSHA] = raw

	outcome, err := control.ProcessRequest(
		store,
		req.RequestID,
		testRequestSHA,
		handlers,
		testLocalSHA,
		func(got protocol.Request) (map[string]any, error) {
			if got.Authority.Record != req.Authority.Record || got.Authority.Revision != req.Authority.Revision {
				t.Fatalf("control authority drift: %#v", got.Authority)
			}
			controlVerified = true
			return map[string]any{"verified": true}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !controlVerified || !capabilityVerified || !handlerSeen {
		t.Fatalf("control=%v capability=%v handler=%v", controlVerified, capabilityVerified, handlerSeen)
	}
	if !outcome.Executed || outcome.Result != control.ResultEligible || outcome.RetryAuthorized {
		t.Fatalf("unsafe outcome: %#v", outcome)
	}
	if store.receipt["workspace_mutation_performed"] != true {
		t.Fatalf("mutation evidence missing: %#v", store.receipt)
	}
}

func TestCapabilityVerifierFailureRemainsUnknownNoRetry(t *testing.T) {
	registry := actuatorRegistry(t, nil, nil, nil)
	handlers, err := BuildHandlers(
		registry,
		[]Binding{{Profile: "approved-profile", CapabilityID: "test.actuate"}},
		func(context.Context, capability.Request) error {
			return errors.New("authority changed")
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	details, err := handlers["approved-profile"](request("approved-profile", map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if details["result"] != "UNKNOWN" ||
		details["status"] != "authority-verification-failed" ||
		details["workspace_mutation_performed"] != false ||
		details["retry_authorized"] != false ||
		details["preserve_evidence"] != true {
		t.Fatalf("unexpected unknown outcome: %#v", details)
	}
}
