package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const defaultAPIBase = "https://api.github.com"

type Config struct {
	AppID      string
	PrivateKey *rsa.PrivateKey
	APIBase    string
}

type Client struct {
	Config Config
	HTTP   *http.Client
	Now    func() time.Time
}

func ConfigFromEnv() (Config, error) {
	appID := strings.TrimSpace(os.Getenv("GITHUB_APP_ID"))
	keyFile := strings.TrimSpace(os.Getenv("GITHUB_APP_PRIVATE_KEY_FILE"))
	keyText := ""
	if keyFile != "" {
		raw, err := os.ReadFile(keyFile)
		if err != nil {
			return Config{}, fmt.Errorf("cannot read GITHUB_APP_PRIVATE_KEY_FILE: %w", err)
		}
		keyText = string(raw)
	} else {
		keyText = os.Getenv("GITHUB_APP_PRIVATE_KEY")
		if strings.Contains(keyText, `\\n`) && !strings.Contains(keyText, "\\n") {
			keyText = strings.ReplaceAll(keyText, `\\n`, "\\n")
		}
	}
	if appID == "" || strings.TrimSpace(keyText) == "" {
		return Config{}, fmt.Errorf("missing GitHub App runtime configuration")
	}
	key, err := ParseRSAPrivateKey([]byte(keyText))
	if err != nil {
		return Config{}, err
	}
	apiBase := strings.TrimRight(strings.TrimSpace(os.Getenv("GITHUB_API_BASE")), "/")
	if apiBase == "" {
		apiBase = defaultAPIBase
	}
	if _, err := url.ParseRequestURI(apiBase); err != nil {
		return Config{}, fmt.Errorf("invalid GitHub API base")
	}
	return Config{AppID: appID, PrivateKey: key, APIBase: apiBase}, nil
}

func ParseRSAPrivateKey(raw []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("GitHub App private key is not PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("GitHub App private key is not PKCS1/PKCS8 RSA")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("GitHub App private key is not RSA")
	}
	return key, nil
}

func New(config Config) (*Client, error) {
	if strings.TrimSpace(config.AppID) == "" {
		return nil, fmt.Errorf("GitHub App id is required")
	}
	if config.PrivateKey == nil {
		return nil, fmt.Errorf("GitHub App private key is required")
	}
	if config.APIBase == "" {
		config.APIBase = defaultAPIBase
	}
	config.APIBase = strings.TrimRight(config.APIBase, "/")
	if _, err := url.ParseRequestURI(config.APIBase); err != nil {
		return nil, fmt.Errorf("invalid GitHub API base")
	}
	return &Client{
		Config: config,
		HTTP:   &http.Client{Timeout: 20 * time.Second},
		Now:    time.Now,
	}, nil
}

func base64URL(raw []byte) string {
	return base64.RawURLEncoding.EncodeToString(raw)
}

func (c *Client) AppJWT() (string, error) {
	if c == nil || c.Config.PrivateKey == nil {
		return "", fmt.Errorf("GitHub App client is not configured")
	}
	now := c.Now
	if now == nil {
		now = time.Now
	}
	current := now().Unix()
	header := map[string]any{"alg": "RS256", "typ": "JWT"}
	payload := map[string]any{
		"iat": current - 60,
		"exp": current + 9*60,
		"iss": c.Config.AppID,
	}
	headerRaw, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	payloadRaw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	unsigned := base64URL(headerRaw) + "." + base64URL(payloadRaw)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, c.Config.PrivateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign GitHub App JWT: %w", err)
	}
	return unsigned + "." + base64URL(signature), nil
}

func validateRepository(repository string) (string, string, error) {
	if strings.Count(repository, "/") != 1 || strings.TrimSpace(repository) != repository {
		return "", "", fmt.Errorf("repository must be owner/name")
	}
	owner, repo, _ := strings.Cut(repository, "/")
	if owner == "" || repo == "" {
		return "", "", fmt.Errorf("repository must be owner/name")
	}
	return owner, repo, nil
}

func (c *Client) doJSON(ctx context.Context, method, path, token string, body any, expected string) (any, error) {
	if expected != "object" && expected != "array" {
		return nil, fmt.Errorf("invalid expected shape")
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Config.APIBase+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "semper-supra-wow-sidecar/0.0.0-dev")

	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub App API failed: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("read GitHub App response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := string(raw)
		if len(detail) > 2000 {
			detail = detail[:2000]
		}
		return nil, fmt.Errorf("GitHub App API failed: %s %s: HTTP %d: %s", method, path, resp.StatusCode, detail)
	}
	if len(raw) == 0 {
		if expected == "object" {
			return map[string]any{}, nil
		}
		return []any{}, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("GitHub returned malformed JSON for %s %s", method, path)
	}
	if expected == "object" {
		if _, ok := value.(map[string]any); !ok {
			return nil, fmt.Errorf("GitHub returned unexpected non-object JSON for %s %s", method, path)
		}
		return value, nil
	}
	if _, ok := value.([]any); !ok {
		return nil, fmt.Errorf("GitHub returned unexpected non-array JSON for %s %s", method, path)
	}
	return value, nil
}

func (c *Client) InstallationIDForRepository(ctx context.Context, repository string) (int64, error) {
	owner, repo, err := validateRepository(repository)
	if err != nil {
		return 0, err
	}
	jwt, err := c.AppJWT()
	if err != nil {
		return 0, err
	}
	value, err := c.doJSON(ctx, http.MethodGet, "/repos/"+owner+"/"+repo+"/installation", jwt, nil, "object")
	if err != nil {
		return 0, err
	}
	object := value.(map[string]any)
	idValue, ok := object["id"].(float64)
	if !ok || idValue <= 0 || idValue != float64(int64(idValue)) {
		return 0, fmt.Errorf("GitHub App installation id is missing for %s", repository)
	}
	return int64(idValue), nil
}

func (c *Client) InstallationTokenForRepository(ctx context.Context, repository string) (string, error) {
	_, repo, err := validateRepository(repository)
	if err != nil {
		return "", err
	}
	installationID, err := c.InstallationIDForRepository(ctx, repository)
	if err != nil {
		return "", err
	}
	jwt, err := c.AppJWT()
	if err != nil {
		return "", err
	}
	value, err := c.doJSON(
		ctx,
		http.MethodPost,
		fmt.Sprintf("/app/installations/%d/access_tokens", installationID),
		jwt,
		map[string]any{"repositories": []string{repo}},
		"object",
	)
	if err != nil {
		return "", err
	}
	token, ok := value.(map[string]any)["token"].(string)
	if !ok || token == "" {
		return "", fmt.Errorf("GitHub did not return an installation access token")
	}
	return token, nil
}
