package federation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SemperSupra/wow-sidecar/go/embodiment"
)

const (
	SnapshotSchema = "wow-sidecar.peer-snapshot.v1"
	maxPeers       = 8
	maxCardBytes   = 64 * 1024
)

const (
	StatusNeverObserved   = "NEVER_OBSERVED"
	StatusObserved        = "OBSERVED"
	StatusFetchFailed     = "FETCH_FAILED"
	StatusCardRejected    = "CARD_REJECTED"
	StatusIdentityMismatch = "IDENTITY_MISMATCH"
	StatusNodeAlreadyBound = "NODE_ALREADY_BOUND"
)

type peerRecord struct {
	PeerRef        string
	NodeID         string
	Status         string
	LastObservedAt time.Time
}

type PeerView struct {
	PeerRef        string                   `json:"peer_ref"`
	NodeID         string                   `json:"node_id,omitempty"`
	Generation     uint64                   `json:"generation,omitempty"`
	IncarnationID  string                   `json:"incarnation_id,omitempty"`
	Locality       string                   `json:"locality,omitempty"`
	Presence       embodiment.Presence      `json:"presence"`
	Reachability   embodiment.Reachability  `json:"reachability"`
	LeaseExpiresAt string                   `json:"lease_expires_at,omitempty"`
	ObservedAt     string                   `json:"observed_at,omitempty"`
	Status         string                   `json:"status"`
}

type Snapshot struct {
	Schema          string     `json:"schema"`
	ConfiguredPeers int        `json:"configured_peers"`
	ObservedPeers   int        `json:"observed_peers"`
	Peers           []PeerView `json:"peers"`
}

type Observer struct {
	mu          sync.Mutex
	endpoints   []string
	records     map[string]*peerRecord
	nodeSources map[string]string
	tracker     *embodiment.Tracker
	client      *http.Client
	now         func() time.Time
}

func ParsePeerURLs(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	if raw != strings.TrimSpace(raw) {
		return nil, fmt.Errorf("peer URL set must already be normalized")
	}
	items := strings.Split(raw, ",")
	if len(items) > maxPeers {
		return nil, fmt.Errorf("at most %d peer URLs are allowed", maxPeers)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		normalized, err := normalizePeerURL(item)
		if err != nil {
			return nil, err
		}
		if seen[normalized] {
			return nil, fmt.Errorf("duplicate peer URL")
		}
		seen[normalized] = true
		out = append(out, normalized)
	}
	sort.Strings(out)
	return out, nil
}

