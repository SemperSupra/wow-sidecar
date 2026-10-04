package node

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SemperSupra/wow-sidecar/go/githubapp"
	"github.com/SemperSupra/wow-sidecar/go/protocol"
)

const (
	controlTestSourceSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	controlTestRequestSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	controlTestRecord = "github-issue-comment:SemperSupra/control#7:123456789"
	controlTestBody = "AUTHORITY\nsynthetic durable control\n"
)

func clearControlEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"WOW_CONTROL_REPOSITORY",
		"WOW_CONTROL_PROFILE",
		"WOW_CONTROL_CAPABILITY",
		"WOW_CONTROL_AUTHORITY_RECORD",
		"WOW_CONTROL_AUTHORITY_REVISION",
		"WOW_CONTROL_POLL_SECONDS",
	} {
		t.Setenv(key, "")
	}
}

func TestControlConfigDisabledAndFailClosedPartial(t *testing.T) {
	clearControlEnv(t)
	cfg, err := controlConfigFromEnv()
	if err != nil || cfg != nil {
		t.Fatalf("disabled config: cfg=%#v err=%v", cfg, err)
	}

	t.Setenv("WOW_CONTROL_PROFILE", "identity-control")
	if _, err := controlConfigFromEnv(); err == nil {
		t.Fatal("partial control configuration was accepted")
	}

	clearControlEnv(t)
	t.Setenv("WOW_CONTROL_REPOSITORY", "SemperSupra/control")
	t.Setenv("WOW_CONTROL_PROFILE", " identity-control ")
	t.Setenv("WOW_CONTROL_CAPABILITY", "system.identity")
	t.Setenv("WOW_CONTROL_AUTHORITY_RECORD", controlTestRecord)
	t.Setenv("WOW_CONTROL_AUTHORITY_REVISION", githubapp.AuthorityRevision(controlTestRecord, controlTestBody))
	if _, err := controlConfigFromEnv(); err == nil {
		t.Fatal("non-normalized control configuration was accepted")
	}
}

