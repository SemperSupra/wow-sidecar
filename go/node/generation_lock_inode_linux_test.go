//go:build linux

package node

import (
    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"
)

// A substituted symlink/hard-link must never create a second usable lock
// identity. The same fail-closed path applies both to generation allocation
// and to replay/mutation admission in the already configured peer runtime.
func TestGenerationLockRejectsLinkedAndNonregularInodes(t *testing.T) {
    for _, mode := range []string{"symlink", "hardlink", "directory"} {
        t.Run(mode, func(t *testing.T) {
            cfg := runtimeConfig(t)
            lockPath := cfg.GenerationStateFile + ".lock"
            if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil { t.Fatal(err) }
            target := filepath.Join(t.TempDir(), "another-lock")
            if err := os.WriteFile(target, []byte{}, 0o600); err != nil { t.Fatal(err) }
            switch mode {
            case "symlink":
                if err := os.Symlink(target, lockPath); err != nil { t.Fatal(err) }
            case "hardlink":
                if err := os.Link(target, lockPath); err != nil { t.Fatal(err) }
            case "directory":
                if err := os.Mkdir(lockPath, 0o700); err != nil { t.Fatal(err) }
            }
            if _, err := newRuntime(cfg, strings.Repeat("a", 40), "fence-test", time.Now); err == nil {
                t.Fatal("generation allocation admitted substituted lock inode")
            }
            callbackCalls := 0
            err := withCurrentGenerationLease(cfg.GenerationStateFile, cfg.NodeID, 1, func() error {
                callbackCalls++
                return nil
            })
            if err == nil || callbackCalls != 0 {
                t.Fatalf("generation guard admitted substituted lock: err=%v callback=%d", err, callbackCalls)
            }
        })
    }
}

func TestGenerationLockRejectsWorldAccessibleLock(t *testing.T) {
    cfg := runtimeConfig(t)
    lockPath := cfg.GenerationStateFile + ".lock"
    if err := os.WriteFile(lockPath, []byte{}, 0o600); err != nil { t.Fatal(err) }
    if err := os.Chmod(lockPath, 0o666); err != nil { t.Fatal(err) }
    if _, err := newRuntime(cfg, strings.Repeat("a", 40), "fence-test", time.Now); err == nil {
        t.Fatal("generation allocation admitted overly permissive lock inode")
    }
}
