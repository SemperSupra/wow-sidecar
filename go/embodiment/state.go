package embodiment

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/SemperSupra/wow-sidecar/go/protocol"
)

const (
	CardSchema      = "wow-sidecar.embodiment-card.v1"
	ProtocolVersion = "wow-embodiment/v1"
)

type Presence string

const (
	PresenceOnline   Presence = "ONLINE"
	PresenceDegraded Presence = "DEGRADED"
	PresenceOffline  Presence = "OFFLINE"
	PresenceUnknown  Presence = "UNKNOWN"
)

type Reachability string

const (
	Reachable   Reachability = "REACHABLE"
	Unreachable Reachability = "UNREACHABLE"
	Unknown     Reachability = "UNKNOWN"
)

var (
	nodeIDRE       = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{2,127}$`)
	incarnationRE  = regexp.MustCompile(`^[0-9a-f]{32}$`)
	capabilityIDRE = regexp.MustCompile(`^[a-z][a-z0-9_.-]{2,127}$`)
	endpointKindRE = regexp.MustCompile(`^[a-z][a-z0-9_.-]{1,31}$`)
)

type Endpoint struct {
	Kind    string `json:"kind"`
	Address string `json:"address"`
	Auth    string `json:"auth"`
}

type Card struct {
	Schema         string     `json:"schema"`
	Protocol       string     `json:"protocol"`
	NodeID         string     `json:"node_id"`
	Generation     uint64     `json:"generation"`
	IncarnationID  string     `json:"incarnation_id"`
	Locality       string     `json:"locality"`
	Endpoints      []Endpoint `json:"endpoints"`
	Capabilities   []string   `json:"capabilities"`
	LeaseExpiresAt string     `json:"lease_expires_at"`
}

type PeerState struct {
	Card           Card
	LastObservedAt time.Time
	Reachability   Reachability
}

type ObserveResult struct {
	State      PeerState
	Superseded bool
	Duplicate  bool
}

type Tracker struct {
	peers map[string]PeerState
}

func NewTracker() *Tracker {
	return &Tracker{peers: map[string]PeerState{}}
}

func NewIncarnationID() (string, error) {
	return NewIncarnationIDFrom(rand.Reader)
}

func NewIncarnationIDFrom(source io.Reader) (string, error) {
	if source == nil {
		return "", fmt.Errorf("incarnation entropy source is required")
	}
	raw := make([]byte, 16)
	if _, err := io.ReadFull(source, raw); err != nil {
		return "", fmt.Errorf("generate incarnation id: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func NextGeneration(current uint64) (uint64, error) {
	if current == ^uint64(0) {
		return 0, fmt.Errorf("generation counter exhausted")
	}
	return current + 1, nil
}

func DecodeCard(data []byte) (Card, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var card Card
	if err := dec.Decode(&card); err != nil {
		return Card{}, fmt.Errorf("decode embodiment card: %w", err)
	}
	if dec.More() {
		return Card{}, fmt.Errorf("embodiment card contains trailing JSON")
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Card{}, fmt.Errorf("embodiment card contains trailing JSON")
		}
		return Card{}, fmt.Errorf("decode embodiment card trailing data: %w", err)
	}
	if err := card.Validate(); err != nil {
		return Card{}, err
	}
	return card, nil
}

func (c Card) Validate() error {
	if c.Schema != CardSchema {
		return fmt.Errorf("unsupported embodiment card schema")
	}
	if c.Protocol != ProtocolVersion {
		return fmt.Errorf("unsupported embodiment protocol")
	}
	if !nodeIDRE.MatchString(c.NodeID) {
		return fmt.Errorf("invalid node_id")
	}
	if c.Generation == 0 {
		return fmt.Errorf("generation must be positive")
	}
	if !incarnationRE.MatchString(c.IncarnationID) {
		return fmt.Errorf("invalid incarnation_id")
	}
	if c.Locality != "sovereign" && c.Locality != "cloud" {
		return fmt.Errorf("unsupported locality")
	}
	if _, err := c.LeaseExpiry(); err != nil {
		return err
	}

	if !sort.StringsAreSorted(c.Capabilities) {
		return fmt.Errorf("capabilities must be sorted")
	}
	for i, capability := range c.Capabilities {
		if !capabilityIDRE.MatchString(capability) {
			return fmt.Errorf("invalid capability id")
		}
		if i > 0 && capability == c.Capabilities[i-1] {
			return fmt.Errorf("duplicate capability id")
		}
	}

	for i, endpoint := range c.Endpoints {
		if !endpointKindRE.MatchString(endpoint.Kind) {
			return fmt.Errorf("invalid endpoint kind")
		}
		if strings.TrimSpace(endpoint.Address) == "" || endpoint.Address != strings.TrimSpace(endpoint.Address) {
			return fmt.Errorf("invalid endpoint address")
		}
		if strings.TrimSpace(endpoint.Auth) == "" || endpoint.Auth != strings.TrimSpace(endpoint.Auth) {
			return fmt.Errorf("invalid endpoint auth descriptor")
		}
		if i > 0 {
			prev := c.Endpoints[i-1]
			if prev.Kind > endpoint.Kind || (prev.Kind == endpoint.Kind && prev.Address >= endpoint.Address) {
				return fmt.Errorf("endpoints must be sorted and unique")
			}
		}
	}
	return nil
}

func (c Card) LeaseExpiry() (time.Time, error) {
	expiry, err := time.Parse(time.RFC3339Nano, c.LeaseExpiresAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid lease_expires_at")
	}
	return expiry, nil
}

func (c Card) CanonicalDigest() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	raw, err := protocol.CanonicalJSON(c)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (p PeerState) PresenceAt(now time.Time) Presence {
	expiry, err := p.Card.LeaseExpiry()
	if err != nil {
		return PresenceUnknown
	}
	if !now.Before(expiry) {
		return PresenceOffline
	}
	if p.Reachability == Unreachable {
		return PresenceDegraded
	}
	return PresenceOnline
}

func (t *Tracker) Get(nodeID string) (PeerState, bool) {
	if t == nil {
		return PeerState{}, false
	}
	state, ok := t.peers[nodeID]
	return state, ok
}

func (t *Tracker) Observe(card Card, observedAt time.Time, reachability Reachability) (ObserveResult, error) {
	if t == nil {
		return ObserveResult{}, fmt.Errorf("tracker is nil")
	}
	if err := card.Validate(); err != nil {
		return ObserveResult{}, err
	}
	if reachability != Reachable && reachability != Unreachable && reachability != Unknown {
		return ObserveResult{}, fmt.Errorf("invalid reachability")
	}
	if observedAt.IsZero() {
		return ObserveResult{}, fmt.Errorf("observed_at is required")
	}

	current, exists := t.peers[card.NodeID]
	if !exists {
		next := PeerState{Card: card, LastObservedAt: observedAt, Reachability: reachability}
		t.peers[card.NodeID] = next
		return ObserveResult{State: next}, nil
	}

	if card.Generation < current.Card.Generation {
		return ObserveResult{}, fmt.Errorf("stale generation")
	}
	if card.Generation == current.Card.Generation {
		if card.IncarnationID != current.Card.IncarnationID {
			return ObserveResult{}, fmt.Errorf("generation collision: different incarnation")
		}
		currentExpiry, _ := current.Card.LeaseExpiry()
		incomingExpiry, _ := card.LeaseExpiry()
		if incomingExpiry.Before(currentExpiry) {
			return ObserveResult{}, fmt.Errorf("stale lease update")
		}
		duplicate := incomingExpiry.Equal(currentExpiry) &&
			card.IncarnationID == current.Card.IncarnationID
		next := PeerState{Card: card, LastObservedAt: observedAt, Reachability: reachability}
		t.peers[card.NodeID] = next
		return ObserveResult{State: next, Duplicate: duplicate}, nil
	}

	next := PeerState{Card: card, LastObservedAt: observedAt, Reachability: reachability}
	t.peers[card.NodeID] = next
	return ObserveResult{State: next, Superseded: true}, nil
}

func (t *Tracker) SetReachability(nodeID string, reachability Reachability, observedAt time.Time) (PeerState, error) {
	if t == nil {
		return PeerState{}, fmt.Errorf("tracker is nil")
	}
	if reachability != Reachable && reachability != Unreachable && reachability != Unknown {
		return PeerState{}, fmt.Errorf("invalid reachability")
	}
	current, ok := t.peers[nodeID]
	if !ok {
		return PeerState{}, fmt.Errorf("unknown node")
	}
	if observedAt.IsZero() {
		return PeerState{}, fmt.Errorf("observed_at is required")
	}
	current.Reachability = reachability
	current.LastObservedAt = observedAt
	t.peers[nodeID] = current
	return current, nil
}
