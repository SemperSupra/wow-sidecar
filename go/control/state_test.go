package control

import (
	"errors"
	"testing"

	"github.com/SemperSupra/wow-sidecar/go/protocol"
)

const (
	requestSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	localSHA   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type fakeStore struct {
	requests map[string]string
	claims   map[string]string
	receipts map[string]string
	raw      map[string][]byte
	last     map[string]any

	claimErr        error
	claimWinOnError bool
}

func newStore() *fakeStore {
	return &fakeStore{
		requests: map[string]string{},
		claims:   map[string]string{},
		receipts: map[string]string{},
		raw:      map[string][]byte{},
	}
}

func (s *fakeStore) RefMap(kind string) (map[string]string, error) {
	var source map[string]string
	switch kind {
	case "requests":
		source = s.requests
	case "claims":
		source = s.claims
	case "receipts":
		source = s.receipts
	default:
		return nil, errors.New("bad ref kind")
	}
	out := make(map[string]string, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out, nil
}

func (s *fakeStore) ReadRequest(sha string) ([]byte, error) {
	raw, ok := s.raw[sha]
	if !ok {
		return nil, errors.New("missing request")
	}
	return raw, nil
}

func (s *fakeStore) CreateClaim(id, sha string) error {
	if s.claimErr != nil {
		if s.claimWinOnError {
			s.claims[id] = sha
		}
		return s.claimErr
	}
	if _, exists := s.claims[id]; exists {
		return errors.New("claim exists")
	}
	s.claims[id] = sha
	return nil
}

func (s *fakeStore) CreateReceipt(id, sha string, receipt map[string]any) (string, string, error) {
	s.last = receipt
	commit := "cccccccccccccccccccccccccccccccccccccccc"
	s.receipts[id] = commit
	return commit, "sha256:synthetic", nil
}

func validRequest(id, revision string) []byte {
	return []byte(`{
		"schema":"agent-dispatch.host-operator-request.v1",
		"request_id":"` + id + `",
		"operator_profile":"synthetic",
		"operator_revision":"` + revision + `",
		"authority":{
			"record":"github-issue-comment:ExampleOrg/authority#7:11",
			"revision":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			"state":"open"
		},
		"inputs":{},
		"constraints":{"no_retry":true,"promotion_performed":false,"release_performed":false}
	}`)
}

func TestSuccessfulClaimAuthorityHandlerReceipt(t *testing.T) {
	s := newStore()
	s.requests["abc-123"] = requestSHA
	s.raw[requestSHA] = validRequest("abc-123", localSHA)
	var verified, handled bool

	out, err := ProcessPendingOnce(
		s,
		map[string]Handler{"synthetic": func(r protocol.Request) (map[string]any, error) {
			handled = true
			if !verified {
				t.Fatal("handler ran before authority verifier")
			}
			return map[string]any{
				"result":                       ResultEligible,
				"status":                       "synthetic-pass",
				"workspace_mutation_performed": false,
			}, nil
		}},
		localSHA,
		func(protocol.Request) (map[string]any, error) {
			verified = true
			return map[string]any{"verified": true}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || !out[0].Executed || out[0].Result != ResultEligible {
		t.Fatalf("outcome %#v", out)
	}
	if !handled || s.claims["abc-123"] != requestSHA || s.last["retry_authorized"] != false {
		t.Fatal("claim/receipt invariant failed")
	}
	details := s.last["details"].(map[string]any)
	if details["authority_verified"] != true {
		t.Fatal("authority verification not receipted")
	}
}

func TestExistingReceiptNeverExecutes(t *testing.T) {
	s := newStore()
	s.receipts["abc-123"] = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	called := false
	out, err := ProcessRequest(
		s,
		"abc-123",
		requestSHA,
		map[string]Handler{"synthetic": func(protocol.Request) (map[string]any, error) {
			called = true
			return nil, nil
		}},
		localSHA,
		func(protocol.Request) (map[string]any, error) { return map[string]any{}, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if called || out.Executed || out.State != "already-receipted" {
		t.Fatalf("unsafe outcome %#v called=%v", out, called)
	}
}

func TestExistingClaimIsUnknownAndNeverExecutes(t *testing.T) {
	s := newStore()
	s.claims["abc-123"] = requestSHA
	called := false
	out, err := ProcessRequest(
		s,
		"abc-123",
		requestSHA,
		map[string]Handler{"synthetic": func(protocol.Request) (map[string]any, error) {
			called = true
			return nil, nil
		}},
		localSHA,
		func(protocol.Request) (map[string]any, error) { return map[string]any{}, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if called || out.Result != ResultUnknown || out.RetryAuthorized || out.State != "claimed-without-receipt" {
		t.Fatalf("outcome %#v called=%v", out, called)
	}
	if out.ClaimMatchesRequest == nil || !*out.ClaimMatchesRequest {
		t.Fatalf("claim match evidence missing: %#v", out)
	}
	if s.last != nil {
		t.Fatal("existing claim must not manufacture a receipt")
	}
}

func TestExistingMismatchedClaimReportsMismatch(t *testing.T) {
	s := newStore()
	s.claims["abc-123"] = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	out, err := ProcessRequest(
		s,
		"abc-123",
		requestSHA,
		map[string]Handler{},
		localSHA,
		func(protocol.Request) (map[string]any, error) { return map[string]any{}, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if out.ClaimMatchesRequest == nil || *out.ClaimMatchesRequest {
		t.Fatalf("expected claim mismatch, got %#v", out)
	}
	if out.Result != ResultUnknown || out.Executed || out.RetryAuthorized {
		t.Fatalf("unsafe mismatched-claim outcome %#v", out)
	}
}

func TestAuthorityFailureReceiptsUnknownWithoutHandler(t *testing.T) {
	s := newStore()
	s.raw[requestSHA] = validRequest("abc-123", localSHA)
	called := false
	out, err := ProcessRequest(
		s,
		"abc-123",
		requestSHA,
		map[string]Handler{"synthetic": func(protocol.Request) (map[string]any, error) {
			called = true
			return nil, nil
		}},
		localSHA,
		func(protocol.Request) (map[string]any, error) { return nil, errors.New("stale authority") },
	)
	if err != nil {
		t.Fatal(err)
	}
	if called || out.Result != ResultUnknown || out.Executed {
		t.Fatalf("outcome %#v called=%v", out, called)
	}
	if s.last["status"] != "host-operator-failed-closed" {
		t.Fatalf("receipt %#v", s.last)
	}
}

func TestHandlerFailureMarksExecutionConsumed(t *testing.T) {
	s := newStore()
	s.raw[requestSHA] = validRequest("abc-123", localSHA)
	out, err := ProcessRequest(
		s,
		"abc-123",
		requestSHA,
		map[string]Handler{"synthetic": func(protocol.Request) (map[string]any, error) {
			return nil, errors.New("operator failed")
		}},
		localSHA,
		func(protocol.Request) (map[string]any, error) { return map[string]any{}, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Executed || out.Result != ResultUnknown {
		t.Fatalf("outcome %#v", out)
	}
}

func TestOnlyLexicographicallyFirstPendingRequestRuns(t *testing.T) {
	s := newStore()
	s.requests["bbb-001"] = "1111111111111111111111111111111111111111"
	s.requests["aaa-001"] = requestSHA
	s.raw[requestSHA] = validRequest("aaa-001", localSHA)
	s.raw[s.requests["bbb-001"]] = validRequest("bbb-001", localSHA)
	var handled []string
	out, err := ProcessPendingOnce(
		s,
		map[string]Handler{"synthetic": func(r protocol.Request) (map[string]any, error) {
			handled = append(handled, r.RequestID)
			return map[string]any{"result": ResultEligible, "workspace_mutation_performed": false}, nil
		}},
		localSHA,
		func(protocol.Request) (map[string]any, error) { return map[string]any{}, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || len(handled) != 1 || handled[0] != "aaa-001" {
		t.Fatalf("out=%#v handled=%#v", out, handled)
	}
}

func TestClaimRaceReconcilesUnknown(t *testing.T) {
	s := newStore()
	s.claimErr = errors.New("create ref conflict")
	s.claimWinOnError = true
	out, err := ProcessRequest(
		s,
		"abc-123",
		requestSHA,
		map[string]Handler{},
		localSHA,
		func(protocol.Request) (map[string]any, error) { return map[string]any{}, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != ResultUnknown || out.State != "claimed-without-receipt" || out.RetryAuthorized {
		t.Fatalf("outcome %#v", out)
	}
}

func TestInvalidRequestAfterClaimGetsUnknownReceipt(t *testing.T) {
	s := newStore()
	s.raw[requestSHA] = []byte(`{"bad":true}`)
	out, err := ProcessRequest(
		s,
		"abc-123",
		requestSHA,
		map[string]Handler{},
		localSHA,
		func(protocol.Request) (map[string]any, error) { return map[string]any{}, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result != ResultUnknown || s.last == nil || s.claims["abc-123"] != requestSHA {
		t.Fatalf("outcome %#v receipt %#v", out, s.last)
	}
}
