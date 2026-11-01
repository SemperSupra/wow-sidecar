//go:build linux

package node

import (
    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"
)

func TestGenerationLeaseRejectsSupersededBeforeReplayOrMutation(t *testing.T) {
    cfg := runtimeConfig(t)
    old, err := newRuntime(cfg, strings.Repeat("a", 40), "guard-test", time.Now)
    if err != nil { t.Fatal(err) }
    calls := 0
    if err := withCurrentGenerationLease(cfg.GenerationStateFile, cfg.NodeID, old.generation, func() error {
        calls++
        return nil
    }); err != nil { t.Fatal(err) }
    newer, err := newRuntime(cfg, strings.Repeat("a", 40), "guard-test", time.Now)
    if err != nil { t.Fatal(err) }
    if newer.generation != old.generation+1 { t.Fatal("replacement did not advance generation") }

    if err := withCurrentGenerationLease(cfg.GenerationStateFile, cfg.NodeID, old.generation, func() error {
        calls++
        return nil
    }); err == nil {
        t.Fatal("stale generation was allowed to enter guarded mutation")
    }
    if calls != 1 { t.Fatalf("stale callback entered: %d", calls) }
    if err := withCurrentGenerationLease(cfg.GenerationStateFile, cfg.NodeID, newer.generation, func() error {
        calls++
        return nil
    }); err != nil { t.Fatal(err) }
    if calls != 2 { t.Fatalf("current callback count=%d", calls) }
}

func TestGenerationLeaseSerializesNewGenerationWithGuardedCallback(t *testing.T) {
    path := filepath.Join(t.TempDir(), "generation.json")
    first, err := allocateGeneration(path, "peer-test-node")
    if err != nil { t.Fatal(err) }
    done := make(chan struct{})
    errs := make(chan error, 1)
    err = withCurrentGenerationLease(path, "peer-test-node", first, func() error {
        go func() {
            _, generationErr := allocateGeneration(path, "peer-test-node")
            errs <- generationErr
            close(done)
        }()
        select {
        case <-done:
            t.Error("replacement allocation committed while mutation guard held")
        case <-time.After(120 * time.Millisecond):
            // A writer is prevented from entering until this guard returns.
        }
        return nil
    })
    if err != nil { t.Fatal(err) }
    select {
    case <-done:
        if err := <-errs; err != nil { t.Fatal(err) }
    case <-time.After(5 * time.Second):
        t.Fatal("replacement generation remained blocked after shared guard released")
    }
}

func TestGenerationLeaseFailsClosedOnInvalidState(t *testing.T) {
    path := filepath.Join(t.TempDir(), "generation.json")
    gen, err := allocateGeneration(path, "node-01")
    if err != nil { t.Fatal(err) }
    if err := os.WriteFile(path, []byte("{invalid-json"), 0o600); err != nil { t.Fatal(err) }
    called := false
    err = withCurrentGenerationLease(path, "node-01", gen, func() error {
        called = true
        return nil
    })
    if err == nil || called { t.Fatalf("invalid generation state did not fail closed: err=%v called=%v", err, called) }
}
