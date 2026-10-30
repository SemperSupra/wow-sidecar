package wirejson

import (
	"encoding/json"
	"strings"
	"testing"
)

type nestedWire struct {
	State string `json:"state"`
}

type fixedWire struct {
	Nested nestedWire      `json:"nested"`
	Items  []nestedWire    `json:"items"`
	Raw    json.RawMessage `json:"raw"`
}

type customDecoderWire struct {
	State string `json:"state"`
}

func (c *customDecoderWire) UnmarshalJSON(data []byte) error {
	type plain customDecoderWire
	return json.Unmarshal(data, (*plain)(c))
}

func TestValidateRejectsDecodedDuplicateKeysAtEveryDepth(t *testing.T) {
	for _, raw := range []string{
		`{"nested":{"state":"a","state":"b"}}`,
		`{"nested":{"st\u0061te":"a","state":"b"}}`,
		`{"items":[{"state":"a","state":"b"}]}`,
		`{"raw":{"x":1,"x":2}}`,
	} {
		if err := Validate[fixedWire]([]byte(raw)); err == nil {
			t.Fatalf("duplicate key accepted: %s", raw)
		}
	}
}

func TestValidateRejectsStructCaseAliasesButAllowsMapCaseDistinctions(t *testing.T) {
	for _, raw := range []string{
		`{"nested":{"STATE":"a"}}`,
		`{"nested":{"STATE":"a","state":"b"}}`,
	} {
		if err := Validate[fixedWire]([]byte(raw)); err == nil {
			t.Fatalf("struct field case alias accepted: %s", raw)
		}
	}
	if err := Validate[map[string]string]([]byte(`{"State":"upper","state":"lower"}`)); err != nil {
		t.Fatalf("distinct map keys rejected: %v", err)
	}
}

func TestValidateAcceptsCanonicalUniqueKeys(t *testing.T) {
	if err := Validate[fixedWire]([]byte(`{"nested":{"state":"open"},"items":[{"state":"closed"}],"raw":{"State":1,"state":2}}`)); err != nil {
		t.Fatalf("canonical unique document rejected: %v", err)
	}
}

func TestValidateRejectsMalformedTrailingOversizedAndDeepDocuments(t *testing.T) {
	for _, raw := range []string{`{"nested":`, `{"nested":{"state":"ok"}} {}`, strings.Repeat(" ", MaxBytes+1)} {
		if err := Validate[fixedWire]([]byte(raw)); err == nil {
			t.Fatalf("invalid bounded document accepted (len=%d)", len(raw))
		}
	}
	deep := strings.Repeat("[", MaxDepth+2) + `0` + strings.Repeat("]", MaxDepth+2)
	if err := Validate[any]([]byte(deep)); err == nil {
		t.Fatal("over-depth document accepted")
	}
}

func TestValidateFailsClosedOnEmbeddedWireSchema(t *testing.T) {
	type embedded struct{ nestedWire }
	if err := Validate[embedded]([]byte(`{"state":"ok"}`)); err == nil {
		t.Fatal("embedded wire schema without explicit field-resolution support accepted")
	}
}

func TestValidateRejectsCaseAmbiguousAndCustomDecoderSchemas(t *testing.T) {
	type ambiguous struct {
		ID string `json:"id"`
		Id string `json:"Id"`
	}
	if err := Validate[ambiguous]([]byte(`{"id":"a"}`)); err == nil {
		t.Fatal("case-ambiguous wire schema accepted")
	}
	if err := Validate[json.RawMessage]([]byte(`{"state":"ok"}`)); err != nil {
		t.Fatalf("RawMessage dynamic payload rejected: %v", err)
	}
	if err := Validate[customDecoderWire]([]byte(`{"state":"ok"}`)); err == nil {
		t.Fatal("custom JSON decoder schema accepted without an explicit validator")
	}
}

func FuzzValidateNoPanic(f *testing.F) {
	for _, seed := range []string{
		`{"nested":{"state":"open"}}`,
		`{"nested":{"state":"closed","state":"open"}}`,
		`{"nested":{"STATE":"open"}}`,
		`{"raw":{"State":1,"state":2}}`,
		`{"items":[{"state":"ok"}]}`, `null`, `[]`, `"text"`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > MaxBytes+1 {
			t.Skip()
		}
		_ = Validate[fixedWire]([]byte(raw))
	})
}