func normalizePeerURL(raw string) (string, error) {
	if raw != strings.TrimSpace(raw) || raw == "" {
		return "", fmt.Errorf("peer URL must be normalized and non-empty")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid peer URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("peer URL scheme must be http or https")
	}
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("peer URL must not contain credentials, query, or fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("peer URL path must be empty")
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed.String(), nil
}

func peerRef(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func NewObserver(endpoints []string, client *http.Client, now func() time.Time) (*Observer, error) {
	if now == nil {
		return nil, fmt.Errorf("clock is required")
	}
	if len(endpoints) > maxPeers {
		return nil, fmt.Errorf("at most %d peer URLs are allowed", maxPeers)
	}
	seen := map[string]bool{}
	normalized := make([]string, 0, len(endpoints))
	records := map[string]*peerRecord{}
	for _, endpoint := range endpoints {
		value, err := normalizePeerURL(endpoint)
		if err != nil {
			return nil, err
		}
		if seen[value] {
			return nil, fmt.Errorf("duplicate peer URL")
		}
		seen[value] = true
		normalized = append(normalized, value)
		records[value] = &peerRecord{PeerRef: peerRef(value), Status: StatusNeverObserved}
	}
	sort.Strings(normalized)
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &Observer{
		endpoints:   normalized,
		records:     records,
		nodeSources: map[string]string{},
		tracker:     embodiment.NewTracker(),
		client:      client,
		now:         now,
	}, nil
}

func (o *Observer) Configured() int {
	if o == nil {
		return 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.endpoints)
}

func (o *Observer) CurrentCard(nodeID string) (embodiment.Card, bool) {
	if o == nil || nodeID == "" {
		return embodiment.Card{}, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	source, ok := o.nodeSources[nodeID]
	if !ok {
		return embodiment.Card{}, false
	}
	record, ok := o.records[source]
	if !ok || record.NodeID != nodeID || record.Status != StatusObserved {
		return embodiment.Card{}, false
	}
	state, ok := o.tracker.Get(nodeID)
	if !ok {
		return embodiment.Card{}, false
	}
	return state.Card, true
}

func (o *Observer) WithCurrentCard(nodeID string, fn func(embodiment.Card) error) error {
	if o == nil || nodeID == "" {
		return fmt.Errorf("current peer is unavailable")
	}
	if fn == nil {
		return fmt.Errorf("current peer callback is required")
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	source, ok := o.nodeSources[nodeID]
	if !ok {
		return fmt.Errorf("current peer is unavailable")
	}
	record, ok := o.records[source]
	if !ok || record.NodeID != nodeID || record.Status != StatusObserved {
		return fmt.Errorf("current peer is unavailable")
	}
	state, ok := o.tracker.Get(nodeID)
	if !ok {
		return fmt.Errorf("current peer is unavailable")
	}
	return fn(state.Card)
}

func (o *Observer) PollOnce(ctx context.Context) {
	if o == nil {
		return
	}
	for _, endpoint := range o.endpoints {
		o.pollEndpoint(ctx, endpoint)
	}
}

func (o *Observer) pollEndpoint(ctx context.Context, endpoint string) {
	card, err := o.fetchCard(ctx, endpoint)
	now := o.now().UTC()

	o.mu.Lock()
	defer o.mu.Unlock()

	record := o.records[endpoint]
	if err != nil {
		record.Status = StatusFetchFailed
		if record.NodeID != "" {
			_, _ = o.tracker.SetReachability(record.NodeID, embodiment.Unreachable, now)
		}
		return
	}

	if record.NodeID != "" && record.NodeID != card.NodeID {
		record.Status = StatusIdentityMismatch
		_, _ = o.tracker.SetReachability(record.NodeID, embodiment.Unknown, now)
		return
	}
	if record.NodeID == "" {
		if source, exists := o.nodeSources[card.NodeID]; exists && source != endpoint {
			record.Status = StatusNodeAlreadyBound
			return
		}
		record.NodeID = card.NodeID
		o.nodeSources[card.NodeID] = endpoint
	}

	if _, err := o.tracker.Observe(card, now, embodiment.Reachable); err != nil {
		record.Status = StatusCardRejected
		_, _ = o.tracker.SetReachability(record.NodeID, embodiment.Unknown, now)
		return
	}
	record.Status = StatusObserved
	record.LastObservedAt = now
}

func (o *Observer) fetchCard(ctx context.Context, endpoint string) (embodiment.Card, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/v1/card", nil)
	if err != nil {
		return embodiment.Card{}, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := o.client.Do(request)
	if err != nil {
		return embodiment.Card{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return embodiment.Card{}, fmt.Errorf("peer card returned non-200")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxCardBytes+1))
	if err != nil {
		return embodiment.Card{}, err
	}
	if len(data) > maxCardBytes {
		return embodiment.Card{}, fmt.Errorf("peer card exceeds size limit")
	}
	return embodiment.DecodeCard(data)
}

func (o *Observer) Run(ctx context.Context, interval time.Duration) error {
	if o == nil {
		return fmt.Errorf("peer observer is nil")
	}
	if interval < 5*time.Second || interval > time.Hour {
		return fmt.Errorf("peer poll interval must be between 5 and 3600 seconds")
	}
	o.PollOnce(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			o.PollOnce(ctx)
		}
	}
}

func (o *Observer) Snapshot() Snapshot {
	if o == nil {
		return Snapshot{Schema: SnapshotSchema, Peers: []PeerView{}}
	}
	now := o.now().UTC()
	o.mu.Lock()
	defer o.mu.Unlock()

	views := make([]PeerView, 0, len(o.endpoints))
	observed := 0
	for _, endpoint := range o.endpoints {
		record := o.records[endpoint]
		view := PeerView{
			PeerRef:      record.PeerRef,
			Presence:     embodiment.PresenceUnknown,
			Reachability: embodiment.Unknown,
			Status:       record.Status,
		}
		if record.NodeID != "" {
			observed++
			view.NodeID = record.NodeID
			if state, ok := o.tracker.Get(record.NodeID); ok {
				view.Generation = state.Card.Generation
				view.IncarnationID = state.Card.IncarnationID
				view.Locality = state.Card.Locality
				view.Presence = state.PresenceAt(now)
				view.Reachability = state.Reachability
				view.LeaseExpiresAt = state.Card.LeaseExpiresAt
				view.ObservedAt = state.LastObservedAt.UTC().Format(time.RFC3339Nano)
			}
		}
		if record.Status == StatusCardRejected ||
			record.Status == StatusIdentityMismatch ||
			record.Status == StatusNodeAlreadyBound {
			view.Presence = embodiment.PresenceUnknown
			view.Reachability = embodiment.Unknown
		}
		views = append(views, view)
	}
	return Snapshot{
		Schema:          SnapshotSchema,
		ConfiguredPeers: len(o.endpoints),
		ObservedPeers:   observed,
		Peers:           views,
	}
}
