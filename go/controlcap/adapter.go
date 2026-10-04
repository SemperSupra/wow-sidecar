package controlcap

import (
	"context"
	"fmt"
	"strings"

	"github.com/SemperSupra/wow-sidecar/go/capability"
	"github.com/SemperSupra/wow-sidecar/go/control"
	"github.com/SemperSupra/wow-sidecar/go/protocol"
)

// Binding statically maps one durable operator profile to one product-admitted
// typed capability. The durable request cannot select or override CapabilityID.
type Binding struct {
	Profile      string
	CapabilityID string
}

// BuildHandlers creates control handlers backed by the typed capability
// registry. It deliberately does not expose a generic invocation surface.
func BuildHandlers(
	registry *capability.Registry,
	bindings []Binding,
	verify capability.AuthorityVerifier,
) (map[string]control.Handler, error) {
	if registry == nil {
		return nil, fmt.Errorf("capability registry is required")
	}
	if len(bindings) == 0 {
		return nil, fmt.Errorf("at least one control-capability binding is required")
	}

	descriptors := registry.Advertise(true)
	admitted := make(map[string]capability.Descriptor, len(descriptors))
	for _, descriptor := range descriptors {
		admitted[descriptor.ID] = descriptor
	}

	handlers := make(map[string]control.Handler, len(bindings))
	for _, binding := range bindings {
		if binding.Profile == "" || strings.TrimSpace(binding.Profile) != binding.Profile {
			return nil, fmt.Errorf("binding profile must be a normalized non-empty string")
		}
		if binding.CapabilityID == "" || strings.TrimSpace(binding.CapabilityID) != binding.CapabilityID {
			return nil, fmt.Errorf("binding capability id must be a normalized non-empty string")
		}
		descriptor, ok := admitted[binding.CapabilityID]
		if !ok {
			return nil, fmt.Errorf("binding capability is not admitted")
		}
		if descriptor.AuthorityRequired && verify == nil {
			return nil, fmt.Errorf("authority verifier is required for actuator capability")
		}
		if _, exists := handlers[binding.Profile]; exists {
			return nil, fmt.Errorf("duplicate control profile binding")
		}

		capabilityID := binding.CapabilityID
		handlers[binding.Profile] = func(req protocol.Request) (map[string]any, error) {
			rawInput, err := protocol.CanonicalJSON(req.Inputs)
			if err != nil {
				return nil, fmt.Errorf("canonicalize capability input: %w", err)
			}

			outcome, err := registry.Invoke(
				context.Background(),
				capability.Request{
					CapabilityID:      capabilityID,
					AuthorityRef:      req.Authority.Record,
					AuthorityRevision: req.Authority.Revision,
					AuthorityState:    req.Authority.State,
					Input:             rawInput,
				},
				verify,
			)
			if err != nil {
				return nil, err
			}

			details := map[string]any{
				"result":                       outcome.Result,
				"status":                       outcome.Status,
				"workspace_mutation_performed": outcome.MutationPerformed,
				"retry_authorized":             outcome.RetryAuthorized,
				"capability_id":                capabilityID,
				"capability_data":              outcome.Data,
			}
			if outcome.Result == control.ResultUnknown {
				details["preserve_evidence"] = true
			}
			return details, nil
		}
	}

	return handlers, nil
}
