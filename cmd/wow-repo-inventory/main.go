// wow-repo-inventory prints a bounded, sanitized local Git worktree/branch
// report. It only uses read-only Git subcommands and never changes worktrees.
package main

import (
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "sort"
    "strings"
)

type Worktree struct {
    Index int `json:"index"`
    Head string `json:"head,omitempty"`
    Branch string `json:"branch,omitempty"`
    Detached bool `json:"detached,omitempty"`
    Locked bool `json:"locked,omitempty"`
    Prunable bool `json:"prunable,omitempty"`
    DirtyCount int `json:"dirty_count"`
    Status string `json:"status"`
    Backlog []BacklogFile `json:"backlog,omitempty"`
    path string
}

type BacklogFile struct {
    Name string `json:"name"`
    SHA256 string `json:"sha256"`
}

type Branch struct {
    Name string `json:"name"`
    Head string `json:"head"`
    Upstream string `json:"upstream,omitempty"`
    Tracking string `json:"tracking,omitempty"`
}

type Report struct {
    Schema string `json:"schema"`
    RepositoryHead string `json:"repository_head"`
    Worktrees []Worktree `json:"worktrees"`
    Branches []Branch `json:"branches"`
    StashCount int `json:"stash_count"`
    Limitations []string `json:"limitations"`
}

func git(path string, args ...string) (string, error) {
    a := append([]string{"-c", "core.fsmonitor=false", "-C", path}, args...)
    cmd := exec.Command("git", a...)
    cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
    output, err := cmd.Output()
    if err != nil {
        // Suppress stderr; callers must not accidentally print local paths,
        // remotes containing credentials or other site-sensitive data.
        return "", fmt.Errorf("read-only git %s failed: %w", args[0], err)
    }
    return string(output), nil
}

func parseWorktrees(raw string) ([]Worktree, error) {
    var out []Worktree
    for _, line := range strings.Split(raw, "\n") {
        if line == "" { continue }
        k, v, _ := strings.Cut(line, " ")
        if k == "worktree" {
            out = append(out, Worktree{Index: len(out) + 1, path: v, Status: "UNKNOWN"})
            continue
        }
        if len(out) == 0 { return nil, fmt.Errorf("worktree porcelain is missing a header") }
        w := &out[len(out)-1]
        switch k {
        case "HEAD": w.Head = v
        case "branch": w.Branch = strings.TrimPrefix(v, "refs/heads/")
        case "detached": w.Detached = true
        case "locked": w.Locked = true
        case "prunable": w.Prunable = true
        case "bare": w.Status = "BARE"
        }
    }
    if len(out) == 0 { return nil, fmt.Errorf("no worktrees found") }
    for _, w := range out {
        if w.path == "" || len(w.Head) != 40 { return nil, fmt.Errorf("incomplete worktree porcelain") }
    }
    return out, nil
}

func parseBranches(raw string) []Branch {
    var branches []Branch
    for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
        if line == "" { continue }
        p := strings.SplitN(line, "\t", 4)
        if len(p) < 2 { continue }
        b := Branch{Name: p[0], Head: p[1]}
        if len(p) > 2 { b.Upstream = p[2] }
        if len(p) > 3 { b.Tracking = p[3] }
        branches = append(branches, b)
    }
    sort.Slice(branches, func(i, j int) bool { return branches[i].Name < branches[j].Name })
    return branches
}

func countStatus(raw string) int {
    n := 0
    for _, line := range strings.Split(raw, "\n") {
        if line != "" { n++ }
    }
    return n
}

func backlog(worktree string) []BacklogFile {
    var found []BacklogFile
    // Only known, narrow paths. Do not print file bodies or arbitrary matches.
    for _, name := range []string{"AGENTS.md", "TODO.md", "BACKLOG.md", ".local/backlog.md"} {
        raw, err := os.ReadFile(filepath.Join(worktree, name))
        if err != nil { continue }
        h := sha256.Sum256(raw)
        found = append(found, BacklogFile{Name: name, SHA256: hex.EncodeToString(h[:])})
    }
    return found
}

func run() (Report, error) {
    var report Report
    report.Schema = "wow-sidecar.local-repo-inventory.v1"
    report.Limitations = []string{
        "Local paths, file contents, Git remotes and credentials deliberately omitted",
        "Branch/ref existence does not imply ownership, review, merge safety or cleanup permission",
        "No remote GitHub PR/issue inventory; join by exact branch head in private DLE",
        "Untracked and dirty state is counted, never printed or modified",
    }
    root, err := git(".", "rev-parse", "--show-toplevel")
    if err != nil { return report, err }
    dir := strings.TrimSpace(root)
    report.RepositoryHead, err = git(dir, "rev-parse", "HEAD")
    if err != nil { return report, err }
    report.RepositoryHead = strings.TrimSpace(report.RepositoryHead)
    out, err := git(dir, "worktree", "list", "--porcelain")
    if err != nil { return report, err }
    report.Worktrees, err = parseWorktrees(out)
    if err != nil { return report, err }
    for i := range report.Worktrees {
        w := &report.Worktrees[i]
        if w.Prunable { w.Status = "PRUNABLE_NOT_DELETED"; continue }
        if w.Status == "BARE" { continue }
        dirty, err := git(w.path, "status", "--porcelain=v1", "--untracked-files=normal")
        if err != nil { w.Status = "STATUS_UNKNOWN"; continue }
        w.DirtyCount = countStatus(dirty)
        w.Status = "OBSERVED"
        w.Backlog = backlog(w.path)
    }
    branchRaw, err := git(dir, "for-each-ref",
        "--format=%(refname:short)%09%(objectname)%09%(upstream:short)%09%(upstream:track)",
        "refs/heads")
    if err != nil { return report, err }
    report.Branches = parseBranches(branchRaw)
    stash, err := git(dir, "stash", "list", "--format=%H")
    if err != nil { return report, err }
    report.StashCount = countStatus(stash)
    for i := range report.Worktrees { report.Worktrees[i].path = "" }
    return report, nil
}

func main() {
    if len(os.Args) != 1 {
        fmt.Fprintln(os.Stderr, "usage: go run ./cmd/wow-repo-inventory (no mutation flags)")
        os.Exit(2)
    }
    r, err := run()
    if err != nil {
        fmt.Fprintf(os.Stderr, "inventory blocked: %v\n", err)
        os.Exit(1)
    }
    encoder := json.NewEncoder(os.Stdout)
    encoder.SetIndent("", "  ")
    if err := encoder.Encode(r); err != nil { os.Exit(1) }
}
