package githubapp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SemperSupra/wow-sidecar/go/protocol"
)

const (
	controlRequestSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	controlBlobSHA    = "1111111111111111111111111111111111111111"
	controlTreeSHA    = "2222222222222222222222222222222222222222"
	controlCommitSHA  = "3333333333333333333333333333333333333333"
)

func TestControlStoreGitDataFlow(t *testing.T) {
	key := testKey(t)
	requestRaw := []byte(`{
		"schema":"agent-dispatch.host-operator-request.v1",
		"request_id":"abc-123",
		"operator_profile":"synthetic",
		"operator_revision":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"authority":{
			"record":"github-issue-comment:ExampleOrg/authority#7:11",
			"revision":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			"state":"open"
		},
		"inputs":{},
		"constraints":{"no_retry":true,"promotion_performed":false,"release_performed":false}
	}`)

	var createdRefs []map[string]any
	var receiptBody string
	var commitParents []any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/ExampleOrg/example-repo/installation":
			io.WriteString(w, `{"id":77}`)

		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/77/access_tokens":
			io.WriteString(w, `{"token":"scoped-token"}`)

		case r.Method == http.MethodGet && r.URL.Path == "/repos/ExampleOrg/example-repo/git/matching-refs/heads/requests/":
			io.WriteString(w, `[{"ref":"refs/heads/requests/abc-123","object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}]`)

		case r.Method == http.MethodGet && r.URL.Path == "/repos/ExampleOrg/example-repo/contents/request.json":
			if r.URL.Query().Get("ref") != controlRequestSHA {
				t.Fatalf("wrong request ref: %q", r.URL.RawQuery)
			}
			payload := map[string]any{
				"type":     "file",
				"encoding": "base64",
				"content":  base64.StdEncoding.EncodeToString(requestRaw),
			}
			json.NewEncoder(w).Encode(payload)

		case r.Method == http.MethodPost && r.URL.Path == "/repos/ExampleOrg/example-repo/git/blobs":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			receiptBody, _ = body["content"].(string)
			io.WriteString(w, `{"sha":"1111111111111111111111111111111111111111"}`)

		case r.Method == http.MethodPost && r.URL.Path == "/repos/ExampleOrg/example-repo/git/trees":
			io.WriteString(w, `{"sha":"2222222222222222222222222222222222222222"}`)

		case r.Method == http.MethodPost && r.URL.Path == "/repos/ExampleOrg/example-repo/git/commits":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			commitParents, _ = body["parents"].([]any)
			io.WriteString(w, `{"sha":"3333333333333333333333333333333333333333"}`)

		case r.Method == http.MethodPost && r.URL.Path == "/repos/ExampleOrg/example-repo/git/refs":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			createdRefs = append(createdRefs, body)
			json.NewEncoder(w).Encode(map[string]any{"ref": body["ref"], "object": map[string]any{"sha": body["sha"]}})

		default:
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client, err := New(Config{AppID: "12345", PrivateKey: key, APIBase: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	client.Now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	store, err := NewControlStore(client, "ExampleOrg/example-repo")
	if err != nil {
		t.Fatal(err)
	}
	store.Context = context.Background()

	refs, err := store.RefMap("requests")
	if err != nil {
		t.Fatal(err)
	}
	if refs["abc-123"] != controlRequestSHA {
		t.Fatalf("unexpected refs: %#v", refs)
	}

	raw, err := store.ReadRequest(controlRequestSHA)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(requestRaw) {
		t.Fatal("request bytes changed")
	}

	if err := store.CreateClaim("abc-123", controlRequestSHA); err != nil {
		t.Fatal(err)
	}

	receipt := map[string]any{
		"schema":             protocol.ReceiptSchema,
		"request_id":         "abc-123",
		"request_commit_sha": controlRequestSHA,
		"result":             "ELIGIBLE",
	}
	commitSHA, digest, err := store.CreateReceipt("abc-123", controlRequestSHA, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if commitSHA != controlCommitSHA {
		t.Fatalf("wrong receipt commit: %s", commitSHA)
	}
	canonical, err := protocol.CanonicalJSON(receipt)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	wantDigest := "sha256:" + hex.EncodeToString(sum[:])
	if digest != wantDigest {
		t.Fatalf("digest=%s want=%s", digest, wantDigest)
	}
	if receiptBody != string(canonical) {
		t.Fatal("receipt blob is not canonical JSON")
	}
	if len(commitParents) != 1 || commitParents[0] != controlRequestSHA {
		t.Fatalf("receipt commit not parent-bound: %#v", commitParents)
	}
	if len(createdRefs) != 2 {
		t.Fatalf("unexpected refs: %#v", createdRefs)
	}
	if createdRefs[0]["ref"] != claimRefPrefix+"abc-123" || createdRefs[0]["sha"] != controlRequestSHA {
		t.Fatalf("bad claim ref: %#v", createdRefs[0])
	}
	if createdRefs[1]["ref"] != receiptRefPrefix+"abc-123" || createdRefs[1]["sha"] != controlCommitSHA {
		t.Fatalf("bad receipt ref: %#v", createdRefs[1])
	}
}

func TestControlStoreRejectsDuplicateAndMalformedRefs(t *testing.T) {
	key := testKey(t)
	responses := []string{
		`[{"ref":"refs/heads/claims/abc-123","object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},{"ref":"refs/heads/claims/abc-123","object":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}]`,
		`[{"ref":"refs/heads/claims/Bad","object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}]`,
		`[{"ref":"refs/heads/claims/abc-123","object":{"sha":"latest"}}]`,
	}
	for _, response := range responses {
		response := response
		t.Run(response[:min(len(response), 24)], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/installation"):
					io.WriteString(w, `{"id":77}`)
				case r.Method == http.MethodPost && r.URL.Path == "/app/installations/77/access_tokens":
					io.WriteString(w, `{"token":"scoped-token"}`)
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/matching-refs/heads/claims/"):
					io.WriteString(w, response)
				default:
					http.Error(w, "unexpected request", http.StatusBadRequest)
				}
			}))
			defer server.Close()

			client, err := New(Config{AppID: "12345", PrivateKey: key, APIBase: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			store, err := NewControlStore(client, "ExampleOrg/example-repo")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.RefMap("claims"); err == nil {
				t.Fatal("malformed ref set was accepted")
			}
		})
	}
}

func TestControlStoreFailsClosedOnRequestContentShape(t *testing.T) {
	key := testKey(t)
	for _, payload := range [][]byte{
		[]byte(`[]`),
		[]byte(`{"ok":true} {}`),
		[]byte{0xff, 0xfe},
	} {
		payload := payload
		t.Run(base64.StdEncoding.EncodeToString(payload), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/installation"):
					io.WriteString(w, `{"id":77}`)
				case r.Method == http.MethodPost && r.URL.Path == "/app/installations/77/access_tokens":
					io.WriteString(w, `{"token":"scoped-token"}`)
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/request.json"):
					json.NewEncoder(w).Encode(map[string]any{
						"type": "file", "encoding": "base64",
						"content": base64.StdEncoding.EncodeToString(payload),
					})
				default:
					http.Error(w, "unexpected request", http.StatusBadRequest)
				}
			}))
			defer server.Close()

			client, err := New(Config{AppID: "12345", PrivateKey: key, APIBase: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			store, err := NewControlStore(client, "ExampleOrg/example-repo")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.ReadRequest(controlRequestSHA); err == nil {
				t.Fatal("invalid request content was accepted")
			}
		})
	}
}
