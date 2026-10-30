package capability

import (
	"encoding/json"
	"testing"
)

func TestDecodeStrictInputRejectsAmbiguousStructFields(t *testing.T) {
	type input struct {
		State string `json:"state"`
	}
	for _, raw := range []string{
		`{"state":"closed","state":"open"}`,
		`{"st\u0061te":"closed","state":"open"}`,
		`{"STATE":"closed","state":"open"}`,
	} {
		if _, err := DecodeStrictInput[input](json.RawMessage(raw)); err == nil {
			t.Fatalf("ambiguous struct fields accepted: %s", raw)
		}
	}
	got, err := DecodeStrictInput[input](json.RawMessage(`{"state":"open"}`))
	if err != nil || got.State != "open" {
		t.Fatalf("canonical unique key rejected: got=%+v err=%v", got, err)
	}
}

func TestDecodeStrictInputPreservesDistinctCaseMapKeys(t *testing.T) {
	got, err := DecodeStrictInput[map[string]string](json.RawMessage(`{"State":"upper","state":"lower"}`))
	if err != nil {
		t.Fatalf("case-distinct map keys rejected: %v", err)
	}
	if got["State"] != "upper" || got["state"] != "lower" {
		t.Fatalf("case-distinct map keys changed: %#v", got)
	}
}

func TestValidateRequestRejectsDuplicateDynamicInputKeys(t *testing.T) {
	descriptor := sensorDescriptor("system.identity", DisclosurePublic)
	duplicate := Request{
		CapabilityID: descriptor.ID,
		Input:        json.RawMessage(`{"nested":{"state":"closed","state":"open"}}`),
	}
	if err := validateRequest(duplicate, descriptor); err == nil {
		t.Fatal("duplicate dynamic capability input keys accepted")
	}
	distinctCase := Request{
		CapabilityID: descriptor.ID,
		Input:        json.RawMessage(`{"State":"upper","state":"lower"}`),
	}
	if err := validateRequest(distinctCase, descriptor); err != nil {
		t.Fatalf("case-distinct dynamic map keys rejected: %v", err)
	}
}
