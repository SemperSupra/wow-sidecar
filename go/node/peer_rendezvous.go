package node

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SemperSupra/wow-sidecar/go/capability"
	"github.com/SemperSupra/wow-sidecar/go/embodiment"
	"github.com/SemperSupra/wow-sidecar/go/federation"
)

const maxPeerRendezvousRequestBytes = 16 * 1024

type PeerAuthorityVerifier func(context.Context, string, string, string) error
type CurrentPeerLookup func(string) (embodiment.Card, bool)

type PeerRendezvousHandlerConfig struct {
	DestinationCard embodiment.Card
	CurrentPeer     CurrentPeerLookup
	Credentials       *federation.PeerCredentials
	VerifyAuthority   PeerAuthorityVerifier
	Rendezvous        *RendezvousService
	Replay            *federation.ReplayCache
	Now               func() time.Time
	MaxSkew           time.Duration
	ReplayTTL         time.Duration
}

type peerRendezvousAuthority struct {
	Record   string `json:"record"`
	Revision string `json:"revision"`
	State    string `json:"state"`
}

type peerRendezvousRequest struct {
	Envelope  federation.PeerEnvelope `json:"envelope"`
	Authority peerRendezvousAuthority `json:"authority"`
	Payload   json.RawMessage          `json:"payload"`
}

type peerRendezvousResponse struct {
	Result            string         `json:"result"`
	Status            string         `json:"status"`
	MutationPerformed bool           `json:"mutation_performed"`
	Duplicate         bool           `json:"duplicate"`
	Data              map[string]any `json:"data"`
}

type PeerRendezvousHandler struct {
	config PeerRendezvousHandlerConfig
}

func NewPeerRendezvousHandler(config PeerRendezvousHandlerConfig) (*PeerRendezvousHandler, error) {
	if err := config.DestinationCard.Validate(); err != nil {
		return nil, fmt.Errorf("destination card is invalid: %w", err)
	}
	if config.CurrentPeer == nil {
		return nil, fmt.Errorf("current peer lookup is required")
	}
	if config.Credentials == nil {
		return nil, fmt.Errorf("peer credentials are required")
	}
	if config.VerifyAuthority == nil {
		return nil, fmt.Errorf("canonical authority verifier is required")
	}
	if config.Rendezvous == nil || config.Rendezvous.store == nil {
		return nil, fmt.Errorf("rendezvous service is required")
	}
	if config.Replay == nil {
		return nil, fmt.Errorf("replay cache is required")
	}
	if config.Now == nil {
		return nil, fmt.Errorf("clock is required")
	}
	if config.MaxSkew <= 0 || config.MaxSkew > 10*time.Minute {
		return nil, fmt.Errorf("max skew must be between 1ns and 10m")
	}
	if config.ReplayTTL <= 0 || config.ReplayTTL > 30*time.Minute {
		return nil, fmt.Errorf("replay ttl must be between 1ns and 30m")
	}
	return &PeerRendezvousHandler{config: config}, nil
}

func decodePeerRendezvousRequest(raw []byte) (peerRendezvousRequest, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var request peerRendezvousRequest
	if err := decoder.Decode(&request); err != nil {
		return peerRendezvousRequest{}, fmt.Errorf("decode peer rendezvous request: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return peerRendezvousRequest{}, fmt.Errorf("peer rendezvous request contains trailing JSON")
		}
		return peerRendezvousRequest{}, fmt.Errorf("peer rendezvous request contains trailing data")
	}
	if strings.TrimSpace(request.Authority.Record) == "" ||
		request.Authority.Record != strings.TrimSpace(request.Authority.Record) {
		return peerRendezvousRequest{}, fmt.Errorf("authority record is required")
	}
	if !controlAuthorityRevisionRE.MatchString(request.Authority.Revision) {
		return peerRendezvousRequest{}, fmt.Errorf("authority revision is invalid")
	}
	if request.Authority.State != "open" {
		return peerRendezvousRequest{}, fmt.Errorf("authority state must be open")
	}
	if len(request.Payload) == 0 {
		return peerRendezvousRequest{}, fmt.Errorf("payload is required")
	}
	return request, nil
}

