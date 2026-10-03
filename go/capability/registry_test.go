package capability

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func sensorDescriptor(id string, disclosure Disclosure) Descriptor {
	return Descriptor{
		Schema: DescriptorSchema,
		ID: id,
		Version: "v1",
		Kind: KindSensor,
		Effect: EffectReadOnly,
		Disclosure: disclosure,
		AuthorityRequired: false,
	}
}

func actuatorDescriptor(id string, disclosure Disclosure) Descriptor {
	return Descriptor{
		Schema: DescriptorSchema,
		ID: id,
		Version: "v1",
		Kind: KindActuator,
		Effect: EffectBoundedMutation,
		Disclosure: disclosure,
		AuthorityRequired: true,
	}
}

func readyProbe(id string) Probe {
	return func(context.Context) (State, error) {
		return State{
			CapabilityID: id,
			Readiness: ReadinessReady,
			ObservedAt: time.Date(2026, 10, 3, 22, 0, 0, 0, time.UTC),
		}, nil
	}
}

func eligibleHandler(_ context.Context, _ Request) (Outcome, error) {
	return Outcome{
		Result: "ELIGIBLE",
		Status: "complete",
		MutationPerformed: false,
		RetryAuthorized: false,
		Data: map[string]any{"ok": true},
	}, nil
}

func TestDescriptorEnforcesSensorAndActuatorBoundaries(t *testing.T) {
	sensor := sensorDescriptor("system.identity", DisclosurePublic)
	if err := sensor.Validate(); err != nil {
		t.Fatal(err)
	}

	badSensor := sensor
	badSensor.Effect = EffectBoundedMutation
	if err := badSensor.Validate(); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("mutating sensor descriptor was accepted: %v", err)
	}

	actuator := actuatorDescriptor("marker.record", DisclosureAuthenticated)
	if err := actuator.Validate(); err != nil {
		t.Fatal(err)
	}
	badActuator := actuator
	badActuator.AuthorityRequired = false
	if err := badActuator.Validate(); err == nil || !strings.Contains(err.Error(), "require authority") {
		t.Fatalf("authority-free actuator was accepted: %v", err)
	}
}

func TestSelectiveDisclosure(t *testing.T) {
	registry := NewRegistry()
	for _, d := range []Descriptor{
		sensorDescriptor("system.identity", DisclosurePublic),
		sensorDescriptor("truenas.observe", DisclosureAuthenticated),
	} {
		if err := registry.Register(Registration{
			Descriptor: d,
			Probe: readyProbe(d.ID),
			Handler: eligibleHandler,
		}); err != nil {
			t.Fatal(err)
		}
	}

	public := registry.Advertise(false)
	if len(public) != 1 || public[0].ID != "system.identity" {
		t.Fatalf("unexpected public advertisement: %#v", public)
	}
	authenticated := registry.Advertise(true)
	if len(authenticated) != 2 ||
		authenticated[0].ID != "system.identity" ||
		authenticated[1].ID != "truenas.observe" {
		t.Fatalf("unexpected authenticated advertisement: %#v", authenticated)
	}
}

