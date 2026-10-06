package federation

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCredentialFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "peer-credentials.json")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func validCredentialJSON(nodeID string, key []byte) string {
	return `{"schema":"` + PeerCredentialsSchema + `","peers":[{"node_id":"` + nodeID + `","key":"` +
		base64.StdEncoding.EncodeToString(key) + `"}]}`
}

func TestLoadPeerCredentialsValidAndCopySafe(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	path := writeCredentialFile(t, validCredentialJSON("oci-edge-node", key), 0o600)

	creds, err := LoadPeerCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if creds.Count() != 1 {
		t.Fatalf("count=%d", creds.Count())
	}
	first, err := creds.Key("oci-edge-node")
	if err != nil {
		t.Fatal(err)
	}
	first[0] ^= 0xff
	second, err := creds.Key("oci-edge-node")
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != string(key) {
		t.Fatal("returned key mutated stored credential")
	}
}

func TestLoadPeerCredentialsRejectsRelativeSymlinkAndLoosePermissions(t *testing.T) {
	if _, err := LoadPeerCredentials("relative.json"); err == nil {
		t.Fatal("relative path unexpectedly accepted")
	}
	if _, err := LoadPeerCredentials("/tmp/../tmp/peer-credentials.json"); err == nil {
		t.Fatal("non-normalized absolute path unexpectedly accepted")
	}

	key := []byte("0123456789abcdef0123456789abcdef")
	target := writeCredentialFile(t, validCredentialJSON("oci-edge-node", key), 0o600)
	link := filepath.Join(filepath.Dir(target), "peer-link.json")
	if err := os.Symlink(target, link); err == nil {
		if _, err := LoadPeerCredentials(link); err == nil {
			t.Fatal("symlink unexpectedly accepted")
		}
	}

	if err := os.Chmod(target, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPeerCredentials(target); err == nil {
		t.Fatal("group-readable credential unexpectedly accepted")
	}
}

func TestLoadPeerCredentialsRejectsSchemaAndUnknownFields(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cases := []string{
		`{"schema":"wrong","peers":[{"node_id":"oci-edge-node","key":"` + key + `"}]}`,
		`{"schema":"` + PeerCredentialsSchema + `","peers":[{"node_id":"oci-edge-node","key":"` + key + `","extra":true}]}`,
		`{"schema":"` + PeerCredentialsSchema + `","peers":[{"node_id":"oci-edge-node","key":"` + key + `"}],"extra":true}`,
		`{"schema":"` + PeerCredentialsSchema + `","peers":[{"node_id":"oci-edge-node","key":"` + key + `"}]}{}`,
	}
	for i, content := range cases {
		path := writeCredentialFile(t, content, 0o600)
		if _, err := LoadPeerCredentials(path); err == nil {
			t.Fatalf("case %d unexpectedly accepted", i)
		}
	}
}

func TestLoadPeerCredentialsRejectsDuplicateInvalidAndMissingPeers(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	duplicate := `{"schema":"` + PeerCredentialsSchema + `","peers":[` +
		`{"node_id":"oci-edge-node","key":"` + key + `"},` +
		`{"node_id":"oci-edge-node","key":"` + key + `"}]}`
	for _, content := range []string{
		duplicate,
		`{"schema":"` + PeerCredentialsSchema + `","peers":[]}`,
		`{"schema":"` + PeerCredentialsSchema + `","peers":[{"node_id":"BAD NODE","key":"` + key + `"}]}`,
	} {
		path := writeCredentialFile(t, content, 0o600)
		if _, err := LoadPeerCredentials(path); err == nil {
			t.Fatal("invalid peer set unexpectedly accepted")
		}
	}
}

func TestLoadPeerCredentialsRejectsBadKeyMaterial(t *testing.T) {
	short := base64.StdEncoding.EncodeToString([]byte("too-short"))
	long := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 129)))
	for _, encoded := range []string{"not-base64!", short, long, " " + base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))} {
		content := `{"schema":"` + PeerCredentialsSchema + `","peers":[{"node_id":"oci-edge-node","key":"` + encoded + `"}]}`
		path := writeCredentialFile(t, content, 0o600)
		if _, err := LoadPeerCredentials(path); err == nil {
			t.Fatal("invalid key material unexpectedly accepted")
		}
	}
}

func TestLoadPeerCredentialsRejectsOversizedFileAndMissingLookup(t *testing.T) {
	oversized := strings.Repeat("x", maxPeerCredentialFile+1)
	path := writeCredentialFile(t, oversized, 0o600)
	if _, err := LoadPeerCredentials(path); err == nil {
		t.Fatal("oversized file unexpectedly accepted")
	}

	key := []byte("0123456789abcdef0123456789abcdef")
	path = writeCredentialFile(t, validCredentialJSON("oci-edge-node", key), 0o600)
	creds, err := LoadPeerCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Key("truenas-node"); err == nil {
		t.Fatal("missing peer lookup unexpectedly succeeded")
	}
}

func TestLoadPeerCredentialsRejectsTooManyPeers(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	parts := make([]string, 0, maxPeerCredentials+1)
	for i := 0; i < maxPeerCredentials+1; i++ {
		parts = append(parts, `{"node_id":"peer-`+string(rune('a'+i))+ `-node","key":"`+key+`"}`)
	}
	content := `{"schema":"` + PeerCredentialsSchema + `","peers":[` + strings.Join(parts, ",") + `]}`
	path := writeCredentialFile(t, content, 0o600)
	if _, err := LoadPeerCredentials(path); err == nil {
		t.Fatal("too many peers unexpectedly accepted")
	}
}
