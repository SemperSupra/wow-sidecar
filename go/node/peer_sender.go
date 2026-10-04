package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SemperSupra/wow-sidecar/go/embodiment"
	"github.com/SemperSupra/wow-sidecar/go/federation"
)

const maxPeerRendezvousResponseBytes = 16 * 1024

type PeerRendezvousSenderConfig struct {
	SourceCard        embodiment.Card
	DestinationNodeID string
	Credentials       *federation.PeerCredentials
	Client            *http.Client
	Now               func() time.Time
}

type PeerRendezvousSendRequest struct {
	Endpoint          string
	RequestID         string
	Operation         string
	AuthorityRecord   string
	AuthorityRevision string
	AuthorityState    string
	Payload           json.RawMessage
}

type PeerRendezvousResult struct {
	Result            string         `json:"result"`
	Status            string         `json:"status"`
	MutationPerformed bool           `json:"mutation_performed"`
	Duplicate         bool           `json:"duplicate"`
	Data              map[string]any `json:"data"`
}

type PeerRendezvousSender struct {
	config PeerRendezvousSenderConfig
}

func NewPeerRendezvousSender(config PeerRendezvousSenderConfig) (*PeerRendezvousSender, error) {
	if err := config.SourceCard.Validate(); err != nil {
		return nil, fmt.Errorf("source card is invalid: %w", err)
	}
	if strings.TrimSpace(config.DestinationNodeID) == "" ||
		config.DestinationNodeID != strings.TrimSpace(config.DestinationNodeID) {
		return nil, fmt.Errorf("destination node id is required")
	}
	if config.SourceCard.NodeID == config.DestinationNodeID {
		return nil, fmt.Errorf("source and destination node ids must differ")
	}
	if config.Credentials == nil {
		return nil, fmt.Errorf("peer credentials are required")
	}
	if config.Now == nil {
		return nil, fmt.Errorf("clock is required")
	}
	if config.Client == nil {
		config.Client = &http.Client{}
	} else {
		copy := *config.Client
		config.Client = &copy
	}
	if config.Client.Timeout <= 0 {
		config.Client.Timeout = 10 * time.Second
	}
	config.Client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return fmt.Errorf("peer rendezvous redirects are prohibited")
	}
	return &PeerRendezvousSender{config: config}, nil
}

func normalizePeerRendezvousEndpoint(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", fmt.Errorf("peer rendezvous endpoint must be normalized and non-empty")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid peer rendezvous endpoint")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("peer rendezvous endpoint scheme must be http or https")
	}
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("peer rendezvous endpoint must not contain credentials, query, or fragment")
	}
	if parsed.Path == "" || parsed.Path == "/" {
		return "", fmt.Errorf("peer rendezvous endpoint requires an explicit path")
	}
	return parsed.String(), nil
}

func validatePeerSendRequest(request PeerRendezvousSendRequest) (string, error) {
	endpoint, err := normalizePeerRendezvousEndpoint(request.Endpoint)
	if err != nil {
		return "", err
	}
	if !peerRequestIDREForNode(request.RequestID) {
		return "", fmt.Errorf("invalid peer request id")
	}
	if request.Operation != "rendezvous.claim" && request.Operation != "rendezvous.complete" {
		return "", fmt.Errorf("unsupported peer rendezvous operation")
	}
	if strings.TrimSpace(request.AuthorityRecord) == "" ||
		request.AuthorityRecord != strings.TrimSpace(request.AuthorityRecord) {
		return "", fmt.Errorf("authority record is required")
	}
	if !controlAuthorityRevisionRE.MatchString(request.AuthorityRevision) {
		return "", fmt.Errorf("authority revision is invalid")
	}
	if request.AuthorityState != "open" {
		return "", fmt.Errorf("authority state must be open")
	}
	if len(request.Payload) == 0 {
		return "", fmt.Errorf("payload is required")
	}
	if err := validatePeerRendezvousPayload(request.Operation, request.Payload); err != nil {
		return "", err
	}
	return endpoint, nil
}

