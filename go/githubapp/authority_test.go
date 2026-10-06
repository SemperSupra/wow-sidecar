package githubapp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const authorityRecord = "github-issue-comment:SemperSupra/example-private#7:123456789"
const authorityBody = "AUTHORITY\nexactly one bounded observation\n"
const authorityExpectedRevision = "sha256:66ab680f59a3b44a74a0453247a749b9f47d233d18e4259b0340541db3b5a71c"

func TestAuthorityRevisionMatchesPythonVector(t *testing.T) {
	if got := AuthorityRevision(authorityRecord, authorityBody); got != authorityExpectedRevision {
		t.Fatalf("got %q want %q", got, authorityExpectedRevision)
	}
}

func authorityClient(t *testing.T, issueState string, commentID int64, issueURL string, body string) (*Client, *int) {
	t.Helper()
	key := testKey(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/SemperSupra/example-private/installation":
			fmt.Fprint(w, `{"id":77}`)
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/77/access_tokens":
			fmt.Fprint(w, `{"token":"scoped-token"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/SemperSupra/example-private/issues/7":
			if r.Header.Get("Authorization") != "Bearer scoped-token" {
				t.Fatalf("missing repository-scoped bearer")
			}
			fmt.Fprintf(w, `{"state":%q}`, issueState)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/SemperSupra/example-private/issues/comments/123456789":
			if r.Header.Get("Authorization") != "Bearer scoped-token" {
				t.Fatalf("missing repository-scoped bearer")
			}
			fmt.Fprintf(w, `{"id":%d,"body":%q,"issue_url":%q}`, commentID, body, issueURL)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	client, err := New(Config{AppID: "12345", PrivateKey: key, APIBase: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	client.Now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	return client, &calls
}

func TestReadAndVerifyIssueCommentAuthority(t *testing.T) {
	client, calls := authorityClient(
		t,
		"open",
		123456789,
		"https://api.github.test/repos/SemperSupra/example-private/issues/7",
		authorityBody,
	)
	observed, err := client.ReadIssueCommentAuthority(context.Background(), authorityRecord)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Repository != "SemperSupra/example-private" ||
		observed.IssueNumber != 7 ||
		observed.CommentID != 123456789 ||
		observed.IssueState != "open" ||
		observed.Revision != authorityExpectedRevision {
		t.Fatalf("unexpected observed authority: %#v", observed)
	}
	if *calls != 4 {
		t.Fatalf("unexpected call count: %d", *calls)
	}

	receipt, err := client.VerifyIssueCommentAuthority(
		context.Background(),
		authorityRecord,
		authorityExpectedRevision,
		"open",
	)
	if err != nil {
		t.Fatal(err)
	}
	if receipt["record"] != authorityRecord ||
		receipt["revision"] != authorityExpectedRevision ||
		receipt["issue_state"] != "open" ||
		receipt["verified"] != true {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
}

func TestAuthorityReaderFailsClosedOnIdentityAndStateDrift(t *testing.T) {
	cases := []struct {
		name      string
		record    string
		state     string
		commentID int64
		issueURL  string
		body      string
		revision  string
	}{
		{
			name:      "closed-issue",
			record:    authorityRecord,
			state:     "closed",
			commentID: 123456789,
			issueURL:  "https://api.github.test/repos/SemperSupra/example-private/issues/7",
			body:      authorityBody,
			revision:  authorityExpectedRevision,
		},
		{
			name:      "comment-id-mismatch",
			record:    authorityRecord,
			state:     "open",
			commentID: 123456788,
			issueURL:  "https://api.github.test/repos/SemperSupra/example-private/issues/7",
			body:      authorityBody,
			revision:  authorityExpectedRevision,
		},
		{
			name:      "wrong-issue-url",
			record:    authorityRecord,
			state:     "open",
			commentID: 123456789,
			issueURL:  "https://api.github.test/repos/SemperSupra/example-private/issues/8",
			body:      authorityBody,
			revision:  authorityExpectedRevision,
		},
		{
			name:      "empty-body",
			record:    authorityRecord,
			state:     "open",
			commentID: 123456789,
			issueURL:  "https://api.github.test/repos/SemperSupra/example-private/issues/7",
			body:      "",
			revision:  authorityExpectedRevision,
		},
		{
			name:      "stale-revision",
			record:    authorityRecord,
			state:     "open",
			commentID: 123456789,
			issueURL:  "https://api.github.test/repos/SemperSupra/example-private/issues/7",
			body:      authorityBody,
			revision:  "sha256:" + strings.Repeat("0", 64),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := authorityClient(t, tc.state, tc.commentID, tc.issueURL, tc.body)
			if _, err := client.VerifyIssueCommentAuthority(
				context.Background(),
				tc.record,
				tc.revision,
				"open",
			); err == nil {
				t.Fatal("drifted authority accepted")
			}
		})
	}

	client, _ := authorityClient(
		t,
		"open",
		123456789,
		"https://api.github.test/repos/SemperSupra/example-private/issues/7",
		authorityBody,
	)
	for _, bad := range []string{
		"github-issue:SemperSupra/example-private#7",
		"github-issue-comment:SemperSupra/example-private#0:1",
		"github-issue-comment:SemperSupra/example-private#7:0",
		"github-issue-comment:https://example.invalid#7:1",
	} {
		if _, err := client.ReadIssueCommentAuthority(context.Background(), bad); err == nil {
			t.Fatalf("invalid record accepted: %q", bad)
		}
	}
	if _, err := client.VerifyIssueCommentAuthority(
		context.Background(),
		authorityRecord,
		authorityExpectedRevision,
		"closed",
	); err == nil {
		t.Fatal("non-open expected state accepted")
	}
}
