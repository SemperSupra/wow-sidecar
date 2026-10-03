package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type conformanceVector struct {
	ID            string         `json:"id"`
	Value         map[string]any `json:"value"`
	Base          string         `json:"base"`
	Mutate        map[string]any `json:"mutate"`
	Accept        bool           `json:"accept"`
	ErrorContains string         `json:"error_contains"`
}

type conformanceCorpus struct {
	Schema          string `json:"schema"`
	OracleRevision  string `json:"oracle_revision"`
	RequestVectors  []conformanceVector `json:"request_vectors"`
	ProfileVectors  []conformanceVector `json:"profile_vectors"`
	CanonicalVectors []struct {
		ID            string         `json:"id"`
		Value         map[string]any `json:"value"`
		CanonicalJSON string         `json:"canonical_json"`
	} `json:"canonical_vectors"`
	ReceiptVectors []struct {
		ID       string         `json:"id"`
		Args     ReceiptArgs    `json:"args"`
		Expected map[string]any `json:"expected"`
	} `json:"receipt_vectors"`
}

func loadCorpus(t *testing.T) conformanceCorpus {
	t.Helper()
	path := filepath.Join("..", "..", "conformance", "v1", "vectors.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read conformance vectors: %v", err)
	}
	var corpus conformanceCorpus
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("decode conformance vectors: %v", err)
	}
	if corpus.Schema != "wow-sidecar.conformance.v1" {
		t.Fatalf("unexpected corpus schema: %q", corpus.Schema)
	}
	return corpus
}

func vectorIndex(vectors []conformanceVector) map[string]conformanceVector {
	out := make(map[string]conformanceVector, len(vectors))
	for _, vector := range vectors {
		out[vector.ID] = vector
	}
	return out
}

func deepCopyMap(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("copy marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("copy unmarshal: %v", err)
	}
	return out
}

func setPath(t *testing.T, value map[string]any, dotted string, replacement any) {
	t.Helper()
	parts := strings.Split(dotted, ".")
	cursor := value
	for _, part := range parts[:len(parts)-1] {
		next, ok := cursor[part].(map[string]any)
		if !ok {
			t.Fatalf("path %q is not an object at %q", dotted, part)
		}
		cursor = next
	}
	cursor[parts[len(parts)-1]] = replacement
}

func materializeVector(t *testing.T, vector conformanceVector, index map[string]conformanceVector) map[string]any {
	t.Helper()
	var value map[string]any
	if vector.Value != nil {
		value = deepCopyMap(t, vector.Value)
	} else {
		base, ok := index[vector.Base]
		if !ok {
			t.Fatalf("missing base vector %q", vector.Base)
		}
		value = deepCopyMap(t, base.Value)
	}
	if vector.Mutate == nil {
		return value
	}
	for _, key := range []string{"set", "add", "add_top_level"} {
		raw, ok := vector.Mutate[key]
		if !ok {
			continue
		}
		pair, ok := raw.([]any)
		if !ok || len(pair) != 2 {
			t.Fatalf("invalid mutation %s: %#v", key, raw)
		}
		path, ok := pair[0].(string)
		if !ok {
			t.Fatalf("invalid mutation path: %#v", pair[0])
		}
		if key == "add_top_level" {
			value[path] = pair[1]
		} else {
			setPath(t, value, path, pair[1])
		}
		return value
	}
	t.Fatalf("unknown mutation: %#v", vector.Mutate)
	return nil
}

func marshalVector(t *testing.T, value map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal vector: %v", err)
	}
	return raw
}