// peerRequestIDREForNode intentionally mirrors the federation request-id contract
// without exporting federation's internal regexp as a new public protocol surface.
func peerRequestIDREForNode(value string) bool {
	if len(value) < 8 || len(value) > 80 {
		return false
	}
	for index, r := range value {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
		if !valid {
			return false
		}
		if index == 0 && !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

func decodePeerRendezvousResponse(raw []byte) (PeerRendezvousResult, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var response PeerRendezvousResult
	if err := decoder.Decode(&response); err != nil {
		return PeerRendezvousResult{}, fmt.Errorf("decode peer rendezvous response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return PeerRendezvousResult{}, fmt.Errorf("peer rendezvous response contains trailing JSON")
		}
		return PeerRendezvousResult{}, fmt.Errorf("peer rendezvous response contains trailing data")
	}
	if response.Result != "ELIGIBLE" && response.Result != "REJECTED" && response.Result != "UNKNOWN" {
		return PeerRendezvousResult{}, fmt.Errorf("peer rendezvous response result is invalid")
	}
	if strings.TrimSpace(response.Status) == "" || response.Status != strings.TrimSpace(response.Status) {
		return PeerRendezvousResult{}, fmt.Errorf("peer rendezvous response status is invalid")
	}
	if response.Data == nil {
		return PeerRendezvousResult{}, fmt.Errorf("peer rendezvous response data is required")
	}
	allowedData := map[string]bool{
		"receipt_status":    true,
		"handoff_id":        true,
		"handoff_status":    true,
		"actor_instance_id": true,
		"lease_expires_at":  true,
		"completed_at":      true,
	}
	for key := range response.Data {
		if !allowedData[key] {
			return PeerRendezvousResult{}, fmt.Errorf("peer rendezvous response contains unsupported data field")
		}
	}
	return response, nil
}

func (s *PeerRendezvousSender) Send(
	ctx context.Context,
	request PeerRendezvousSendRequest,
) (PeerRendezvousResult, error) {
	if s == nil {
		return PeerRendezvousResult{}, fmt.Errorf("peer rendezvous sender is nil")
	}
	if ctx == nil {
		return PeerRendezvousResult{}, fmt.Errorf("context is required")
	}
	endpoint, err := validatePeerSendRequest(request)
	if err != nil {
		return PeerRendezvousResult{}, err
	}
	key, err := s.config.Credentials.Key(s.config.DestinationNodeID)
	if err != nil {
		return PeerRendezvousResult{}, fmt.Errorf("destination peer credential unavailable")
	}
	now := s.config.Now().UTC()
	envelope, err := federation.SignPeerEnvelope(federation.PeerEnvelope{
		Schema:              federation.PeerEnvelopeSchema,
		SourceNodeID:        s.config.SourceCard.NodeID,
		SourceGeneration:    s.config.SourceCard.Generation,
		SourceIncarnationID: s.config.SourceCard.IncarnationID,
		DestinationNodeID:   s.config.DestinationNodeID,
		RequestID:           request.RequestID,
		IssuedAt:            now.Format(time.RFC3339Nano),
		Operation:           request.Operation,
		PayloadDigest:       federation.PayloadDigest(request.Payload),
	}, key)
	if err != nil {
		return PeerRendezvousResult{}, fmt.Errorf("sign peer rendezvous request: %w", err)
	}

	wire, err := json.Marshal(peerRendezvousRequest{
		Envelope: envelope,
		Authority: peerRendezvousAuthority{
			Record:   request.AuthorityRecord,
			Revision: request.AuthorityRevision,
			State:    request.AuthorityState,
		},
		Payload: request.Payload,
	})
	if err != nil {
		return PeerRendezvousResult{}, fmt.Errorf("encode peer rendezvous request: %w", err)
	}
	if len(wire) > maxPeerRendezvousRequestBytes {
		return PeerRendezvousResult{}, fmt.Errorf("peer rendezvous request exceeds size limit")
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(wire))
	if err != nil {
		return PeerRendezvousResult{}, fmt.Errorf("create peer rendezvous request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	response, err := s.config.Client.Do(httpRequest)
	if err != nil {
		return PeerRendezvousResult{}, fmt.Errorf("peer rendezvous transport failed: %w", err)
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxPeerRendezvousResponseBytes+1))
	if err != nil {
		return PeerRendezvousResult{}, fmt.Errorf("read peer rendezvous response: %w", err)
	}
	if len(raw) > maxPeerRendezvousResponseBytes {
		return PeerRendezvousResult{}, fmt.Errorf("peer rendezvous response exceeds size limit")
	}
	if response.StatusCode != http.StatusOK {
		return PeerRendezvousResult{}, fmt.Errorf("peer rendezvous response status %d", response.StatusCode)
	}
	return decodePeerRendezvousResponse(raw)
}
