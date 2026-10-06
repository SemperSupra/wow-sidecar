package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

func TestAllocateGenerationPersistsAndAdvances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "generation.json")
	first, err := allocateGeneration(path, "sovereign-node")
	if err != nil {
		t.Fatal(err)
	}
	second, err := allocateGeneration(path, "sovereign-node")
	if err != nil {
		t.Fatal(err)
	}
	if first != 1 || second != 2 {
		t.Fatalf("unexpected generations: %d %d", first, second)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state generationState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state.Schema != generationStateSchema || state.NodeID != "sovereign-node" || state.Generation != 2 {
		t.Fatalf("unexpected persisted state: %#v", state)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("generation state permissions = %o want 600", info.Mode().Perm())
	}
}

func TestAllocateGenerationSerializesConcurrentStarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "generation.json")
	const count = 16
	results := make(chan uint64, count)
	errors := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			generation, err := allocateGeneration(path, "cloud-node")
			if err != nil {
				errors <- err
				return
			}
			results <- generation
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	var generations []int
	for generation := range results {
		generations = append(generations, int(generation))
	}
	sort.Ints(generations)
	if len(generations) != count {
		t.Fatalf("got %d generations want %d", len(generations), count)
	}
	for i, generation := range generations {
		if generation != i+1 {
			t.Fatalf("generation sequence at %d = %d", i, generation)
		}
	}
}

func TestGenerationStateFailsClosedOnCorruptionAndIdentityMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "generation.json")
	if err := os.WriteFile(path, []byte("{\"schema\":\"wrong\",\"node_id\":\"node-a\",\"generation\":7}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := allocateGeneration(path, "node-a"); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("bad schema was not rejected: %v", err)
	}

	if err := os.WriteFile(path, []byte("{\"schema\":\"wow-sidecar.generation-state.v1\",\"node_id\":\"node-a\",\"generation\":7}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := allocateGeneration(path, "node-b"); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("node mismatch was not rejected: %v", err)
	}

	if err := os.WriteFile(path, []byte("{\"schema\":\"wow-sidecar.generation-state.v1\",\"node_id\":\"node-a\",\"generation\":7} trailing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := allocateGeneration(path, "node-a"); err == nil {
		t.Fatal("trailing generation-state data was accepted")
	}
}
