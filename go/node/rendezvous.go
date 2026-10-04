package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/SemperSupra/wow-sidecar/go/capability"
	"github.com/SemperSupra/wow-sidecar/go/rendezvous"
)

const rendezvousCapabilityID = "rendezvous.handoff"

type RendezvousConfig struct {
	WorksetRef   string
	DelegationID string
	Lease        time.Duration
}

type rendezvousPrincipalContextKey struct{}

type RendezvousService struct {
	config RendezvousConfig
	store  *rendezvous.Store
}

func rendezvousConfigFromEnv() (*RendezvousConfig, error) {
	values := map[string]string{
		"WOW_RENDEZVOUS_WORKSET_REF":   os.Getenv("WOW_RENDEZVOUS_WORKSET_REF"),
		"WOW_RENDEZVOUS_DELEGATION_ID": os.Getenv("WOW_RENDEZVOUS_DELEGATION_ID"),
		"WOW_RENDEZVOUS_LEASE_SECONDS": os.Getenv("WOW_RENDEZVOUS_LEASE_SECONDS"),
	}
	enabled := false
	for _, value := range values {
		if value != "" {
			enabled = true
			break
		}
	}
	if !enabled {
		return nil, nil
	}
	for name, value := range values {
		if value == "" {
			return nil, fmt.Errorf("%s is required when rendezvous is enabled", name)
		}
		if strings.TrimSpace(value) != value {
			return nil, fmt.Errorf("%s must be normalized", name)
		}
	}
	if len(values["WOW_RENDEZVOUS_WORKSET_REF"]) > rendezvous.RefLimit {
		return nil, fmt.Errorf("WOW_RENDEZVOUS_WORKSET_REF exceeds size limit")
	}
	if len(values["WOW_RENDEZVOUS_DELEGATION_ID"]) > 256 {
		return nil, fmt.Errorf("WOW_RENDEZVOUS_DELEGATION_ID exceeds size limit")
	}
	seconds, err := strconv.Atoi(values["WOW_RENDEZVOUS_LEASE_SECONDS"])
	if err != nil || seconds < 5 || seconds > 3600 {
		return nil, fmt.Errorf("WOW_RENDEZVOUS_LEASE_SECONDS must be between 5 and 3600")
	}
	return &RendezvousConfig{
		WorksetRef:   values["WOW_RENDEZVOUS_WORKSET_REF"],
		DelegationID: values["WOW_RENDEZVOUS_DELEGATION_ID"],
		Lease:        time.Duration(seconds) * time.Second,
	}, nil
}

func newRendezvousService(config *RendezvousConfig, clock func() time.Time) (*RendezvousService, error) {
	if config == nil {
		return nil, fmt.Errorf("rendezvous config is required")
	}
	if strings.TrimSpace(config.WorksetRef) == "" || config.WorksetRef != strings.TrimSpace(config.WorksetRef) {
		return nil, fmt.Errorf("rendezvous workset_ref must be normalized and non-empty")
	}
	if strings.TrimSpace(config.DelegationID) == "" || config.DelegationID != strings.TrimSpace(config.DelegationID) {
		return nil, fmt.Errorf("rendezvous delegation_id must be normalized and non-empty")
	}
	if len(config.WorksetRef) > rendezvous.RefLimit || len(config.DelegationID) > 256 {
		return nil, fmt.Errorf("rendezvous binding exceeds size limit")
	}
	if config.Lease < 5*time.Second || config.Lease > time.Hour {
		return nil, fmt.Errorf("rendezvous lease must be between 5 seconds and 1 hour")
	}
	authorize := func(ctx context.Context, worksetRef, delegationID string) (rendezvous.Principal, error) {
		if worksetRef != config.WorksetRef || delegationID != config.DelegationID {
			return rendezvous.Principal{}, fmt.Errorf("rendezvous request is outside the locally admitted delegation")
		}
		principal, ok := ctx.Value(rendezvousPrincipalContextKey{}).(rendezvous.Principal)
		if !ok {
			return rendezvous.Principal{}, fmt.Errorf("rendezvous request principal is unavailable")
		}
		return principal, nil
	}
	store, err := rendezvous.New(authorize, config.Lease)
	if err != nil {
		return nil, err
	}
	if clock != nil {
		store.SetClock(clock)
	}
	return &RendezvousService{config: *config, store: store}, nil
}

type rendezvousOfferInput struct {
	Operation string `json:"operation"`
	Note      string `json:"note,omitempty"`
}

type rendezvousClaimInput struct {
	Operation string `json:"operation"`
	HandoffID string `json:"handoff_id"`
	LaunchID  string `json:"launch_id"`
}

type rendezvousCompleteInput struct {
	Operation       string `json:"operation"`
	HandoffID       string `json:"handoff_id"`
	ActorInstanceID string `json:"actor_instance_id"`
	Status          string `json:"status"`
	ResultRef       string `json:"result_ref,omitempty"`
	ResultNote      string `json:"result_note,omitempty"`
}

