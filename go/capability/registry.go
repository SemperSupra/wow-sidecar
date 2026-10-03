package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const DescriptorSchema = "wow-sidecar.capability.v1"

type Kind string
type Effect string
type Disclosure string
type Readiness string

const (
	KindSensor   Kind = "sensor"
	KindActuator Kind = "actuator"

	EffectReadOnly        Effect = "read-only"
	EffectBoundedMutation Effect = "bounded-mutation"

	DisclosurePublic        Disclosure = "public"
	DisclosureAuthenticated Disclosure = "authenticated"

	ReadinessReady       Readiness = "READY"
	ReadinessDegraded    Readiness = "DEGRADED"
	ReadinessUnavailable Readiness = "UNAVAILABLE"
	ReadinessUnknown     Readiness = "UNKNOWN"
)

var (
	capabilityIDRE = regexp.MustCompile(`^[a-z][a-z0-9_.-]{2,127}$`)
	versionRE      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	revisionRE     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type Descriptor struct {
	Schema            string     `json:"schema"`
	ID                string     `json:"id"`
	Version           string     `json:"version"`
	Kind              Kind       `json:"kind"`
	Effect            Effect     `json:"effect"`
	Disclosure        Disclosure `json:"disclosure"`
	AuthorityRequired bool       `json:"authority_required"`
}

type State struct {
	CapabilityID string    `json:"capability_id"`
	Readiness    Readiness `json:"readiness"`
	ObservedAt   time.Time `json:"observed_at"`
	Reason       string    `json:"reason,omitempty"`
}

type Request struct {
	CapabilityID     string          `json:"capability_id"`
	AuthorityRef     string          `json:"authority_ref,omitempty"`
	AuthorityRevision string         `json:"authority_revision,omitempty"`
	AuthorityState   string          `json:"authority_state,omitempty"`
	Input            json.RawMessage `json:"input"`
}

type Outcome struct {
	Result            string         `json:"result"`
	Status            string         `json:"status"`
	MutationPerformed bool           `json:"mutation_performed"`
	RetryAuthorized   bool           `json:"retry_authorized"`
	Data              map[string]any `json:"data"`
}

type Handler func(context.Context, Request) (Outcome, error)
type Probe func(context.Context) (State, error)
type AuthorityVerifier func(context.Context, Request) error

type Registration struct {
	Descriptor Descriptor
	Probe      Probe
	Handler    Handler
}

type Registry struct {
	mu           sync.RWMutex
	registrations map[string]Registration
}

func NewRegistry() *Registry {
	return &Registry{registrations: map[string]Registration{}}
}

func (d Descriptor) Validate() error {
	if d.Schema != DescriptorSchema {
		return fmt.Errorf("unsupported capability descriptor schema")
	}
	if !capabilityIDRE.MatchString(d.ID) {
		return fmt.Errorf("invalid capability id")
	}
	if !versionRE.MatchString(d.Version) {
		return fmt.Errorf("invalid capability version")
	}
	if d.Kind != KindSensor && d.Kind != KindActuator {
		return fmt.Errorf("invalid capability kind")
	}
	if d.Disclosure != DisclosurePublic && d.Disclosure != DisclosureAuthenticated {
		return fmt.Errorf("invalid capability disclosure")
	}
	switch d.Kind {
	case KindSensor:
		if d.Effect != EffectReadOnly {
			return fmt.Errorf("sensor capability must be read-only")
		}
	case KindActuator:
		if d.Effect != EffectBoundedMutation {
			return fmt.Errorf("actuator capability must declare bounded-mutation")
		}
		if !d.AuthorityRequired {
			return fmt.Errorf("actuator capability must require authority")
		}
	}
	return nil
}

func (s State) Validate(expectedID string) error {
	if s.CapabilityID != expectedID {
		return fmt.Errorf("capability state id mismatch")
	}
	switch s.Readiness {
	case ReadinessReady, ReadinessDegraded, ReadinessUnavailable, ReadinessUnknown:
	default:
		return fmt.Errorf("invalid capability readiness")
	}
	if s.ObservedAt.IsZero() {
		return fmt.Errorf("capability observed_at is required")
	}
	if s.Reason != strings.TrimSpace(s.Reason) {
		return fmt.Errorf("capability reason must be normalized")
	}
	return nil
}

func validateRequest(req Request, descriptor Descriptor) error {
	if req.CapabilityID != descriptor.ID {
		return fmt.Errorf("capability request id mismatch")
	}
	if descriptor.AuthorityRequired {
		if strings.TrimSpace(req.AuthorityRef) == "" || req.AuthorityRef != strings.TrimSpace(req.AuthorityRef) {
			return fmt.Errorf("authority_ref is required")
		}
		if !revisionRE.MatchString(req.AuthorityRevision) {
			return fmt.Errorf("authority_revision is invalid")
		}
		if req.AuthorityState != "open" {
			return fmt.Errorf("authority_state must be open")
		}
	}
	if req.Input == nil {
		req.Input = json.RawMessage(`{}`)
	}
	var input any
	if err := json.Unmarshal(req.Input, &input); err != nil {
		return fmt.Errorf("capability input must be valid JSON")
	}
	if _, ok := input.(map[string]any); !ok {
		return fmt.Errorf("capability input must be an object")
	}
	return nil
}

func validateOutcome(descriptor Descriptor, outcome Outcome) error {
	if outcome.Result != "ELIGIBLE" && outcome.Result != "REJECTED" && outcome.Result != "UNKNOWN" {
		return fmt.Errorf("invalid capability outcome result")
	}
	if strings.TrimSpace(outcome.Status) == "" || outcome.Status != strings.TrimSpace(outcome.Status) {
		return fmt.Errorf("capability outcome status is required")
	}
	if outcome.RetryAuthorized {
		return fmt.Errorf("capability outcome must not authorize retry")
	}
	if descriptor.Kind == KindSensor && outcome.MutationPerformed {
		return fmt.Errorf("sensor capability reported mutation")
	}
	if outcome.Data == nil {
		return fmt.Errorf("capability outcome data must be an object")
	}
	return nil
}

func (r *Registry) Register(registration Registration) error {
	if r == nil {
		return fmt.Errorf("capability registry is nil")
	}
	if err := registration.Descriptor.Validate(); err != nil {
		return err
	}
	if registration.Probe == nil {
		return fmt.Errorf("capability probe is required")
	}
	if registration.Handler == nil {
		return fmt.Errorf("capability handler is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.registrations[registration.Descriptor.ID]; exists {
		return fmt.Errorf("duplicate capability id")
	}
	r.registrations[registration.Descriptor.ID] = registration
	return nil
}

func (r *Registry) Advertise(authenticated bool) []Descriptor {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Descriptor, 0, len(r.registrations))
	for _, registration := range r.registrations {
		if registration.Descriptor.Disclosure == DisclosureAuthenticated && !authenticated {
			continue
		}
		out = append(out, registration.Descriptor)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (r *Registry) Observe(ctx context.Context, capabilityID string) (State, error) {
	if r == nil {
		return State{}, fmt.Errorf("capability registry is nil")
	}
	r.mu.RLock()
	registration, ok := r.registrations[capabilityID]
	r.mu.RUnlock()
	if !ok {
		return State{}, fmt.Errorf("capability is not admitted")
	}
	state, err := registration.Probe(ctx)
	if err != nil {
		return State{
			CapabilityID: capabilityID,
			Readiness: ReadinessUnknown,
			ObservedAt: time.Now().UTC(),
			Reason: "probe-failed",
		}, nil
	}
	if err := state.Validate(capabilityID); err != nil {
		return State{}, err
	}
	return state, nil
}

func (r *Registry) Invoke(
	ctx context.Context,
	req Request,
	verify AuthorityVerifier,
) (Outcome, error) {
	if r == nil {
		return Outcome{}, fmt.Errorf("capability registry is nil")
	}
	r.mu.RLock()
	registration, ok := r.registrations[req.CapabilityID]
	r.mu.RUnlock()
	if !ok {
		return Outcome{}, fmt.Errorf("capability is not admitted")
	}
	if err := validateRequest(req, registration.Descriptor); err != nil {
		return Outcome{}, err
	}
	if registration.Descriptor.AuthorityRequired {
		if verify == nil {
			return Outcome{}, fmt.Errorf("authority verifier is required")
		}
		if err := verify(ctx, req); err != nil {
			return Outcome{
				Result: "UNKNOWN",
				Status: "authority-verification-failed",
				MutationPerformed: false,
				RetryAuthorized: false,
				Data: map[string]any{"preserve_evidence": true},
			}, nil
		}
	}
	outcome, err := registration.Handler(ctx, req)
	if err != nil {
		return Outcome{
			Result: "UNKNOWN",
			Status: "capability-outcome-unknown",
			MutationPerformed: registration.Descriptor.Kind == KindActuator,
			RetryAuthorized: false,
			Data: map[string]any{"preserve_evidence": true},
		}, nil
	}
	if err := validateOutcome(registration.Descriptor, outcome); err != nil {
		return Outcome{}, err
	}
	return outcome, nil
}

func DecodeStrictInput[T any](raw json.RawMessage) (T, error) {
	var zero T
	if raw == nil {
		raw = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var value T
	if err := decoder.Decode(&value); err != nil {
		return zero, fmt.Errorf("decode capability input: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return zero, fmt.Errorf("capability input contains trailing JSON")
		}
		return zero, fmt.Errorf("capability input contains trailing data")
	}
	return value, nil
}
