package main

import "testing"

func TestWorktreeParse(t *testing.T) {
    raw := "worktree /tmp/example-one\nHEAD aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\nbranch refs/heads/main\n\n" +
        "worktree /tmp/example-two\nHEAD bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\ndetached\nlocked busy\n"
    result, err := parseWorktrees(raw)
    if err != nil { t.Fatal(err) }
    if len(result) != 2 || result[0].Branch != "main" ||
        !result[1].Detached || !result[1].Locked {
        t.Fatalf("unexpected parsed worktrees: %+v", result)
    }
}

func TestLocalBranchAndDirtyCount(t *testing.T) {
    raw := "feature/a\taaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\torigin/feature/a\t[behind 1]\n" +
        "main\tbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\t\t\n"
    branches := parseBranches(raw)
    if len(branches) != 2 || branches[0].Tracking != "[behind 1]" ||
        branches[1].Name != "main" { t.Fatalf("branch parse: %+v", branches) }
    if countStatus(" M a.txt\n?? x.txt\n") != 2 { t.Fatal("missing status") }
}

func TestMalformedWorktreeRejected(t *testing.T) {
    for _, raw := range []string{"", "HEAD aaa\n", "worktree /x\nbranch refs/heads/main\n"} {
        if _, err := parseWorktrees(raw); err == nil { t.Fatalf("accepted malformed %q", raw) }
    }
}
