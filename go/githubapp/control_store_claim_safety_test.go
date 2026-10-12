package githubapp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// All identities and HTTP responses in this file are synthetic.
// The tests qualify one transport primitive, NOT live GitHub admission or
// permission to call a consequential executor.  A failed/ambiguous claim
// must never be interpreted by the caller as permission to execute.

func TestControlStoreCreateClaimConcurrentCreateOnce(t *testing.T) {
	const (
		requestID   = "synthetic-supra-claim-001"
		sha         = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		claimRef    = "refs/heads/claims/" + requestID
		competitors = 8
	)

	var mu sync.Mutex
	refs := make(map[string]string)
	postCount := 0
	acceptedCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/ExampleOrg/example-repo/installation":
			fmt.Fprint(w, `{"id":77}`)
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/77/access_tokens":
			fmt.Fprint(w, `{"token":"scoped-synthetic-token"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/ExampleOrg/example-repo/git/refs":
			var body struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad test request", http.StatusBadRequest)
				return
			}
			mu.Lock()
			postCount++
			_, exists := refs[body.Ref]
			if !exists && body.Ref == claimRef && body.SHA == sha {
				refs[body.Ref] = body.SHA
				acceptedCount++
			}
			mu.Unlock()
			if body.Ref != claimRef || body.SHA != sha {
				http.Error(w, "wrong identity", http.StatusBadRequest)
			} else if exists {
				http.Error(w, "ref already exists", http.StatusUnprocessableEntity)
			} else {
				w.WriteHeader(http.StatusCreated)
				fmt.Fprint(w, `{"ref":"refs/heads/claims/synthetic-supra-claim-001","object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`)
			}
		case r.Method == http.MethodGet && r.URL.Path == "/repos/ExampleOrg/example-repo/git/matching-refs/heads/claims/":
			mu.Lock()
			_, exists := refs[claimRef]
			mu.Unlock()
			if exists {
				fmt.Fprint(w, `[{"ref":"refs/heads/claims/synthetic-supra-claim-001","object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}]`)
			} else {
				fmt.Fprint(w, "[]")
			}
		default:
			http.Error(w, "unexpected test API path", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client, err := New(Config{AppID: "12345", PrivateKey: testKey(t), APIBase: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewControlStore(client, "ExampleOrg/example-repo")
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	outcomes := make(chan error, competitors)
	for i := 0; i < competitors; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outcomes <- store.CreateClaim(requestID, sha)
		}()
	}
	wg.Wait()
	close(outcomes)

	successes, conflicts := 0, 0
	for err := range outcomes {
		if err == nil {
			successes++
		} else if strings.Contains(err.Error(), "HTTP 422") {
			conflicts++
		} else {
			t.Fatalf("unexpected create-once outcome: %v", err)
		}
	}
	mu.Lock()
	count, accepted, got := postCount, acceptedCount, refs[claimRef]
	mu.Unlock()
	if successes != 1 || conflicts != competitors-1 || count != competitors ||
		accepted != 1 || got != sha {
		t.Fatalf("unsafe claim cardinality successes=%d conflicts=%d posts=%d accepted=%d sha=%s",
			successes, conflicts, count, accepted, got)
	}
	observed, err := store.RefMap("claims")
	if err != nil {
		t.Fatal(err)
	}
	if len(observed) != 1 || observed[requestID] != sha {
		t.Fatalf("durable claim readback differs: %#v", observed)
	}
}

// A 5xx response may arrive AFTER GitHub created the ref, or a 429 may
// arrive BEFORE any write. Neither outcome authorizes a retry of an
// irreversible operation. Readback is diagnostic, not evidence that this
// caller owns a claim created by a competing actor.
func TestControlStoreCreateClaimAmbiguousAndThrottled(t *testing.T) {
	const (
		requestID = "synthetic-supra-claim-002"
		sha       = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		claimRef  = "refs/heads/claims/" + requestID
	)
	for _, tc := range []struct {
		name      string
		status    int
		persisted bool
	}{
		{"ambiguous-server-error-after-write", http.StatusGatewayTimeout, true},
		{"throttled-before-write", http.StatusTooManyRequests, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			refs := make(map[string]string)
			writes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/repos/ExampleOrg/example-repo/installation":
					fmt.Fprint(w, `{"id":77}`)
				case r.Method == http.MethodPost && r.URL.Path == "/app/installations/77/access_tokens":
					fmt.Fprint(w, `{"token":"scoped-synthetic-token"}`)
				case r.Method == http.MethodPost && r.URL.Path == "/repos/ExampleOrg/example-repo/git/refs":
					var body struct {
						Ref string `json:"ref"`
						SHA string `json:"sha"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						http.Error(w, "bad test request", http.StatusBadRequest)
						return
					}
					mu.Lock()
					writes++
					// In an ambiguous 5xx (e.g. 504 Gateway Timeout post-write), the server-side
					// store persisted the ref before the gateway timeout occurred.
					// In a throttled 429, the request is rejected before reaching the store.
					if tc.persisted {
						refs[body.Ref] = body.SHA
					}
					mu.Unlock()
					http.Error(w, "synthetic inconclusive write", tc.status)
				case r.Method == http.MethodGet && r.URL.Path == "/repos/ExampleOrg/example-repo/git/matching-refs/heads/claims/":
					mu.Lock()
					refSHA, exists := refs[claimRef]
					mu.Unlock()
					if exists {
						fmt.Fprintf(w, `[{"ref":%q,"object":{"sha":%q}}]`, claimRef, refSHA)
					} else {
						fmt.Fprint(w, "[]")
					}
				default:
					http.Error(w, "unexpected test API path", http.StatusBadRequest)
				}
			}))
			defer server.Close()
			client, err := New(Config{AppID: "12345", PrivateKey: testKey(t), APIBase: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			store, err := NewControlStore(client, "ExampleOrg/example-repo")
			if err != nil {
				t.Fatal(err)
			}
			if err = store.CreateClaim(requestID, sha); err == nil ||
				!strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", tc.status)) {
				t.Fatalf("ambiguous claim result must fail closed, got %v", err)
			}
			if writes != 1 {
				t.Fatalf("one call retried ambiguous claim: %d writes", writes)
			}
			observed, err := store.RefMap("claims")
			if err != nil {
				t.Fatal(err)
			}
			_, present := observed[requestID]
			if present != tc.persisted {
				t.Fatalf("readback present=%v, expected %v", present, tc.persisted)
			}
			// No actor action is called by this test on ambiguous POST or
			// following the independent diagnostic readback.
		})
	}
}