func rendezvousOperation(raw json.RawMessage) (string, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return "", fmt.Errorf("decode rendezvous command: %w", err)
	}
	value, ok := object["operation"]
	if !ok {
		return "", fmt.Errorf("rendezvous operation is required")
	}
	var operation string
	if err := json.Unmarshal(value, &operation); err != nil {
		return "", fmt.Errorf("rendezvous operation must be a string")
	}
	switch operation {
	case "offer", "claim", "complete":
		return operation, nil
	default:
		return "", fmt.Errorf("unsupported rendezvous operation")
	}
}

func rendezvousPrincipal(req capability.Request) rendezvous.Principal {
	return rendezvous.Principal{
		ClientID: "durable-control",
		Subject:  req.AuthorityRef + "@" + req.AuthorityRevision,
	}
}

func rendezvousCapabilityData(result rendezvous.Result) map[string]any {
	data := map[string]any{
		"receipt_status": result.ReceiptStatus,
		"handoff_id":     result.Handoff.ID,
		"handoff_status": result.Handoff.Status,
	}
	if result.Handoff.Claim != nil {
		data["actor_instance_id"] = result.Handoff.Claim.ActorInstanceID
		data["lease_expires_at"] = result.Handoff.Claim.LeaseExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	if result.Handoff.Completion != nil {
		data["completed_at"] = result.Handoff.Completion.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	return data
}

func rendezvousMutationPerformed(receiptStatus string) bool {
	switch receiptStatus {
	case "existing", "existing-claim", "already-completed":
		return false
	default:
		return true
	}
}

func (s *RendezvousService) invoke(ctx context.Context, req capability.Request) (capability.Outcome, error) {
	if s == nil || s.store == nil {
		return capability.Outcome{}, fmt.Errorf("rendezvous service is unavailable")
	}
	if ctx == nil {
		return capability.Outcome{}, fmt.Errorf("request context is required")
	}
	ctx = context.WithValue(ctx, rendezvousPrincipalContextKey{}, rendezvousPrincipal(req))
	operation, err := rendezvousOperation(req.Input)
	if err != nil {
		return capability.Outcome{}, err
	}

	var result rendezvous.Result
	switch operation {
	case "offer":
		input, err := capability.DecodeStrictInput[rendezvousOfferInput](req.Input)
		if err != nil {
			return capability.Outcome{}, err
		}
		if input.Operation != "offer" {
			return capability.Outcome{}, fmt.Errorf("rendezvous operation mismatch")
		}
		result, err = s.store.Offer(ctx, s.config.WorksetRef, s.config.DelegationID, input.Note)
		if err != nil {
			return capability.Outcome{}, err
		}
	case "claim":
		input, err := capability.DecodeStrictInput[rendezvousClaimInput](req.Input)
		if err != nil {
			return capability.Outcome{}, err
		}
		if input.Operation != "claim" {
			return capability.Outcome{}, fmt.Errorf("rendezvous operation mismatch")
		}
		result, err = s.store.Claim(ctx, input.HandoffID, input.LaunchID)
		if err != nil {
			return capability.Outcome{}, err
		}
	case "complete":
		input, err := capability.DecodeStrictInput[rendezvousCompleteInput](req.Input)
		if err != nil {
			return capability.Outcome{}, err
		}
		if input.Operation != "complete" {
			return capability.Outcome{}, fmt.Errorf("rendezvous operation mismatch")
		}
		result, err = s.store.Complete(
			ctx,
			input.HandoffID,
			input.ActorInstanceID,
			input.Status,
			input.ResultRef,
			input.ResultNote,
		)
		if err != nil {
			return capability.Outcome{}, err
		}
	}

	return capability.Outcome{
		Result:            "ELIGIBLE",
		Status:            "rendezvous-" + result.ReceiptStatus,
		MutationPerformed: rendezvousMutationPerformed(result.ReceiptStatus),
		RetryAuthorized:   false,
		Data:              rendezvousCapabilityData(result),
	}, nil
}

func registerRendezvousCapability(registry *capability.Registry, runtime *Runtime) (string, error) {
	if registry == nil || runtime == nil || runtime.rendezvous == nil {
		return "", fmt.Errorf("configured rendezvous runtime is required")
	}
	descriptor := capability.Descriptor{
		Schema:            capability.DescriptorSchema,
		ID:                rendezvousCapabilityID,
		Version:           "v1",
		Kind:              capability.KindActuator,
		Effect:            capability.EffectBoundedMutation,
		Disclosure:        capability.DisclosureAuthenticated,
		AuthorityRequired: true,
	}
	err := registry.Register(capability.Registration{
		Descriptor: descriptor,
		Probe: func(context.Context) (capability.State, error) {
			return capability.State{
				CapabilityID: descriptor.ID,
				Readiness:    capability.ReadinessReady,
				ObservedAt:   runtime.now().UTC(),
			}, nil
		},
		Handler: runtime.rendezvous.invoke,
	})
	if err != nil {
		return "", err
	}
	return descriptor.ID, nil
}