func TestControlConfigComplete(t *testing.T) {
	clearControlEnv(t)
	revision := githubapp.AuthorityRevision(controlTestRecord, controlTestBody)
	t.Setenv("WOW_CONTROL_REPOSITORY", "SemperSupra/control")
	t.Setenv("WOW_CONTROL_PROFILE", "identity-control")
	t.Setenv("WOW_CONTROL_CAPABILITY", "system.identity")
	t.Setenv("WOW_CONTROL_AUTHORITY_RECORD", controlTestRecord)
	t.Setenv("WOW_CONTROL_AUTHORITY_REVISION", revision)
	t.Setenv("WOW_CONTROL_POLL_SECONDS", "19")

	cfg, err := controlConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil ||
		cfg.Repository != "SemperSupra/control" ||
		cfg.Profile != "identity-control" ||
		cfg.CapabilityID != "system.identity" ||
		cfg.AuthorityRecord != controlTestRecord ||
		cfg.AuthorityRevision != revision ||
		cfg.PollInterval != 19*time.Second {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

type syntheticControlServer struct {
	t              *testing.T
	server         *httptest.Server
	requestRaw     []byte
	issueState     string
	claimCreated   bool
	receiptCreated bool
	receiptWrites  int
	receipt        map[string]any
}

func newSyntheticControlServer(t *testing.T, issueState string) *syntheticControlServer {
	t.Helper()
	state := &syntheticControlServer{t: t, issueState: issueState}
	state.server = httptest.NewServer(http.HandlerFunc(state.serveHTTP))
	t.Cleanup(state.server.Close)
	return state
}

func (s *syntheticControlServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/repos/SemperSupra/control/installation":
		fmt.Fprint(w, `{"id":77}`)
	case r.Method == http.MethodPost && r.URL.Path == "/app/installations/77/access_tokens":
		fmt.Fprint(w, `{"token":"scoped-token"}`)
	case r.Method == http.MethodGet && r.URL.Path == "/repos/SemperSupra/control/git/matching-refs/heads/requests/":
		fmt.Fprintf(w, `[{"ref":"refs/heads/requests/abc-123","object":{"sha":%q}}]`, controlTestRequestSHA)
	case r.Method == http.MethodGet && r.URL.Path == "/repos/SemperSupra/control/git/matching-refs/heads/claims/":
		if s.claimCreated {
			fmt.Fprintf(w, `[{"ref":"refs/heads/claims/abc-123","object":{"sha":%q}}]`, controlTestRequestSHA)
		} else {
			fmt.Fprint(w, `[]`)
		}
	case r.Method == http.MethodGet && r.URL.Path == "/repos/SemperSupra/control/git/matching-refs/heads/receipts/":
		if s.receiptCreated {
			fmt.Fprint(w, `[{"ref":"refs/heads/receipts/abc-123","object":{"sha":"3333333333333333333333333333333333333333"}}]`)
		} else {
			fmt.Fprint(w, `[]`)
		}
	case r.Method == http.MethodGet && r.URL.Path == "/repos/SemperSupra/control/contents/request.json":
		if r.URL.Query().Get("ref") != controlTestRequestSHA {
			s.t.Fatalf("unexpected request ref: %s", r.URL.RawQuery)
		}
		fmt.Fprintf(w, `{"type":"file","encoding":"base64","content":%q}`, base64.StdEncoding.EncodeToString(s.requestRaw))
	case r.Method == http.MethodGet && r.URL.Path == "/repos/SemperSupra/control/issues/7":
		fmt.Fprintf(w, `{"state":%q}`, s.issueState)
	case r.Method == http.MethodGet && r.URL.Path == "/repos/SemperSupra/control/issues/comments/123456789":
		fmt.Fprintf(
			w,
			`{"id":123456789,"body":%q,"issue_url":"https://api.github.test/repos/SemperSupra/control/issues/7"}`,
			controlTestBody,
		)
	case r.Method == http.MethodPost && r.URL.Path == "/repos/SemperSupra/control/git/refs":
		var body struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			s.t.Fatalf("decode ref body: %v", err)
		}
		switch body.Ref {
		case "refs/heads/claims/abc-123":
			if body.SHA != controlTestRequestSHA {
				s.t.Fatalf("claim SHA drift: %s", body.SHA)
			}
			s.claimCreated = true
		case "refs/heads/receipts/abc-123":
			if body.SHA != "3333333333333333333333333333333333333333" {
				s.t.Fatalf("receipt SHA drift: %s", body.SHA)
			}
			s.receiptCreated = true
			s.receiptWrites++
		default:
			s.t.Fatalf("unexpected ref mutation: %s", body.Ref)
		}
		fmt.Fprint(w, `{}`)
	case r.Method == http.MethodPost && r.URL.Path == "/repos/SemperSupra/control/git/blobs":
		var body struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			s.t.Fatalf("decode receipt blob: %v", err)
		}
		if err := json.Unmarshal([]byte(body.Content), &s.receipt); err != nil {
			s.t.Fatalf("decode receipt content: %v", err)
		}
		fmt.Fprint(w, `{"sha":"1111111111111111111111111111111111111111"}`)
	case r.Method == http.MethodPost && r.URL.Path == "/repos/SemperSupra/control/git/trees":
		fmt.Fprint(w, `{"sha":"2222222222222222222222222222222222222222"}`)
	case r.Method == http.MethodPost && r.URL.Path == "/repos/SemperSupra/control/git/commits":
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			s.t.Fatalf("decode receipt commit: %v", err)
		}
		parents, ok := body["parents"].([]any)
		if !ok || len(parents) != 1 || parents[0] != controlTestRequestSHA {
			s.t.Fatalf("receipt parent drift: %#v", body["parents"])
		}
		fmt.Fprint(w, `{"sha":"3333333333333333333333333333333333333333"}`)
	default:
		http.NotFound(w, r)
	}
}

