//go:build linux

package node

import (
    "bufio"
    "bytes"
    "context"
    "fmt"
    "net/http"
    "net/http/httptest"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "testing"
    "time"
)

// TestRuntimeCrossProcessGenerationOverlap is read-only HIL-free qualification
// of a generation allocation boundary between *separate* Linux processes.
// It does not operate any peer rendezvous endpoint or claim a vulnerability.
func TestRuntimeCrossProcessGenerationOverlap(t *testing.T) {
    const childMode = "WOW_TEST_GENERATION_CHILD"
    const statePathEnv = "WOW_TEST_GENERATION_STATE_PATH"
    if os.Getenv(childMode) == "holder" {
        path := os.Getenv(statePathEnv)
        if path == "" {
            t.Fatal("missing child state path")
        }
        config := runtimeConfig(t)
        config.GenerationStateFile = path
        runtime, err := newRuntime(config, strings.Repeat("a", 40), "overlap-test", time.Now)
        if err != nil {
            t.Fatal(err)
        }
        card, err := runtime.Card()
        if err != nil {
            t.Fatal(err)
        }
        fmt.Printf("GEN_READY:%d\n", card.Generation)
        // Keep the original process and handler alive until the parent starts
        // the replacement using the same generation state file.
        input := bufio.NewReader(os.Stdin)
        if _, err := input.ReadString('\n'); err != nil {
            t.Fatal(err)
        }
        recorder := httptest.NewRecorder()
        runtime.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
        fmt.Printf("OLD_AFTER:%d:%d\n", card.Generation, recorder.Code)
        return
    }

    statePath := filepath.Join(t.TempDir(), "generation.json")
    ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
    defer cancel()
    cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRuntimeCrossProcessGenerationOverlap$")
    cmd.Env = append(os.Environ(), childMode+"=holder", statePathEnv+"="+statePath)
    stdin, err := cmd.StdinPipe()
    if err != nil {
        t.Fatal(err)
    }
    stdout, err := cmd.StdoutPipe()
    if err != nil {
        t.Fatal(err)
    }
    var stderr bytes.Buffer
    cmd.Stderr = &stderr
    if err := cmd.Start(); err != nil {
        t.Fatal(err)
    }
    defer func() {
        cancel()
        _ = cmd.Process.Kill()
        _ = cmd.Wait()
    }()
    output := bufio.NewReader(stdout)
    firstLine, err := output.ReadString('\n')
    if err != nil {
        t.Fatalf("child did not start: %v stderr=%q", err, stderr.String())
    }
    if strings.TrimSpace(firstLine) != "GEN_READY:1" {
        t.Fatalf("unexpected child readiness %q stderr=%q", firstLine, stderr.String())
    }
    config := runtimeConfig(t)
    config.GenerationStateFile = statePath
    replacement, err := newRuntime(config, strings.Repeat("a", 40), "overlap-test", time.Now)
    if err != nil {
        t.Fatal(err)
    }
    card, err := replacement.Card()
    if err != nil {
        t.Fatal(err)
    }
    if card.Generation != 2 {
        t.Fatalf("replacement generation %d, want 2", card.Generation)
    }
    if _, err := fmt.Fprintln(stdin, "inspect"); err != nil {
        t.Fatal(err)
    }
    after, err := output.ReadString('\n')
    if err != nil {
        t.Fatalf("old process stopped before observed: %v stderr=%q", err, stderr.String())
    }
    if strings.TrimSpace(after) != "OLD_AFTER:1:200" {
        t.Fatalf("old process readiness after gen2=%q stderr=%q", after, stderr.String())
    }
    _ = stdin.Close()
    if err := cmd.Wait(); err != nil {
        t.Fatalf("child exit: %v stderr=%q", err, stderr.String())
    }
}
