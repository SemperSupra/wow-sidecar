package rendezvous

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	NoteLimit = 1000
	RefLimit  = 1000
)

var launchIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{5,127}$`)

type Principal struct {
	ClientID string `json:"client_id"`
	Subject  string `json:"subject"`
}

type AuthorizeFunc func(context.Context, string, string) (Principal, error)
type IDFactory func(prefix string) string
type Clock func() time.Time

type Claim struct {
	ActorInstanceID string    `json:"actor_instance_id"`
	LaunchID        string    `json:"launch_id"`
	ClientID        string    `json:"-"`
	Subject         string    `json:"-"`
	ClaimedAt       time.Time `json:"claimed_at"`
	LeaseExpiresAt  time.Time `json:"lease_expires_at"`
}

type Completion struct {
	ActorInstanceID string    `json:"actor_instance_id"`
	CompletedAt     time.Time `json:"completed_at"`
	ResultRef       string    `json:"result_ref,omitempty"`
	ResultNote      string    `json:"result_note,omitempty"`
}

type ExpiredClaim struct {
	ActorInstanceID string    `json:"actor_instance_id"`
	LaunchID        string    `json:"launch_id"`
	ExpiredAt       time.Time `json:"expired_at"`
}

type Handoff struct {
	ID               string        `json:"handoff_id"`
	WorksetRef       string        `json:"workset_ref"`
	DelegationID     string        `json:"delegation_id"`
	Note             string        `json:"note,omitempty"`
	Status           string        `json:"status"`
	OfferedAt        time.Time     `json:"offered_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
	OfferedBy        Principal     `json:"offered_by"`
	Claim            *Claim        `json:"claim,omitempty"`
	Completion       *Completion   `json:"completion,omitempty"`
	LastExpiredClaim *ExpiredClaim `json:"last_expired_claim,omitempty"`
}

type Result struct {
	ReceiptStatus string  `json:"receipt_status"`
	Handoff       Handoff `json:"handoff"`
}

type Store struct {
	mu           sync.Mutex
	authorize    AuthorizeFunc
	lease        time.Duration
	clock        Clock
	idFactory    IDFactory
	handoffs     map[string]*Handoff
}

func New(authorize AuthorizeFunc, lease time.Duration) (*Store, error) {
	if authorize == nil {
		return nil, fmt.Errorf("authorize callback is required")
	}
	if lease <= 0 {
		return nil, fmt.Errorf("handoff lease must be positive")
	}
	return &Store{
		authorize: authorize,
		lease: lease,
		clock: time.Now,
		idFactory: randomID,
		handoffs: map[string]*Handoff{},
	}, nil
}

func (s *Store) SetClock(clock Clock) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if clock != nil {
		s.clock = clock
	}
}

func (s *Store) SetIDFactory(factory IDFactory) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if factory != nil {
		s.idFactory = factory
	}
}

func randomID(prefix string) string {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(raw)
}

func normalizePrincipal(p Principal) (Principal, error) {
	if strings.TrimSpace(p.ClientID) == "" {
		return Principal{}, fmt.Errorf("authenticated caller principal requires client_id")
	}
	subject := p.Subject
	if strings.TrimSpace(subject) == "" {
		subject = p.ClientID
	}
	return Principal{ClientID: p.ClientID, Subject: subject}, nil
}

func samePrincipal(a, b Principal) bool {
	return a.ClientID == b.ClientID && a.Subject == b.Subject
}

func smallText(value string, limit int, name string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > limit {
		return "", fmt.Errorf("%s must be at most %d characters", name, limit)
	}
	return value, nil
}

func cloneHandoff(h *Handoff) Handoff {
	out := *h
	if h.Claim != nil {
		v := *h.Claim
		out.Claim = &v
	}
	if h.Completion != nil {
		v := *h.Completion
		out.Completion = &v
	}
	if h.LastExpiredClaim != nil {
		v := *h.LastExpiredClaim
		out.LastExpiredClaim = &v
	}
	return out
}

func terminal(status string) bool {
	return status == "completed" || status == "blocked" || status == "failed"
}

func (s *Store) expireLocked(h *Handoff, now time.Time) {
	if h.Status != "claimed" || h.Claim == nil {
		return
	}
	if now.Before(h.Claim.LeaseExpiresAt) {
		return
	}
	h.LastExpiredClaim = &ExpiredClaim{
		ActorInstanceID: h.Claim.ActorInstanceID,
		LaunchID: h.Claim.LaunchID,
		ExpiredAt: now,
	}
	h.Claim = nil
	h.Status = "offered"
	h.UpdatedAt = now
}

func (s *Store) authorizedRecord(ctx context.Context, handoffID string) (*Handoff, Principal, error) {
	if ctx == nil {
		return nil, Principal{}, fmt.Errorf("request context is required")
	}
	s.mu.Lock()
	h := s.handoffs[handoffID]
	if h == nil {
		s.mu.Unlock()
		return nil, Principal{}, fmt.Errorf("handoff not found")
	}
	worksetRef := h.WorksetRef
	delegationID := h.DelegationID
	owner := h.OfferedBy
	s.mu.Unlock()

	principal, err := s.authorize(ctx, worksetRef, delegationID)
	if err != nil {
		return nil, Principal{}, err
	}
	principal, err = normalizePrincipal(principal)
	if err != nil {
		return nil, Principal{}, err
	}
	if !samePrincipal(principal, owner) {
		return nil, Principal{}, fmt.Errorf("authenticated caller principal does not own this handoff channel")
	}
	return h, principal, nil
}