func TestRequestConformanceVectors(t *testing.T) {
	corpus := loadCorpus(t)
	index := vectorIndex(corpus.RequestVectors)
	for _, vector := range corpus.RequestVectors {
		vector := vector
		t.Run(vector.ID, func(t *testing.T) {
			value := materializeVector(t, vector, index)
			_, err := ParseRequest(marshalVector(t, value))
			if vector.Accept {
				if err != nil {
					t.Fatalf("accepted vector rejected: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("rejected vector was accepted")
			}
			if !strings.Contains(err.Error(), vector.ErrorContains) {
				t.Fatalf("error %q does not contain %q", err, vector.ErrorContains)
			}
		})
	}
}

func TestProfileConformanceVectors(t *testing.T) {
	corpus := loadCorpus(t)
	index := vectorIndex(corpus.ProfileVectors)
	for _, vector := range corpus.ProfileVectors {
		vector := vector
		t.Run(vector.ID, func(t *testing.T) {
			value := materializeVector(t, vector, index)
			_, err := ParseProfile(marshalVector(t, value))
			if vector.Accept {
				if err != nil {
					t.Fatalf("accepted vector rejected: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("rejected vector was accepted")
			}
			if !strings.Contains(err.Error(), vector.ErrorContains) {
				t.Fatalf("error %q does not contain %q", err, vector.ErrorContains)
			}
		})
	}
}

func TestCanonicalJSONConformanceVectors(t *testing.T) {
	corpus := loadCorpus(t)
	for _, vector := range corpus.CanonicalVectors {
		got, err := CanonicalJSON(vector.Value)
		if err != nil {
			t.Fatalf("%s: %v", vector.ID, err)
		}
		if string(got) != vector.CanonicalJSON {
			t.Fatalf("%s: got %q want %q", vector.ID, got, vector.CanonicalJSON)
		}
	}
}

func TestReceiptConformanceVectors(t *testing.T) {
	corpus := loadCorpus(t)
	for _, vector := range corpus.ReceiptVectors {
		receipt, err := NewReceipt(vector.Args)
		if err != nil {
			t.Fatalf("%s: %v", vector.ID, err)
		}
		got, err := CanonicalJSON(receipt)
		if err != nil {
			t.Fatalf("%s canonicalize receipt: %v", vector.ID, err)
		}
		want, err := CanonicalJSON(vector.Expected)
		if err != nil {
			t.Fatalf("%s canonicalize expected: %v", vector.ID, err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s: got %s want %s", vector.ID, got, want)
		}
	}
}

func TestAuthorityBindingAndPreExecutionState(t *testing.T) {
	corpus := loadCorpus(t)
	base := corpus.RequestVectors[0].Value
	req, err := ParseRequest(marshalVector(t, base))
	if err != nil {
		t.Fatalf("parse base request: %v", err)
	}
	if err := ValidateAuthorityBinding(req, AuthorityBinding{
		Record: req.Authority.Record,
		Revision: req.Authority.Revision,
		State: "open",
	}); err != nil {
		t.Fatalf("valid binding rejected: %v", err)
	}

	pending, err := DecidePreExecution(req.RequestID, strings.Repeat("1", 40), map[string]string{}, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if pending.State != "pending-unclaimed" || !pending.ClaimRequired || pending.ExecuteAllowed {
		t.Fatalf("unexpected pending decision: %#v", pending)
	}

	claimSHA := strings.Repeat("2", 40)
	claimed, err := DecidePreExecution(
		req.RequestID,
		strings.Repeat("1", 40),
		map[string]string{req.RequestID: claimSHA},
		map[string]string{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.State != "claimed-without-receipt" || claimed.Result == nil || *claimed.Result != "UNKNOWN" || claimed.RetryAuthorized {
		t.Fatalf("unexpected claimed decision: %#v", claimed)
	}

	receiptSHA := strings.Repeat("3", 40)
	receipted, err := DecidePreExecution(
		req.RequestID,
		strings.Repeat("1", 40),
		map[string]string{},
		map[string]string{req.RequestID: receiptSHA},
	)
	if err != nil {
		t.Fatal(err)
	}
	if receipted.State != "already-receipted" || receipted.ReceiptCommitSHA != receiptSHA || receipted.ExecuteAllowed {
		t.Fatalf("unexpected receipted decision: %#v", receipted)
	}
}
