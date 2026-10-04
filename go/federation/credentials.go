package federation

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	PeerCredentialsSchema = "wow-sidecar.peer-credentials.v1"
	maxPeerCredentialFile  = 16 * 1024
	maxPeerCredentials     = 8
)

type peerCredentialDocument struct {
	Schema string                `json:"schema"`
	Peers  []peerCredentialEntry `json:"peers"`
}

type peerCredentialEntry struct {
	NodeID string `json:"node_id"`
	Key    string `json:"key"`
}

type PeerCredentials struct {
	keys map[string][]byte
}

func LoadPeerCredentials(path string) (*PeerCredentials, error) {
	if path == "" || strings.TrimSpace(path) != path {
		return nil, fmt.Errorf("peer credential path must be normalized and non-empty")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("peer credential path must be absolute")
	}

	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat peer credential file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("peer credential file must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("peer credential file must be regular")
	}
	if info.Size() <= 0 || info.Size() > maxPeerCredentialFile {
		return nil, fmt.Errorf("peer credential file size is outside bounds")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("peer credential file must not be group/world accessible")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read peer credential file: %w", err)
	}
	if len(raw) == 0 || len(raw) > maxPeerCredentialFile {
		return nil, fmt.Errorf("peer credential file size is outside bounds")
	}

	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var doc peerCredentialDocument
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode peer credential file: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("peer credential file contains trailing JSON")
		}
		return nil, fmt.Errorf("decode peer credential trailing data: %w", err)
	}
	if doc.Schema != PeerCredentialsSchema {
		return nil, fmt.Errorf("unsupported peer credential schema")
	}
	if len(doc.Peers) == 0 || len(doc.Peers) > maxPeerCredentials {
		return nil, fmt.Errorf("peer credential file must contain 1-%d peers", maxPeerCredentials)
	}

	keys := make(map[string][]byte, len(doc.Peers))
	for _, entry := range doc.Peers {
		if !peerNodeIDRE.MatchString(entry.NodeID) {
			return nil, fmt.Errorf("invalid peer credential node_id")
		}
		if _, exists := keys[entry.NodeID]; exists {
			return nil, fmt.Errorf("duplicate peer credential node_id")
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(entry.Key)
		if err != nil {
			return nil, fmt.Errorf("invalid peer credential key encoding")
		}
		if len(decoded) < 32 || len(decoded) > 128 {
			return nil, fmt.Errorf("peer credential key must decode to 32-128 bytes")
		}
		keys[entry.NodeID] = append([]byte(nil), decoded...)
	}

	return &PeerCredentials{keys: keys}, nil
}

func (c *PeerCredentials) Key(nodeID string) ([]byte, error) {
	if c == nil {
		return nil, fmt.Errorf("peer credentials are unavailable")
	}
	if !peerNodeIDRE.MatchString(nodeID) {
		return nil, fmt.Errorf("invalid peer node_id")
	}
	key, ok := c.keys[nodeID]
	if !ok {
		return nil, fmt.Errorf("peer credential not found")
	}
	return append([]byte(nil), key...), nil
}

func (c *PeerCredentials) Count() int {
	if c == nil {
		return 0
	}
	return len(c.keys)
}
