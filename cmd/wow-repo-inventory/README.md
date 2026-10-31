# Local Git worktree inventory (G8/G9)

Run in the authorized checkout:

```sh
go run ./cmd/wow-repo-inventory
```

This is a standard-library Go observer. It invokes read-only Git queries for
worktrees, branches, status and stash counts. It never changes source or
worktree lifecycle state and requires no network or elevated permission.

Output is JSON. It contains worktree indices, exact Git heads and branch
names, dirty counts, tracking and stash counts, and hashes of selected
backlog files. It omits absolute paths, untracked filenames, file bodies,
remote URLs and credentials. Review the output before sending it anywhere:
branch names can themselves contain internal information.

Join the local snapshot with GitHub PR heads and DLE ownership in **private
authority**. Do not push snapshots to a public repository or treat a prunable
worktree as permission to delete it. Untracked work, concurrent writers and
stashes require explicit reconciliation. No deletion, cleanup or merge is
authorized by this inventory.