func testControlRequest(t *testing.T) []byte {
	t.Helper()
	req := protocol.Request{
		Schema:           protocol.RequestSchema,
		RequestID:        "abc-123",
		OperatorProfile:  "identity-control",
		OperatorRevision: controlTestSourceSHA,
		Authority: protocol.Authority{
			Record:   controlTestRecord,
			Revision: githubapp.AuthorityRevision(controlTestRecord, controlTestBody),
			State:    "open",
		},
		Inputs: map[string]any{"probe": "synthetic"},
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

func testControlRuntime(t *testing.T, server *syntheticControlServer) (*Runtime, *ControlWorker) {
	t.Helper()
	config := Config{
		NodeID:              "test-sovereign",
		Locality:            "sovereign",
		GenerationStateFile: filepath.Join(t.TempDir(), "generation.json"),
		ListenAddr:          "127.0.0.1:0",
		LeaseTTL:            time.Minute,
		PeerPollInterval:    time.Second,
		Control: &ControlConfig{
			Repository:        "SemperSupra/control",
			Profile:           "identity-control",
			CapabilityID:      "system.identity",
			AuthorityRecord:   controlTestRecord,
			AuthorityRevision: githubapp.AuthorityRevision(controlTestRecord, controlTestBody),
			PollInterval:      10 * time.Millisecond,
		},
	}
	runtime, err := newRuntime(
		config,
		controlTestSourceSHA,
		"g69-test",
		func() time.Time { return time.Unix(1_800_000_000, 0).UTC() },
	)
	if err != nil {
		t.Fatal(err)
	}
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
	return runtime, worker
}

func TestDurableControlEndToEndAndDuplicatePoll(t *testing.T) {
	server := newSyntheticControlServer(t, "open")
	server.requestRaw = testControlRequest(t)
	runtime, worker := testControlRuntime(t, server)

	outcomes, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 ||
		!outcomes[0].Executed ||
		outcomes[0].Result != "ELIGIBLE" ||
		outcomes[0].RetryAuthorized {
		t.Fatalf("unexpected outcome: %#v", outcomes)
	}
	if !server.claimCreated || !server.receiptCreated || server.receiptWrites != 1 {
		t.Fatalf("control mutations missing: claim=%v receipt=%v writes=%d", server.claimCreated, server.receiptCreated, server.receiptWrites)
	}
	if server.receipt["status"] != "system-identity-observed" ||
		server.receipt["workspace_mutation_performed"] != false ||
		server.receipt["retry_authorized"] != false {
		t.Fatalf("unexpected receipt: %#v", server.receipt)
	}
	details, ok := server.receipt["details"].(map[string]any)
	if !ok || details["capability_id"] != "system.identity" || details["authority_verified"] != true {
		t.Fatalf("receipt details drift: %#v", server.receipt["details"])
	}

	second, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 || server.receiptWrites != 1 {
		t.Fatalf("duplicate poll caused consequence: outcomes=%#v writes=%d", second, server.receiptWrites)
	}

	status := runtime.ControlStatus()
	if !status.Enabled || status.State != "READY" || status.LastOutcomeState != "idle" {
		t.Fatalf("unexpected control status after duplicate idle poll: %#v", status)
	}
}

func TestClosedAuthorityFailsClosedWithoutCapabilityExecution(t *testing.T) {
	server := newSyntheticControlServer(t, "closed")
	server.requestRaw = testControlRequest(t)
	_, worker := testControlRuntime(t, server)

	outcomes, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].Executed || outcomes[0].Result != "UNKNOWN" || outcomes[0].RetryAuthorized {
		t.Fatalf("closed authority was not fail-closed: %#v", outcomes)
	}
	if server.receipt == nil || server.receipt["status"] != "host-operator-failed-closed" {
		t.Fatalf("missing fail-closed receipt: %#v", server.receipt)
	}
}

func TestControlBackendFailureDoesNotBreakNodeHealth(t *testing.T) {
	server := newSyntheticControlServer(t, "open")
	server.requestRaw = testControlRequest(t)
	runtime, worker := testControlRuntime(t, server)
	server.server.Close()

	if _, err := worker.RunOnce(context.Background()); err == nil {
		t.Fatal("unavailable control backend did not fail observation")
	}
	status := runtime.ControlStatus()
	if status.State != "DEGRADED" || status.Reason != "control-backend-unavailable" {
		t.Fatalf("unexpected degraded status: %#v", status)
	}
	if strings.Contains(status.Reason, server.server.URL) {
		t.Fatal("raw backend detail leaked into control status")
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	runtime.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("node health coupled to control backend: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestControlStatusDisabledWithoutWorker(t *testing.T) {
	config := Config{
		NodeID:              "test-sovereign",
		Locality:            "sovereign",
		GenerationStateFile: filepath.Join(t.TempDir(), "generation.json"),
		ListenAddr:          "127.0.0.1:0",
		LeaseTTL:            time.Minute,
		PeerPollInterval:    time.Second,
	}
	runtime, err := newRuntime(
		config,
		controlTestSourceSHA,
		"g69-test",
		func() time.Time { return time.Unix(1_800_000_000, 0).UTC() },
	)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/control", nil)
	runtime.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"state":"DISABLED"`) {
		t.Fatalf("unexpected disabled status: %d %s", recorder.Code, recorder.Body.String())
	}
}
