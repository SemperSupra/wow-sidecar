package wake

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SemperSupra/wow-sidecar/go/protocol"
)

const (
	EventSchema   = "wow-sidecar.wake-event.v1"
	ReceiptSchema = "wow-sidecar.wake-receipt.v1"
)

var (
	wakeIDRE       = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{2,127}$`)
	occurrenceRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{2,127}$`)
	capabilityRE   = regexp.MustCompile(`^[a-z][a-z0-9_.-]{2,127}$`)
	revisionRE     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	idempotencyRE  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type Event struct {
	Schema            string `json:"schema"`
	WakeID            string `json:"wake_id"`
	AuthorityRef       string `json:"authority_ref"`
	AuthorityRevision  string `json:"authority_revision"`
	OccurrenceID       string `json:"occurrence_id"`
	DueAt              string `json:"due_at"`
	ActionCapability   string `json:"action_capability"`
	IdempotencyKey     string `json:"idempotency_key"`
}

type AuthorityResult struct {
	Active          bool
	ObservedRef     string
	ObservedRevision string
	State           string
}

type VerifyAuthority func(Event) (AuthorityResult, error)
type InvokeAction func(Event) (ActionResult, error)

type ActionResult struct {
	Result string
	Status string
	Detail map[string]any
}

type Receipt struct {
	Schema            string         `json:"schema"`
	WakeID            string         `json:"wake_id"`
	OccurrenceID      string         `json:"occurrence_id"`
	IdempotencyKey    string         `json:"idempotency_key"`
	AuthorityRef      string         `json:"authority_ref"`
	AuthorityRevision string         `json:"authority_revision"`
	ActionCapability  string         `json:"action_capability"`
	Result            string         `json:"result"`
	Status            string         `json:"status"`
	ActionInvoked     bool           `json:"action_invoked"`
	RetryAuthorized   bool           `json:"retry_authorized"`
	Details           map[string]any `json:"details"`
}

type Claim struct {
	IdempotencyKey string
	EventDigest    string
	ClaimedAt      time.Time
}

type Store struct {
	mu       sync.Mutex
	claims   map[string]Claim
	receipts map[string]Receipt
}

type ProcessResult struct {
	State   string
	Receipt *Receipt
	Execute bool
}

func NewStore() *Store {
	return &Store{
		claims:   map[string]Claim{},
		receipts: map[string]Receipt{},
	}
}

func DecodeEvent(data []byte) (Event, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return Event{}, fmt.Errorf("wake event must be an object: %w", err)
	}
	expected := []string{
		"schema", "wake_id", "authority_ref", "authority_revision",
		"occurrence_id", "due_at", "action_capability", "idempotency_key",
	}
	if len(raw) != len(expected) {
		return Event{}, fmt.Errorf("wake event fields do not match v1 schema")
	}
	for _, key := range expected {
		if _, ok := raw[key]; !ok {
			return Event{}, fmt.Errorf("wake event fields do not match v1 schema")
		}
	}
	var event Event
	if err := json.Unmarshal(data, &event); err != nil {
		return Event{}, fmt.Errorf("decode wake event: %w", err)
	}
	if err := event.Validate(); err != nil {
		return Event{}, err
	}
	return event, nil
}

func (e Event) Validate() error {
	if e.Schema != EventSchema {
		return fmt.Errorf("unsupported wake event schema")
	}
	if !wakeIDRE.MatchString(e.WakeID) {
		return fmt.Errorf("invalid wake_id")
	}
	if strings.TrimSpace(e.AuthorityRef) == "" || e.AuthorityRef != strings.TrimSpace(e.AuthorityRef) {
		return fmt.Errorf("authority_ref is required")
	}
	if !revisionRE.MatchString(e.AuthorityRevision) {
		return fmt.Errorf("authority_revision is invalid")
	}
	if !occurrenceRE.MatchString(e.OccurrenceID) {
		return fmt.Errorf("invalid occurrence_id")
	}
	due, err := time.Parse(time.RFC3339Nano, e.DueAt)
	if err != nil || due.Location() != time.UTC {
		return fmt.Errorf("due_at must be RFC3339 UTC")
	}
	if !capabilityRE.MatchString(e.ActionCapability) {
		return fmt.Errorf("invalid action_capability")
	}
	if !idempotencyRE.MatchString(e.IdempotencyKey) {
		return fmt.Errorf("invalid idempotency_key")
	}
	expected, err := e.ExpectedIdempotencyKey()
	if err != nil {
		return err
	}
	if e.IdempotencyKey != expected {
		return fmt.Errorf("idempotency_key does not bind exact wake occurrence")
	}
	return nil
}

