package node

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SemperSupra/wow-sidecar/go/capability"
	"github.com/SemperSupra/wow-sidecar/go/control"
	"github.com/SemperSupra/wow-sidecar/go/protocol"
)

const (
	workerRequestSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	workerLocalSHA   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	workerRevision   = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	workerRecord     = "github-issue-comment:ExampleOrg/authority#7:11"
)

type workerStore struct {
	requests map[string]string
	claims   map[string]string
	receipts map[string]string
	raw      map[string][]byte
	last     map[string]any
	refErr   error
	ctx      context.Context
}

func newWorkerStore() *workerStore {
	return &workerStore{
		requests: map[string]string{},
		claims:   map[string]string{},
		receipts: map[string]string{},
		raw:      map[string][]byte{},
	}
}

func (s *workerStore) SetContext(ctx context.Context) {
	s.ctx = ctx
}

func (s *workerStore) RefMap(kind string) (map[string]string, error) {
	if s.refErr != nil {
		return nil, s.refErr
	}
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
	out := make(map[string]string, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out, nil
}

func (s *workerStore) ReadRequest(sha string) ([]byte, error) {
	raw, ok := s.raw[sha]
	if !ok {
		return nil, errors.New("missing request")
	}
	return raw, nil
}

func (s *workerStore) CreateClaim(id, sha string) error {
	if _, exists := s.claims[id]; exists {
		return errors.New("claim exists")
	}
	s.claims[id] = sha
	return nil
}

func (s *workerStore) CreateReceipt(id, requestSHA string, receipt map[string]any) (string, string, error) {
	s.last = receipt
	commit := "dddddddddddddddddddddddddddddddddddddddd"
	s.receipts[id] = commit
	return commit, "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", nil
}

func workerConfig() *ControlConfig {
	return &ControlConfig{
		Repository:        "ExampleOrg/control",
		Profile:           "synthetic",
		CapabilityID:      "test.actuate",
		AuthorityRecord:   workerRecord,
		AuthorityRevision: workerRevision,
		AuthorityState:    "open",
		PollInterval:      5 * time.Second,
	}
}

func workerRegistry(t *testing.T, runs *int) *capability.Registry {
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
		Handler: func(_ context.Context, _ capability.Request) (capability.Outcome, error) {
			*runs++
			return capability.Outcome{
				Result:            control.ResultEligible,
				Status:            "synthetic-control-complete",
				MutationPerformed: true,
				RetryAuthorized:   false,
				Data:              map[string]any{"synthetic": true},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func workerRequest(t *testing.T) []byte {
	t.Helper()
	req := protocol.Request{
		Schema:           protocol.RequestSchema,
		RequestID:        "abc-123",
		OperatorProfile:  "synthetic",
		OperatorRevision: workerLocalSHA,
		Authority: protocol.Authority{
			Record:   workerRecord,
			Revision: workerRevision,
			State:    "open",
		},
		Inputs: map[string]any{"value": 7},
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
	return raw
}

func newSyntheticWorker(t *testing.T, store *workerStore, controlVerify control.AuthorityVerifier, runs *int) *controlWorker {
	t.Helper()
	capabilityVerify := func(_ context.Context, req capability.Request) error {
		if req.AuthorityRef != workerRecord || req.AuthorityRevision != workerRevision || req.AuthorityState != "open" {
			return errors.New("capability authority drift")
		}
		return nil
	}
	worker, err := newControlWorkerWithDependencies(
		workerConfig(),
		store,
		workerRegistry(t, runs),
		workerLocalSHA,
		controlVerify,
		capabilityVerify,
	)
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func TestControlWorkerClaimsVerifiesInvokesReceiptsOnce(t *testing.T) {
	store := newWorkerStore()
	store.requests["abc-123"] = workerRequestSHA
	store.raw[workerRequestSHA] = workerRequest(t)
	runs := 0
	verified := false
	worker := newSyntheticWorker(t, store, func(req protocol.Request) (map[string]any, error) {
		verified = true
		if req.Authority.Record != workerRecord || req.Authority.Revision != workerRevision {
			t.Fatal("authority drift")
		}
		return map[string]any{"verified": true}, nil
	}, &runs)

	out, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || !out[0].Executed || out[0].Result != control.ResultEligible {
		t.Fatalf("unexpected first outcome: %#v", out)
	}
	if !verified || runs != 1 || store.last == nil {
		t.Fatalf("verified=%v runs=%d receipt=%#v", verified, runs, store.last)
	}

	out, err = worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 || runs != 1 {
		t.Fatalf("duplicate poll repeated consequence: out=%#v runs=%d", out, runs)
	}
}

func TestControlWorkerAuthorityFailureNeverInvokesCapability(t *testing.T) {
	store := newWorkerStore()
	store.requests["abc-123"] = workerRequestSHA
	store.raw[workerRequestSHA] = workerRequest(t)
	runs := 0
	worker := newSyntheticWorker(t, store, func(protocol.Request) (map[string]any, error) {
		return nil, errors.New("stale authority")
	}, &runs)

	out, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Executed || out[0].Result != control.ResultUnknown || runs != 0 {
		t.Fatalf("unsafe stale-authority outcome: %#v runs=%d", out, runs)
	}
}

func TestControlWorkerReportsExistingClaimUnknownWithoutRetry(t *testing.T) {
	store := newWorkerStore()
	store.requests["abc-123"] = workerRequestSHA
	store.claims["abc-123"] = workerRequestSHA
	runs := 0
	worker := newSyntheticWorker(t, store, func(protocol.Request) (map[string]any, error) {
		t.Fatal("authority verifier must not run for consumed claim")
		return nil, nil
	}, &runs)

	out, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].State != "claimed-without-receipt" ||
		out[0].Result != control.ResultUnknown || out[0].Executed || out[0].RetryAuthorized || runs != 0 {
		t.Fatalf("unexpected consumed-claim outcome: %#v runs=%d", out, runs)
	}
	if out[0].ClaimMatchesRequest == nil || !*out[0].ClaimMatchesRequest {
		t.Fatalf("claim-match evidence missing: %#v", out[0])
	}
}

func TestControlWorkerBackendFailureIsCycleLocal(t *testing.T) {
	store := newWorkerStore()
	store.refErr = errors.New("backend unavailable")
	runs := 0
	worker := newSyntheticWorker(t, store, func(protocol.Request) (map[string]any, error) {
		return map[string]any{"verified": true}, nil
	}, &runs)
	if _, err := worker.RunOnce(context.Background()); err == nil {
		t.Fatal("backend failure was hidden from cycle caller")
	}
	if runs != 0 {
		t.Fatal("backend failure reached actuator")
	}
}

func TestControlConfigIsOptionalButFailsClosedWhenPartial(t *testing.T) {
	for _, name := range []string{
		"WOW_CONTROL_REPOSITORY",
		"WOW_CONTROL_PROFILE",
		"WOW_CONTROL_CAPABILITY_ID",
		"WOW_CONTROL_AUTHORITY_RECORD",
		"WOW_CONTROL_AUTHORITY_REVISION",
		"WOW_CONTROL_AUTHORITY_STATE",
		"WOW_CONTROL_POLL_SECONDS",
	} {
		t.Setenv(name, "")
	}
	config, err := controlConfigFromEnv()
	if err != nil || config != nil {
		t.Fatalf("disabled control changed daemon startup: config=%#v err=%v", config, err)
	}

	t.Setenv("WOW_CONTROL_REPOSITORY", "ExampleOrg/control")
	if _, err := controlConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("partial control config accepted: %v", err)
	}
}

func TestControlConfigRequiresAndAcceptsFileBackedGitHubAppKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "app.pem")
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WOW_CONTROL_REPOSITORY", "ExampleOrg/control")
	t.Setenv("WOW_CONTROL_PROFILE", "synthetic")
	t.Setenv("WOW_CONTROL_CAPABILITY_ID", "test.actuate")
	t.Setenv("WOW_CONTROL_AUTHORITY_RECORD", workerRecord)
	t.Setenv("WOW_CONTROL_AUTHORITY_REVISION", workerRevision)
	t.Setenv("WOW_CONTROL_AUTHORITY_STATE", "open")
	t.Setenv("WOW_CONTROL_POLL_SECONDS", "5")
	t.Setenv("GITHUB_APP_ID", "12345")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "")
	t.Setenv("GITHUB_APP_PRIVATE_KEY_FILE", "")
	if _, err := controlConfigFromEnv(); err == nil || !strings.Contains(err.Error(), "PRIVATE_KEY_FILE") {
		t.Fatalf("inline/absent secret unexpectedly admitted: %v", err)
	}

	t.Setenv("GITHUB_APP_PRIVATE_KEY_FILE", keyPath)
	config, err := controlConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config == nil || config.Repository != "ExampleOrg/control" || config.PollInterval != 5*time.Second {
		t.Fatalf("unexpected config: %#v", config)
	}
}