func TestProbeFailureBecomesSanitizedUnknownReadiness(t *testing.T) {
	registry := NewRegistry()
	d := sensorDescriptor("truenas.observe", DisclosureAuthenticated)
	if err := registry.Register(Registration{
		Descriptor: d,
		Probe: func(context.Context) (State, error) {
			return State{}, errors.New("secret host detail")
		},
		Handler: eligibleHandler,
	}); err != nil {
		t.Fatal(err)
	}
	state, err := registry.Observe(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Readiness != ReadinessUnknown || state.Reason != "probe-failed" {
		t.Fatalf("unexpected degraded observation: %#v", state)
	}
	if strings.Contains(state.Reason, "secret") {
		t.Fatal("raw probe error escaped")
	}
}

func TestActuatorRequiresExactAuthorityVerifierBeforeHandler(t *testing.T) {
	registry := NewRegistry()
	d := actuatorDescriptor("marker.record", DisclosureAuthenticated)
	calls := 0
	if err := registry.Register(Registration{
		Descriptor: d,
		Probe: readyProbe(d.ID),
		Handler: func(context.Context, Request) (Outcome, error) {
			calls++
			return Outcome{
				Result: "ELIGIBLE",
				Status: "marker-recorded",
				MutationPerformed: true,
				RetryAuthorized: false,
				Data: map[string]any{"marker": "synthetic"},
			}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	req := Request{
		CapabilityID: d.ID,
		AuthorityRef: "github-issue-comment:ExampleOrg/project#7:11",
		AuthorityRevision: "sha256:" + strings.Repeat("a", 64),
		AuthorityState: "open",
		Input: json.RawMessage(`{}`),
	}

	outcome, err := registry.Invoke(
		context.Background(),
		req,
		func(context.Context, Request) error {
			return errors.New("stale authority secret detail")
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("actuator handler ran despite authority failure")
	}
	if outcome.Result != "UNKNOWN" || outcome.Status != "authority-verification-failed" ||
		outcome.MutationPerformed || outcome.RetryAuthorized {
		t.Fatalf("unexpected authority failure outcome: %#v", outcome)
	}

	outcome, err = registry.Invoke(
		context.Background(),
		req,
		func(context.Context, Request) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || outcome.Result != "ELIGIBLE" || !outcome.MutationPerformed {
		t.Fatalf("authorized actuator did not execute exactly once: calls=%d outcome=%#v", calls, outcome)
	}
}

func TestActuatorAmbiguousHandlerFailureConservativelyMarksMutationUnknown(t *testing.T) {
	registry := NewRegistry()
	d := actuatorDescriptor("marker.record", DisclosureAuthenticated)
	if err := registry.Register(Registration{
		Descriptor: d,
		Probe: readyProbe(d.ID),
		Handler: func(context.Context, Request) (Outcome, error) {
			return Outcome{}, errors.New("downstream ambiguous secret detail")
		},
	}); err != nil {
		t.Fatal(err)
	}
	req := Request{
		CapabilityID: d.ID,
		AuthorityRef: "github-issue-comment:ExampleOrg/project#7:11",
		AuthorityRevision: "sha256:" + strings.Repeat("a", 64),
		AuthorityState: "open",
		Input: json.RawMessage(`{}`),
	}
	outcome, err := registry.Invoke(
		context.Background(),
		req,
		func(context.Context, Request) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Result != "UNKNOWN" || outcome.Status != "capability-outcome-unknown" ||
		!outcome.MutationPerformed || outcome.RetryAuthorized {
		t.Fatalf("ambiguous actuator outcome was not conservative: %#v", outcome)
	}
	raw, _ := json.Marshal(outcome)
	if strings.Contains(string(raw), "secret detail") {
		t.Fatal("raw actuator error escaped")
	}
}

func TestSensorCanNeverReportMutation(t *testing.T) {
	registry := NewRegistry()
	d := sensorDescriptor("system.identity", DisclosurePublic)
	if err := registry.Register(Registration{
		Descriptor: d,
		Probe: readyProbe(d.ID),
		Handler: func(context.Context, Request) (Outcome, error) {
			return Outcome{
				Result: "ELIGIBLE",
				Status: "identity-read",
				MutationPerformed: true,
				RetryAuthorized: false,
				Data: map[string]any{},
			}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	_, err := registry.Invoke(
		context.Background(),
		Request{CapabilityID: d.ID, Input: json.RawMessage(`{}`)},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "sensor capability reported mutation") {
		t.Fatalf("mutating sensor result was accepted: %v", err)
	}
}

func TestUnknownCapabilityFailsClosed(t *testing.T) {
	registry := NewRegistry()
	if _, err := registry.Observe(context.Background(), "unknown.capability"); err == nil {
		t.Fatal("unknown capability observation was accepted")
	}
	if _, err := registry.Invoke(
		context.Background(),
		Request{CapabilityID: "unknown.capability", Input: json.RawMessage(`{}`)},
		nil,
	); err == nil {
		t.Fatal("unknown capability invocation was accepted")
	}
}

func TestStrictTypedInputRejectsUnknownAndTrailingFields(t *testing.T) {
	type identityInput struct {
		Verbose bool `json:"verbose"`
	}

	value, err := DecodeStrictInput[identityInput](json.RawMessage(`{"verbose":true}`))
	if err != nil || !value.Verbose {
		t.Fatalf("valid typed input rejected: %#v %v", value, err)
	}
	if _, err := DecodeStrictInput[identityInput](json.RawMessage(`{"verbose":true,"command":"whoami"}`)); err == nil {
		t.Fatal("unknown command field was accepted")
	}
	if _, err := DecodeStrictInput[identityInput](json.RawMessage(`{"verbose":true} {"extra":1}`)); err == nil {
		t.Fatal("trailing JSON was accepted")
	}
}

func TestDuplicateCapabilityRegistrationRejected(t *testing.T) {
	registry := NewRegistry()
	d := sensorDescriptor("system.identity", DisclosurePublic)
	registration := Registration{Descriptor: d, Probe: readyProbe(d.ID), Handler: eligibleHandler}
	if err := registry.Register(registration); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(registration); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate registration accepted: %v", err)
	}
}
