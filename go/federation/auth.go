package federation

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/SemperSupra/wow-sidecar/go/embodiment"
	"github.com/SemperSupra/wow-sidecar/go/protocol"
)

const PeerEnvelopeSchema = "wow-sidecar.peer-envelope.v1"

var (
	peerNodeIDRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{2,127}$`)
	peerRequestIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{7,79}$`)
	peerDigestRE    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	peerSignatureRE = regexp.MustCompile(`^hmac-sha256:[0-9a-f]{64}$`)
	peerIncarnationRE = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

type PeerEnvelope struct {
	Schema              string `json:"schema"`
	SourceNodeID        string `json:"source_node_id"`
	SourceGeneration    uint64 `json:"source_generation"`
	SourceIncarnationID string `json:"source_incarnation_id"`
	DestinationNodeID   string `json:"destination_node_id"`
	RequestID           string `json:"request_id"`
	IssuedAt            string `json:"issued_at"`
	Operation           string `json:"operation"`
	PayloadDigest       string `json:"payload_digest"`
	Signature           string `json:"signature"`
}

type peerEnvelopeUnsigned struct {
	Schema              string `json:"schema"`
	SourceNodeID        string `json:"source_node_id"`
	SourceGeneration    uint64 `json:"source_generation"`
	SourceIncarnationID string `json:"source_incarnation_id"`
	DestinationNodeID   string `json:"destination_node_id"`
	RequestID           string `json:"request_id"`
	IssuedAt            string `json:"issued_at"`
	Operation           string `json:"operation"`
	PayloadDigest       string `json:"payload_digest"`
}

func PayloadDigest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func DecodePeerEnvelope(data []byte) (PeerEnvelope, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var env PeerEnvelope
	if err := dec.Decode(&env); err != nil {
		return PeerEnvelope{}, fmt.Errorf("decode peer envelope: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return PeerEnvelope{}, fmt.Errorf("peer envelope contains trailing JSON")
		}
		return PeerEnvelope{}, fmt.Errorf("decode peer envelope trailing data: %w", err)
	}
	if err := validatePeerEnvelopeFields(env); err != nil {
		return PeerEnvelope{}, err
	}
	if !peerSignatureRE.MatchString(env.Signature) {
		return PeerEnvelope{}, fmt.Errorf("signature is required")
	}
	return env, nil
}

func validatePeerEnvelopeFields(env PeerEnvelope) error {
	if env.Schema != PeerEnvelopeSchema {
		return fmt.Errorf("unsupported peer envelope schema")
	}
	if !peerNodeIDRE.MatchString(env.SourceNodeID) {
		return fmt.Errorf("invalid source_node_id")
	}
	if !peerNodeIDRE.MatchString(env.DestinationNodeID) {
		return fmt.Errorf("invalid destination_node_id")
	}
	if env.SourceGeneration == 0 {
		return fmt.Errorf("source_generation must be positive")
	}
	if !peerIncarnationRE.MatchString(env.SourceIncarnationID) {
		return fmt.Errorf("invalid source_incarnation_id")
	}
	if !peerRequestIDRE.MatchString(env.RequestID) {
		return fmt.Errorf("invalid request_id")
	}
	if strings.TrimSpace(env.Operation) != env.Operation || env.Operation == "" {
		return fmt.Errorf("operation must be normalized and non-empty")
	}
	switch env.Operation {
	case "rendezvous.claim", "rendezvous.complete":
	default:
		return fmt.Errorf("unsupported peer operation")
	}
	if !peerDigestRE.MatchString(env.PayloadDigest) {
		return fmt.Errorf("invalid payload_digest")
	}
	if env.Signature != "" && !peerSignatureRE.MatchString(env.Signature) {
		return fmt.Errorf("invalid signature")
	}
	if _, err := time.Parse(time.RFC3339Nano, env.IssuedAt); err != nil {
		return fmt.Errorf("invalid issued_at")
	}
	return nil
}

func unsignedEnvelope(env PeerEnvelope) peerEnvelopeUnsigned {
	return peerEnvelopeUnsigned{
		Schema:              env.Schema,
		SourceNodeID:        env.SourceNodeID,
		SourceGeneration:    env.SourceGeneration,
		SourceIncarnationID: env.SourceIncarnationID,
		DestinationNodeID:   env.DestinationNodeID,
		RequestID:           env.RequestID,
		IssuedAt:            env.IssuedAt,
		Operation:           env.Operation,
		PayloadDigest:       env.PayloadDigest,
	}
}

