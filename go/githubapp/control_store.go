package githubapp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/SemperSupra/wow-sidecar/go/protocol"
)

const (
	requestRefPrefix = "refs/heads/requests/"
	claimRefPrefix   = "refs/heads/claims/"
	receiptRefPrefix = "refs/heads/receipts/"
)

type ControlStore struct {
	Client     *Client
	Repository string
	Context    context.Context

	owner string
	repo  string
}

func NewControlStore(client *Client, repository string) (*ControlStore, error) {
	if client == nil {
		return nil, errors.New("GitHub App client is required")
	}
	owner, repo, err := validateRepository(repository)
	if err != nil {
		return nil, err
	}
	return &ControlStore{
		Client:     client,
		Repository: repository,
		owner:      owner,
		repo:       repo,
	}, nil
}

func (s *ControlStore) ctx() context.Context {
	if s.Context != nil {
		return s.Context
	}
	return context.Background()
}

func (s *ControlStore) SetContext(ctx context.Context) {
	s.Context = ctx
}

func (s *ControlStore) token() (string, error) {
	return s.Client.InstallationTokenForRepository(s.ctx(), s.Repository)
}

func (s *ControlStore) RefMap(kind string) (map[string]string, error) {
	var prefix string
	switch kind {
	case "requests":
		prefix = requestRefPrefix
	case "claims":
		prefix = claimRefPrefix
	case "receipts":
		prefix = receiptRefPrefix
	default:
		return nil, fmt.Errorf("unsupported control ref prefix")
	}

	token, err := s.token()
	if err != nil {
		return nil, err
	}
	path := "/repos/" + url.PathEscape(s.owner) + "/" + url.PathEscape(s.repo) +
		"/git/matching-refs/heads/" + kind + "/"
	value, err := s.Client.doJSON(s.ctx(), http.MethodGet, path, token, nil, "array")
	if err != nil {
		return nil, err
	}

	out := map[string]string{}
	for index, item := range value.([]any) {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s ref entry %d is not an object", kind, index)
		}
		ref, ok := object["ref"].(string)
		if !ok || !strings.HasPrefix(ref, prefix) {
			return nil, fmt.Errorf("unexpected %s ref", kind)
		}
		requestID := strings.TrimPrefix(ref, prefix)
		if !validControlRequestID(requestID) {
			return nil, fmt.Errorf("invalid %s request id", kind)
		}
		target, ok := object["object"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s lacks a commit object", ref)
		}
		sha, ok := target["sha"].(string)
		if !ok || !lowerHexControl(sha, 40) {
			return nil, fmt.Errorf("%s lacks a commit SHA", ref)
		}
		if _, duplicate := out[requestID]; duplicate {
			return nil, fmt.Errorf("duplicate %s request id", kind)
		}
		out[requestID] = sha
	}
	return out, nil
}