func validatePeerRendezvousPayload(operation string, raw json.RawMessage) error {
	switch operation {
	case "rendezvous.claim":
		input, err := capability.DecodeStrictInput[rendezvousClaimInput](raw)
		if err != nil {
			return err
		}
		if input.Operation != "claim" {
			return fmt.Errorf("peer operation/payload mismatch")
		}
	case "rendezvous.complete":
		input, err := capability.DecodeStrictInput[rendezvousCompleteInput](raw)
		if err != nil {
			return err
		}
		if input.Operation != "complete" {
			return fmt.Errorf("peer operation/payload mismatch")
		}
	default:
		return fmt.Errorf("unsupported peer rendezvous operation")
	}
	return nil
}

func (h *PeerRendezvousHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if req.Context() == nil {
		http.Error(w, "bad peer request", http.StatusBadRequest)
		return
	}

	raw, err := io.ReadAll(io.LimitReader(req.Body, maxPeerRendezvousRequestBytes+1))
	if err != nil {
		http.Error(w, "bad peer request", http.StatusBadRequest)
		return
	}
	if len(raw) == 0 || len(raw) > maxPeerRendezvousRequestBytes {
		http.Error(w, "peer request too large", http.StatusRequestEntityTooLarge)
		return
	}
	wire, err := decodePeerRendezvousRequest(raw)
	if err != nil {
		http.Error(w, "bad peer request", http.StatusBadRequest)
		return
	}

	currentPeer, ok := h.config.CurrentPeer(wire.Envelope.SourceNodeID)
	if !ok {
		http.Error(w, "peer authentication failed", http.StatusUnauthorized)
		return
	}
	key, err := h.config.Credentials.Key(wire.Envelope.SourceNodeID)
	if err != nil {
		http.Error(w, "peer authentication failed", http.StatusUnauthorized)
		return
	}
	now := h.config.Now().UTC()
	if err := federation.VerifyPeerEnvelope(
		wire.Envelope,
		key,
		h.config.DestinationCard,
		currentPeer,
		now,
		h.config.MaxSkew,
		wire.Payload,
	); err != nil {
		http.Error(w, "peer authentication failed", http.StatusUnauthorized)
		return
	}

	if err := validatePeerRendezvousPayload(wire.Envelope.Operation, wire.Payload); err != nil {
		http.Error(w, "bad peer operation", http.StatusBadRequest)
		return
	}

	if err := h.config.VerifyAuthority(
		req.Context(),
		wire.Authority.Record,
		wire.Authority.Revision,
		wire.Authority.State,
	); err != nil {
		http.Error(w, "canonical authority unavailable", http.StatusServiceUnavailable)
		return
	}

	duplicate, err := h.config.Replay.Admit(
		wire.Envelope.SourceNodeID,
		wire.Envelope.SourceGeneration,
		wire.Envelope.RequestID,
		wire.Envelope.Signature,
		now,
		h.config.ReplayTTL,
	)
	if err != nil {
		http.Error(w, "peer replay rejected", http.StatusConflict)
		return
	}

	outcome, err := h.config.Rendezvous.invoke(
		req.Context(),
		capability.Request{
			CapabilityID:      rendezvousCapabilityID,
			AuthorityRef:      wire.Authority.Record,
			AuthorityRevision: wire.Authority.Revision,
			AuthorityState:    wire.Authority.State,
			Input:             wire.Payload,
		},
	)
	if err != nil {
		http.Error(w, "rendezvous operation not accepted", http.StatusConflict)
		return
	}

	writeJSON(w, http.StatusOK, peerRendezvousResponse{
		Result:            outcome.Result,
		Status:            outcome.Status,
		MutationPerformed: outcome.MutationPerformed,
		Duplicate:         duplicate,
		Data:              outcome.Data,
	})
}
