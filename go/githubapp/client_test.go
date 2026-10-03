package githubapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func pemKey(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func TestAppJWTClaimsAndSignature(t *testing.T) {
	key := testKey(t)
	client, err := New(Config{AppID: "12345", PrivateKey: key, APIBase: "https://api.example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	client.Now = func() time.Time { return now }

	token, err := client.AppJWT()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected JWT shape: %q", token)
	}
	payloadRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["iss"] != "12345" || payload["iat"] != float64(now.Unix()-60) || payload["exp"] != float64(now.Unix()+540) {
		t.Fatalf("unexpected claims: %#v", payload)
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("signature verification failed: %v", err)
	}
}

func TestRepositoryScopedInstallationTokenFlow(t *testing.T) {
	key := testKey(t)
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Fatal("missing bearer authorization")
		}
		if r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Fatal("missing API version")
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/ExampleOrg/example-repo/installation":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":77}`)
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/77/access_tokens":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			repositories, ok := body["repositories"].([]any)
			if !ok || len(repositories) != 1 || repositories[0] != "example-repo" {
				t.Fatalf("unexpected token scope: %#v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"token":"synthetic-installation-token"}`)
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

	token, err := client.InstallationTokenForRepository(context.Background(), "ExampleOrg/example-repo")
	if err != nil {
		t.Fatal(err)
	}
	if token != "synthetic-installation-token" {
		t.Fatalf("unexpected token: %q", token)
	}
	want := []string{
		"GET /repos/ExampleOrg/example-repo/installation",
		"POST /app/installations/77/access_tokens",
	}
	if len(calls) != len(want) {
		t.Fatalf("unexpected calls: %#v", calls)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("call %d: got %q want %q", i, calls[i], want[i])
		}
	}
}

func TestGenericJSONShapeValidation(t *testing.T) {
	key := testKey(t)
	responses := map[string]string{
		"/object": `{"ok":true}`,
		"/array":  `[{"id":1}]`,
		"/wrong":  `[1]`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, responses[r.URL.Path])
	}))
	defer server.Close()
	client, err := New(Config{AppID: "123", PrivateKey: key, APIBase: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := client.doJSON(context.Background(), http.MethodGet, "/object", "token", nil, "object"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.doJSON(context.Background(), http.MethodGet, "/array", "token", nil, "array"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.doJSON(context.Background(), http.MethodGet, "/wrong", "token", nil, "object"); err == nil {
		t.Fatal("wrong JSON shape was accepted")
	}
}

func TestRepositoryLocatorFailsClosed(t *testing.T) {
	for _, repository := range []string{"missing-slash", "too/many/parts", "/repo", "owner/", " owner/repo"} {
		if _, _, err := validateRepository(repository); err == nil {
			t.Fatalf("invalid repository accepted: %q", repository)
		}
	}
}

func TestConfigFromEnvPrefersFileBackedKey(t *testing.T) {
	key := testKey(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "github-app.pem")
	if err := os.WriteFile(path, pemKey(t, key), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_APP_ID", "123")
	t.Setenv("GITHUB_APP_PRIVATE_KEY_FILE", path)
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "must-not-be-used")
	t.Setenv("GITHUB_API_BASE", "https://api.example.invalid/")

	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.AppID != "123" || config.APIBase != "https://api.example.invalid" {
		t.Fatalf("unexpected config: %#v", config)
	}
}