func (s *ControlStore) ReadRequest(commitSHA string) ([]byte, error) {
	if !lowerHexControl(commitSHA, 40) {
		return nil, errors.New("commit_sha must be a 40-hex Git SHA")
	}
	token, err := s.token()
	if err != nil {
		return nil, err
	}
	path := "/repos/" + url.PathEscape(s.owner) + "/" + url.PathEscape(s.repo) +
		"/contents/request.json?ref=" + url.QueryEscape(commitSHA)
	value, err := s.Client.doJSON(s.ctx(), http.MethodGet, path, token, nil, "object")
	if err != nil {
		return nil, err
	}
	object := value.(map[string]any)
	if object["type"] != "file" {
		return nil, errors.New("request.json is not a file")
	}
	encoding, _ := object["encoding"].(string)
	content, ok := object["content"].(string)
	if encoding != "base64" || !ok {
		return nil, errors.New("request.json is not base64 content")
	}
	raw, err := base64.StdEncoding.DecodeString(content)
	if err != nil {
		return nil, errors.New("request.json has invalid base64 content")
	}

	if !utf8.Valid(raw) {
		return nil, errors.New("request.json is not valid UTF-8 JSON object")
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed == nil {
		return nil, errors.New("request.json is not valid UTF-8 JSON object")
	}
	return raw, nil
}

func (s *ControlStore) CreateClaim(requestID, requestSHA string) error {
	if !validControlRequestID(requestID) {
		return errors.New("invalid request id")
	}
	if !lowerHexControl(requestSHA, 40) {
		return errors.New("request_sha must be a 40-hex Git SHA")
	}
	token, err := s.token()
	if err != nil {
		return err
	}
	_, err = s.Client.doJSON(
		s.ctx(),
		http.MethodPost,
		"/repos/"+url.PathEscape(s.owner)+"/"+url.PathEscape(s.repo)+"/git/refs",
		token,
		map[string]any{"ref": claimRefPrefix + requestID, "sha": requestSHA},
		"object",
	)
	return err
}

func (s *ControlStore) CreateReceipt(requestID, requestSHA string, receipt map[string]any) (string, string, error) {
	if !validControlRequestID(requestID) {
		return "", "", errors.New("invalid request id")
	}
	if !lowerHexControl(requestSHA, 40) {
		return "", "", errors.New("request_sha must be a 40-hex Git SHA")
	}
	if receipt == nil {
		return "", "", errors.New("receipt is required")
	}

	raw, err := protocol.CanonicalJSON(receipt)
	if err != nil {
		return "", "", err
	}
	token, err := s.token()
	if err != nil {
		return "", "", err
	}
	base := "/repos/" + url.PathEscape(s.owner) + "/" + url.PathEscape(s.repo)

	blobValue, err := s.Client.doJSON(
		s.ctx(),
		http.MethodPost,
		base+"/git/blobs",
		token,
		map[string]any{"content": string(raw), "encoding": "utf-8"},
		"object",
	)
	if err != nil {
		return "", "", err
	}
	blobSHA, ok := blobValue.(map[string]any)["sha"].(string)
	if !ok || !lowerHexControl(blobSHA, 40) {
		return "", "", errors.New("GitHub did not return receipt blob SHA")
	}

	treeValue, err := s.Client.doJSON(
		s.ctx(),
		http.MethodPost,
		base+"/git/trees",
		token,
		map[string]any{
			"tree": []any{
				map[string]any{
					"path": "receipt.json",
					"mode": "100644",
					"type": "blob",
					"sha":  blobSHA,
				},
			},
		},
		"object",
	)
	if err != nil {
		return "", "", err
	}
	treeSHA, ok := treeValue.(map[string]any)["sha"].(string)
	if !ok || !lowerHexControl(treeSHA, 40) {
		return "", "", errors.New("GitHub did not return receipt tree SHA")
	}

	commitValue, err := s.Client.doJSON(
		s.ctx(),
		http.MethodPost,
		base+"/git/commits",
		token,
		map[string]any{
			"message": "record host-operator receipt " + requestID,
			"tree":    treeSHA,
			"parents": []string{requestSHA},
		},
		"object",
	)
	if err != nil {
		return "", "", err
	}
	commitSHA, ok := commitValue.(map[string]any)["sha"].(string)
	if !ok || !lowerHexControl(commitSHA, 40) {
		return "", "", errors.New("GitHub did not return receipt commit SHA")
	}

	_, err = s.Client.doJSON(
		s.ctx(),
		http.MethodPost,
		base+"/git/refs",
		token,
		map[string]any{"ref": receiptRefPrefix + requestID, "sha": commitSHA},
		"object",
	)
	if err != nil {
		return "", "", err
	}

	sum := sha256.Sum256(raw)
	return commitSHA, "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validControlRequestID(value string) bool {
	if len(value) < 3 || len(value) > 80 {
		return false
	}
	for i, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			continue
		}
		if r == '-' && i > 0 {
			continue
		}
		return false
	}
	return true
}

func lowerHexControl(value string, size int) bool {
	if len(value) != size {
		return false
	}
	for _, r := range value {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'f' {
			continue
		}
		return false
	}
	return true
}

