//go:build linux

package node

import (
    "bufio"
    "bytes"
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "testing"
    "time"
)

// TestGenerationLeaseRejectsOldProcessBeforeCallback proves the process
// boundary using two independent Linux test-binary processes sharing an exact
// generation-state file. The callback represents replay/mutation admission and
// deliberately does no actual peer or capability work.
func TestGenerationLeaseRejectsOldProcessBeforeCallback(t *testing.T) {
    const mode = "WOW_TEST_FENCE_CHILD"
    const stateEnv = "WOW_TEST_FENCE_STATE"
    if os.Getenv(mode) == "old" {
        statePath := os.Getenv(stateEnv)
        if statePath == "" { t.Fatal("child state missing") }
        config := runtimeConfig(t)
        config.GenerationStateFile = statePath
        first, err := newRuntime(config, strings.Repeat("a", 40), "fence-child", time.Now)
        if err != nil { t.Fatal(err) }
        fmt.Printf("OLD_READY:%d\n", first.generation)
        input := bufio.NewReader(os.Stdin)
        if _, err := input.ReadString('\n'); err != nil { t.Fatal(err) }
        entered := 0
        err = withCurrentGenerationLease(statePath, config.NodeID, first.generation, func() error {
            entered++
            return nil
        })
        if err == nil || entered != 0 {
            t.Fatalf("old process callback entered after replacement: error=%v count=%d", err, entered)
        }
        fmt.Printf("OLD_REJECTED:0\n")
        return
    }

    statePath := filepath.Join(t.TempDir(), "generation.json")
    cmd := exec.Command(os.Args[0], "-test.run=^TestGenerationLeaseRejectsOldProcessBeforeCallback$")
    cmd.Env = append(os.Environ(), mode+"=old", stateEnv+"="+statePath)
    stdin, err := cmd.StdinPipe()
    if err != nil { t.Fatal(err) }
    stdout, err := cmd.StdoutPipe()
    if err != nil { t.Fatal(err) }
    var stderr bytes.Buffer
    cmd.Stderr = &stderr
    if err := cmd.Start(); err != nil { t.Fatal(err) }
    // A failed assertion does not leave the child process behind.
    defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
    reader := bufio.NewReader(stdout)
    ready, err := reader.ReadString('\n')
    if err != nil { t.Fatalf("old process failed startup: %v stderr=%q", err, stderr.String()) }
    if strings.TrimSpace(ready) != "OLD_READY:1" {
        t.Fatalf("old generation identity %q stderr=%q", ready, stderr.String())
    }
    cfg := runtimeConfig(t)
    cfg.GenerationStateFile = statePath
    second, err := newRuntime(cfg, strings.Repeat("a", 40), "fence-parent", time.Now)
    if err != nil { t.Fatal(err) }
    if second.generation != 2 { t.Fatalf("replacement generation %d, want 2", second.generation) }
    if _, err := fmt.Fprintln(stdin, "probe"); err != nil { t.Fatal(err) }
    rejected, err := reader.ReadString('\n')
    if err != nil { t.Fatalf("old process exited early: %v stderr=%q", err, stderr.String()) }
    if strings.TrimSpace(rejected) != "OLD_REJECTED:0" {
        t.Fatalf("old process was not fenced: %q stderr=%q", rejected, stderr.String())
    }
    _ = stdin.Close()
    // Poll process liveness through a bounded 5-second completion channel;
    // do not block the test forever on a stuck child.
    done := make(chan error, 1)
    go func() { done <- cmd.Wait() }()
    select {
    case err := <-done:
        if err != nil { t.Fatalf("old child exited badly: %v stderr=%q", err, stderr.String()) }
    case <-time.After(5 * time.Second):
        t.Fatal("old child did not finish within 5s")
    }
}