func (s *Store) Offer(ctx context.Context, worksetRef, delegationID, note string) (Result, error) {
	if ctx == nil {
		return Result{}, fmt.Errorf("request context is required")
	}
	note, err := smallText(note, NoteLimit, "note")
	if err != nil {
		return Result{}, err
	}
	principal, err := s.authorize(ctx, worksetRef, delegationID)
	if err != nil {
		return Result{}, err
	}
	principal, err = normalizePrincipal(principal)
	if err != nil {
		return Result{}, err
	}
	now := s.clock()

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, h := range s.handoffs {
		s.expireLocked(h, now)
		if terminal(h.Status) {
			continue
		}
		if samePrincipal(h.OfferedBy, principal) &&
			h.WorksetRef == worksetRef &&
			h.DelegationID == delegationID &&
			h.Note == note {
			return Result{ReceiptStatus: "existing", Handoff: cloneHandoff(h)}, nil
		}
		return Result{}, fmt.Errorf("rendezvous permits only one active handoff at a time")
	}

	h := &Handoff{
		ID: s.idFactory("handoff"),
		WorksetRef: worksetRef,
		DelegationID: delegationID,
		Note: note,
		Status: "offered",
		OfferedAt: now,
		UpdatedAt: now,
		OfferedBy: principal,
	}
	s.handoffs[h.ID] = h
	return Result{ReceiptStatus: "offered", Handoff: cloneHandoff(h)}, nil
}

func (s *Store) Claim(ctx context.Context, handoffID, launchID string) (Result, error) {
	if ctx == nil {
		return Result{}, fmt.Errorf("request context is required")
	}
	if !launchIDRE.MatchString(launchID) {
		return Result{}, fmt.Errorf("launch_id must be an opaque 6-128 character identifier")
	}
	_, principal, err := s.authorizedRecord(ctx, handoffID)
	if err != nil {
		return Result{}, err
	}
	now := s.clock()

	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.handoffs[handoffID]
	if h == nil {
		return Result{}, fmt.Errorf("handoff not found")
	}
	s.expireLocked(h, now)
	if terminal(h.Status) {
		return Result{}, fmt.Errorf("handoff is already terminal")
	}
	if h.Claim != nil {
		if h.Claim.LaunchID == launchID &&
			h.Claim.ClientID == principal.ClientID &&
			h.Claim.Subject == principal.Subject {
			return Result{ReceiptStatus: "existing-claim", Handoff: cloneHandoff(h)}, nil
		}
		return Result{}, fmt.Errorf("handoff already has an active leased claim")
	}
	h.Claim = &Claim{
		ActorInstanceID: s.idFactory("actor"),
		LaunchID: launchID,
		ClientID: principal.ClientID,
		Subject: principal.Subject,
		ClaimedAt: now,
		LeaseExpiresAt: now.Add(s.lease),
	}
	h.Status = "claimed"
	h.UpdatedAt = now
	return Result{ReceiptStatus: "claimed", Handoff: cloneHandoff(h)}, nil
}

func (s *Store) Get(ctx context.Context, handoffID string) (Handoff, error) {
	if ctx == nil {
		return Handoff{}, fmt.Errorf("request context is required")
	}
	if _, _, err := s.authorizedRecord(ctx, handoffID); err != nil {
		return Handoff{}, err
	}
	now := s.clock()
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.handoffs[handoffID]
	if h == nil {
		return Handoff{}, fmt.Errorf("handoff not found")
	}
	s.expireLocked(h, now)
	return cloneHandoff(h), nil
}

func (s *Store) Complete(ctx context.Context, handoffID, actorInstanceID, status, resultRef, resultNote string) (Result, error) {
	if ctx == nil {
		return Result{}, fmt.Errorf("request context is required")
	}
	if !terminal(status) {
		return Result{}, fmt.Errorf("handoff terminal status must be completed, blocked, or failed")
	}
	var err error
	resultRef, err = smallText(resultRef, RefLimit, "result_ref")
	if err != nil {
		return Result{}, err
	}
	resultNote, err = smallText(resultNote, NoteLimit, "result_note")
	if err != nil {
		return Result{}, err
	}
	if _, _, err := s.authorizedRecord(ctx, handoffID); err != nil {
		return Result{}, err
	}
	now := s.clock()

	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.handoffs[handoffID]
	if h == nil {
		return Result{}, fmt.Errorf("handoff not found")
	}
	s.expireLocked(h, now)
	if terminal(h.Status) {
		if h.Completion != nil &&
			h.Completion.ActorInstanceID == actorInstanceID &&
			h.Status == status &&
			h.Completion.ResultRef == resultRef &&
			h.Completion.ResultNote == resultNote {
			return Result{ReceiptStatus: "already-completed", Handoff: cloneHandoff(h)}, nil
		}
		return Result{}, fmt.Errorf("handoff is already terminal")
	}
	if h.Claim == nil {
		return Result{}, fmt.Errorf("handoff has no active claim; expired actors cannot complete it")
	}
	if h.Claim.ActorInstanceID != actorInstanceID {
		return Result{}, fmt.Errorf("actor_instance_id does not own the active handoff claim")
	}
	h.Status = status
	h.Completion = &Completion{
		ActorInstanceID: actorInstanceID,
		CompletedAt: now,
		ResultRef: resultRef,
		ResultNote: resultNote,
	}
	h.UpdatedAt = now
	return Result{ReceiptStatus: status, Handoff: cloneHandoff(h)}, nil
}
