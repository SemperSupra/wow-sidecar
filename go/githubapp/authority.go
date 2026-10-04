package githubapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	authorityRecordRE   = regexp.MustCompile(`^github-issue-comment:([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)#([1-9][0-9]*):([1-9][0-9]*)$`)
	authorityRevisionRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type IssueCommentAuthority struct {
	Record      string
	Repository  string
	IssueNumber int64
	CommentID   int64
	Revision    string
	IssueState  string
}

func AuthorityRevision(record, body string) string {
	sum := sha256.Sum256([]byte(record + "\n" + body))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (a IssueCommentAuthority) Receipt() map[string]any {
	return map[string]any{
		"record":      a.Record,
		"revision":    a.Revision,
		"issue_state": a.IssueState,
		"verified":    true,
	}
}

func parseAuthorityRecord(record string) (repository string, issueNumber, commentID int64, err error) {
	match := authorityRecordRE.FindStringSubmatch(record)
	if match == nil {
		return "", 0, 0, fmt.Errorf("unsupported authority record locator")
	}
	issueNumber, err = strconv.ParseInt(match[2], 10, 64)
	if err != nil || issueNumber <= 0 {
		return "", 0, 0, fmt.Errorf("invalid authority issue number")
	}
	commentID, err = strconv.ParseInt(match[3], 10, 64)
	if err != nil || commentID <= 0 {
		return "", 0, 0, fmt.Errorf("invalid authority comment id")
	}
	return match[1], issueNumber, commentID, nil
}

func ValidateIssueCommentAuthorityExpectation(record, revision, state string) error {
	if _, _, _, err := parseAuthorityRecord(record); err != nil {
		return err
	}
	if !authorityRevisionRE.MatchString(revision) {
		return fmt.Errorf("invalid authority revision token")
	}
	if state != "open" {
		return fmt.Errorf("authority state must be open")
	}
	return nil
}

func (c *Client) ReadIssueCommentAuthority(ctx context.Context, record string) (IssueCommentAuthority, error) {
	if c == nil {
		return IssueCommentAuthority{}, fmt.Errorf("GitHub App client is required")
	}
	repository, issueNumber, commentID, err := parseAuthorityRecord(record)
	if err != nil {
		return IssueCommentAuthority{}, err
	}
	owner, repo, err := validateRepository(repository)
	if err != nil {
		return IssueCommentAuthority{}, err
	}
	token, err := c.InstallationTokenForRepository(ctx, repository)
	if err != nil {
		return IssueCommentAuthority{}, err
	}

	base := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
	issueValue, err := c.doJSON(
		ctx,
		http.MethodGet,
		base+"/issues/"+strconv.FormatInt(issueNumber, 10),
		token,
		nil,
		"object",
	)
	if err != nil {
		return IssueCommentAuthority{}, err
	}
	issue := issueValue.(map[string]any)
	issueState, ok := issue["state"].(string)
	if !ok || (issueState != "open" && issueState != "closed") {
		return IssueCommentAuthority{}, fmt.Errorf("canonical authority issue returned invalid state")
	}

	commentValue, err := c.doJSON(
		ctx,
		http.MethodGet,
		base+"/issues/comments/"+strconv.FormatInt(commentID, 10),
		token,
		nil,
		"object",
	)
	if err != nil {
		return IssueCommentAuthority{}, err
	}
	comment := commentValue.(map[string]any)
	observedID, ok := comment["id"].(json.Number)
	if !ok {
		return IssueCommentAuthority{}, fmt.Errorf("canonical authority comment id mismatch")
	}
	observedIDValue, err := observedID.Int64()
	if err != nil || observedIDValue != commentID {
		return IssueCommentAuthority{}, fmt.Errorf("canonical authority comment id mismatch")
	}
	body, ok := comment["body"].(string)
	if !ok || body == "" {
		return IssueCommentAuthority{}, fmt.Errorf("canonical authority comment body is unavailable")
	}
	issueURL, ok := comment["issue_url"].(string)
	expectedSuffix := "/repos/" + owner + "/" + repo + "/issues/" + strconv.FormatInt(issueNumber, 10)
	if !ok || !strings.HasSuffix(issueURL, expectedSuffix) {
		return IssueCommentAuthority{}, fmt.Errorf("authority comment is not attached to the expected issue")
	}

	return IssueCommentAuthority{
		Record:      record,
		Repository:  repository,
		IssueNumber: issueNumber,
		CommentID:   commentID,
		Revision:    AuthorityRevision(record, body),
		IssueState:  issueState,
	}, nil
}

func (c *Client) VerifyIssueCommentAuthority(
	ctx context.Context,
	record, expectedRevision, expectedState string,
) (map[string]any, error) {
	if expectedState != "open" {
		return nil, fmt.Errorf("host-control authority must require open canonical state")
	}
	if !authorityRevisionRE.MatchString(expectedRevision) {
		return nil, fmt.Errorf("invalid authority revision token")
	}
	observed, err := c.ReadIssueCommentAuthority(ctx, record)
	if err != nil {
		return nil, err
	}
	if observed.IssueState != expectedState {
		return nil, fmt.Errorf("canonical authority is closed or cancelled")
	}
	if observed.Revision != expectedRevision {
		return nil, fmt.Errorf("canonical authority revision is stale")
	}
	return observed.Receipt(), nil
}