func (e Event) ExpectedIdempotencyKey() (string, error) {
	type keyMaterial struct {
		WakeID            string `json:"wake_id"`
		AuthorityRef       string `json:"authority_ref"`
		AuthorityRevision  string `json:"authority_revision"`
		OccurrenceID       string `json:"occurrence_id"`
		DueAt              string `json:"due_at"`
		ActionCapability   string `json:"action_capability"`
	}
	material := keyMaterial{
		WakeID: e.WakeID,
		AuthorityRef: e.AuthorityRef,
		AuthorityRevision: e.AuthorityRevision,
		OccurrenceID: e.OccurrenceID,
		DueAt: e.DueAt,
		ActionCapability: e.ActionCapability,
	}
	raw, err := protocol.CanonicalJSON(material)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (e Event) Digest() (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}
	raw, err := protocol.CanonicalJSON(e)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func NewEvent(
	wakeID, authorityRef, authorityRevision, occurrenceID string,
	dueAt time.Time, actionCapability string,
) (Event, error) {
	if dueAt.Location() != time.UTC {
		return Event{}, fmt.Errorf("due_at must be UTC")
	}
	event := Event{
		Schema: EventSchema,
		WakeID: wakeID,
		AuthorityRef: authorityRef,
		AuthorityRevision: authorityRevision,
		OccurrenceID: occurrenceID,
		DueAt: dueAt.Format(time.RFC3339Nano),
		ActionCapability: actionCapability,
	}
	key, err := event.ExpectedIdempotencyKey()
	if err != nil {
		return Event{}, err
	}
	event.IdempotencyKey = key
	if err := event.Validate(); err != nil {
		return Event{}, err
	}
	return event, nil
}

func (s *Store) Begin(event Event, now time.Time) (ProcessResult, error) {
	if s == nil {
		return ProcessResult{}, fmt.Errorf("wake store is nil")
	}
	if err := event.Validate(); err != nil {
		return ProcessResult{}, err
	}
	if now.IsZero() {
		return ProcessResult{}, fmt.Errorf("claim time is required")
	}
	digest, err := event.Digest()
	if err != nil {
		return ProcessResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if receipt, ok := s.receipts[event.IdempotencyKey]; ok {
		copy := receipt
		return ProcessResult{State: "already-receipted", Receipt: &copy, Execute: false}, nil
	}
	if claim, ok := s.claims[event.IdempotencyKey]; ok {
		if claim.EventDigest != digest {
			return ProcessResult{}, fmt.Errorf("idempotency key collision")
		}
		return ProcessResult{State: "claimed-without-receipt", Execute: false}, nil
	}
	s.claims[event.IdempotencyKey] = Claim{
		IdempotencyKey: event.IdempotencyKey,
		EventDigest: digest,
		ClaimedAt: now,
	}
	return ProcessResult{State: "claimed", Execute: true}, nil
}

func (s *Store) Record(event Event, receipt Receipt) error {
	if s == nil {
		return fmt.Errorf("wake store is nil")
	}
	if err := event.Validate(); err != nil {
		return err
	}
	if err := validateReceipt(event, receipt); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	claim, ok := s.claims[event.IdempotencyKey]
	if !ok {
		return fmt.Errorf("wake receipt without claim")
	}
	digest, err := event.Digest()
	if err != nil {
		return err
	}
	if claim.EventDigest != digest {
		return fmt.Errorf("wake claim does not bind exact event")
	}
	if existing, ok := s.receipts[event.IdempotencyKey]; ok {
		existingJSON, _ := protocol.CanonicalJSON(existing)
		incomingJSON, _ := protocol.CanonicalJSON(receipt)
		if string(existingJSON) == string(incomingJSON) {
			return nil
		}
		return fmt.Errorf("wake occurrence already has a different receipt")
	}
	s.receipts[event.IdempotencyKey] = receipt
	return nil
}

func validateReceipt(event Event, receipt Receipt) error {
	if receipt.Schema != ReceiptSchema {
		return fmt.Errorf("unsupported wake receipt schema")
	}
	if receipt.WakeID != event.WakeID ||
		receipt.OccurrenceID != event.OccurrenceID ||
		receipt.IdempotencyKey != event.IdempotencyKey ||
		receipt.AuthorityRef != event.AuthorityRef ||
		receipt.AuthorityRevision != event.AuthorityRevision ||
		receipt.ActionCapability != event.ActionCapability {
		return fmt.Errorf("wake receipt does not bind exact occurrence")
	}
	if receipt.Result != "ELIGIBLE" &&
		receipt.Result != "NOOP" &&
		receipt.Result != "UNKNOWN" &&
		receipt.Result != "REJECTED" {
		return fmt.Errorf("invalid wake receipt result")
	}
	if strings.TrimSpace(receipt.Status) == "" {
		return fmt.Errorf("wake receipt status is required")
	}
	if receipt.RetryAuthorized {
		return fmt.Errorf("wake receipt must not authorize retry")
	}
	if receipt.Details == nil {
		return fmt.Errorf("wake receipt details must be an object")
	}
	return nil
}

func Process(
	store *Store,
	event Event,
	now time.Time,
	verify VerifyAuthority,
	invoke InvokeAction,
) (Receipt, error) {
	if verify == nil {
		return Receipt{}, fmt.Errorf("authority verifier is required")
	}
	if invoke == nil {
		return Receipt{}, fmt.Errorf("action invoker is required")
	}
	begin, err := store.Begin(event, now)
	if err != nil {
		return Receipt{}, err
	}
	if !begin.Execute {
		if begin.Receipt != nil {
			return *begin.Receipt, nil
		}
		return Receipt{
			Schema: ReceiptSchema,
			WakeID: event.WakeID,
			OccurrenceID: event.OccurrenceID,
			IdempotencyKey: event.IdempotencyKey,
			AuthorityRef: event.AuthorityRef,
			AuthorityRevision: event.AuthorityRevision,
			ActionCapability: event.ActionCapability,
			Result: "UNKNOWN",
			Status: "claimed-without-receipt",
			ActionInvoked: false,
			RetryAuthorized: false,
			Details: map[string]any{"preserve_evidence": true},
		}, nil
	}

	receipt := Receipt{
		Schema: ReceiptSchema,
		WakeID: event.WakeID,
		OccurrenceID: event.OccurrenceID,
		IdempotencyKey: event.IdempotencyKey,
		AuthorityRef: event.AuthorityRef,
		AuthorityRevision: event.AuthorityRevision,
		ActionCapability: event.ActionCapability,
		RetryAuthorized: false,
		Details: map[string]any{},
	}

	authority, err := verify(event)
	if err != nil {
		receipt.Result = "UNKNOWN"
		receipt.Status = "authority-reconciliation-failed"
		receipt.Details["preserve_evidence"] = true
		_ = store.Record(event, receipt)
		return receipt, nil
	}
	receipt.Details["observed_authority_ref"] = authority.ObservedRef
	receipt.Details["observed_authority_revision"] = authority.ObservedRevision
	receipt.Details["observed_authority_state"] = authority.State

	if authority.ObservedRef != event.AuthorityRef ||
		authority.ObservedRevision != event.AuthorityRevision ||
		!authority.Active {
		receipt.Result = "NOOP"
		receipt.Status = "authority-stale-or-inactive"
		if err := store.Record(event, receipt); err != nil {
			return Receipt{}, err
		}
		return receipt, nil
	}

	action, err := invoke(event)
	receipt.ActionInvoked = true
	if err != nil {
		receipt.Result = "UNKNOWN"
		receipt.Status = "action-outcome-unknown"
		receipt.Details["preserve_evidence"] = true
		if err := store.Record(event, receipt); err != nil {
			return Receipt{}, err
		}
		return receipt, nil
	}
	if action.Result != "ELIGIBLE" && action.Result != "REJECTED" && action.Result != "UNKNOWN" {
		receipt.Result = "UNKNOWN"
		receipt.Status = "action-result-invalid"
		receipt.Details["preserve_evidence"] = true
	} else {
		receipt.Result = action.Result
		receipt.Status = action.Status
		for k, v := range action.Detail {
			receipt.Details[k] = v
		}
	}
	if err := store.Record(event, receipt); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func (s *Store) Snapshot() []Receipt {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.receipts))
	for key := range s.receipts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]Receipt, 0, len(keys))
	for _, key := range keys {
		out = append(out, s.receipts[key])
	}
	return out
}
