package node

import (
	"context"
	"fmt"
	"sort"
	"net/http"

	"github.com/SemperSupra/wow-sidecar/go/capability"
)

const capabilityIndexSchema = "wow-sidecar.capability-index.v1"

type capabilityView struct {
	Descriptor capability.Descriptor `json:"descriptor"`
	State      capability.State      `json:"state"`
}

type capabilityIndex struct {
	Schema       string           `json:"schema"`
	Capabilities []capabilityView `json:"capabilities"`
}

func installCoreCapabilities(r *Runtime) (*capability.Registry, []string, error) {
	if r == nil {
		return nil, nil, fmt.Errorf("runtime is required")
	}
	registry := capability.NewRegistry()
	descriptor := capability.Descriptor{
		Schema:            capability.DescriptorSchema,
		ID:                "system.identity",
		Version:           "v1",
		Kind:              capability.KindSensor,
		Effect:            capability.EffectReadOnly,
		Disclosure:        capability.DisclosurePublic,
		AuthorityRequired: false,
	}
	err := registry.Register(capability.Registration{
		Descriptor: descriptor,
		Probe: func(context.Context) (capability.State, error) {
			return capability.State{
				CapabilityID: descriptor.ID,
				Readiness:    capability.ReadinessReady,
				ObservedAt:   r.now().UTC(),
			}, nil
		},
		Handler: func(context.Context, capability.Request) (capability.Outcome, error) {
			return capability.Outcome{
				Result:            "ELIGIBLE",
				Status:            "system-identity-observed",
				MutationPerformed: false,
				RetryAuthorized:   false,
				Data: map[string]any{
					"node_id":         r.config.NodeID,
					"generation":      r.generation,
					"incarnation_id":  r.incarnationID,
					"locality":        r.config.Locality,
					"source_revision": r.sourceRevision,
					"build_version":   r.buildVersion,
				},
			}, nil
		},
	})
	if err != nil {
		return nil, nil, err
	}
	capabilityIDs := []string{descriptor.ID}
	if r.rendezvous != nil {
		id, err := registerRendezvousCapability(registry, r)
		if err != nil {
			return nil, nil, err
		}
		capabilityIDs = append(capabilityIDs, id)
	}
	sort.Strings(capabilityIDs)
	return registry, capabilityIDs, nil
}

func (r *Runtime) capabilityIndex(ctx context.Context) (capabilityIndex, error) {
	descriptors := r.capabilities.Advertise(false)
	views := make([]capabilityView, 0, len(descriptors))
	for _, descriptor := range descriptors {
		state, err := r.capabilities.Observe(ctx, descriptor.ID)
		if err != nil {
			return capabilityIndex{}, err
		}
		views = append(views, capabilityView{Descriptor: descriptor, State: state})
	}
	return capabilityIndex{
		Schema:       capabilityIndexSchema,
		Capabilities: views,
	}, nil
}

func (r *Runtime) handleCapabilities(w http.ResponseWriter, req *http.Request) {
	if !requireGET(w, req) {
		return
	}
	index, err := r.capabilityIndex(req.Context())
	if err != nil {
		http.Error(w, "capability index unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, index)
}

func (r *Runtime) handleSystemIdentity(w http.ResponseWriter, req *http.Request) {
	if !requireGET(w, req) {
		return
	}
	outcome, err := r.capabilities.Invoke(
		req.Context(),
		capability.Request{
			CapabilityID: "system.identity",
			Input:        []byte("{}"),
		},
		nil,
	)
	if err != nil {
		http.Error(w, "identity capability unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, outcome)
}