func SignPeerEnvelope(env PeerEnvelope, key []byte) (PeerEnvelope, error) {
	if len(key) < 32 || len(key) > 128 {
		return PeerEnvelope{}, fmt.Errorf("peer key must be 32-128 bytes")
	}
	env.Signature = ""
	if err := validatePeerEnvelopeFields(env); err != nil {
		return PeerEnvelope{}, err
	}
	raw, err := protocol.CanonicalJSON(unsignedEnvelope(env))
	if err != nil {
		return PeerEnvelope{}, fmt.Errorf("canonicalize peer envelope: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(raw)
	env.Signature = "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
	return env, nil
}

func VerifyPeerEnvelope(
	env PeerEnvelope,
	key []byte,
	expectedDestination string,
	currentPeer embodiment.Card,
	now time.Time,
	maxSkew time.Duration,
	payload []byte,
) error {
	if len(key) < 32 || len(key) > 128 {
		return fmt.Errorf("peer key must be 32-128 bytes")
	}
	if maxSkew <= 0 || maxSkew > 10*time.Minute {
		return fmt.Errorf("max skew must be between 1ns and 10m")
	}
	if err := validatePeerEnvelopeFields(env); err != nil {
		return err
	}
	if !peerSignatureRE.MatchString(env.Signature) {
		return fmt.Errorf("signature is required")
	}
	if env.DestinationNodeID != expectedDestination {
		return fmt.Errorf("peer envelope destination mismatch")
	}
	if err := currentPeer.Validate(); err != nil {
		return fmt.Errorf("current peer card invalid: %w", err)
	}
	if env.SourceNodeID != currentPeer.NodeID {
		return fmt.Errorf("peer envelope source node mismatch")
	}
	if env.SourceGeneration != currentPeer.Generation {
		return fmt.Errorf("peer envelope source generation mismatch")
	}
	if env.SourceIncarnationID != currentPeer.IncarnationID {
		return fmt.Errorf("peer envelope source incarnation mismatch")
	}
	leaseExpiry, _ := currentPeer.LeaseExpiry()
	if !now.Before(leaseExpiry) {
		return fmt.Errorf("peer card lease is expired")
	}
	issuedAt, _ := time.Parse(time.RFC3339Nano, env.IssuedAt)
	if issuedAt.Before(now.Add(-maxSkew)) || issuedAt.After(now.Add(maxSkew)) {
		return fmt.Errorf("peer envelope is outside freshness window")
	}
	if env.PayloadDigest != PayloadDigest(payload) {
		return fmt.Errorf("peer envelope payload digest mismatch")
	}

	raw, err := protocol.CanonicalJSON(unsignedEnvelope(env))
	if err != nil {
		return fmt.Errorf("canonicalize peer envelope: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(raw)
	expected := mac.Sum(nil)
	actual, err := hex.DecodeString(strings.TrimPrefix(env.Signature, "hmac-sha256:"))
	if err != nil || !hmac.Equal(actual, expected) {
		return fmt.Errorf("peer envelope signature mismatch")
	}
	return nil
}

const maxReplayEntries = 4096

type replayKey struct {
	SourceNodeID     string
	SourceGeneration uint64
	RequestID        string
}

type replayEntry struct {
	Signature string
	ExpiresAt time.Time
}

type ReplayCache struct {
	mu      sync.Mutex
	entries map[replayKey]replayEntry
}

func NewReplayCache() *ReplayCache {
	return &ReplayCache{entries: map[replayKey]replayEntry{}}
}

func (c *ReplayCache) Admit(
	sourceNodeID string,
	sourceGeneration uint64,
	requestID string,
	signature string,
	now time.Time,
	ttl time.Duration,
) (bool, error) {
	if c == nil {
		return false, fmt.Errorf("replay cache is required")
	}
	if !peerNodeIDRE.MatchString(sourceNodeID) {
		return false, fmt.Errorf("invalid source_node_id")
	}
	if sourceGeneration == 0 {
		return false, fmt.Errorf("source_generation must be positive")
	}
	if !peerRequestIDRE.MatchString(requestID) {
		return false, fmt.Errorf("invalid request_id")
	}
	if !peerSignatureRE.MatchString(signature) {
		return false, fmt.Errorf("invalid peer request signature")
	}
	if ttl <= 0 || ttl > 30*time.Minute {
		return false, fmt.Errorf("replay ttl must be between 1ns and 30m")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for key, entry := range c.entries {
		if !now.Before(entry.ExpiresAt) {
			delete(c.entries, key)
		}
	}
	key := replayKey{
		SourceNodeID:     sourceNodeID,
		SourceGeneration: sourceGeneration,
		RequestID:        requestID,
	}
	if entry, exists := c.entries[key]; exists && now.Before(entry.ExpiresAt) {
		if entry.Signature != signature {
			return false, fmt.Errorf("peer request_id reused with a different signed message")
		}
		return true, nil
	}
	if len(c.entries) >= maxReplayEntries {
		return false, fmt.Errorf("peer replay cache capacity exceeded")
	}
	c.entries[key] = replayEntry{Signature: signature, ExpiresAt: now.Add(ttl)}
	return false, nil
}
