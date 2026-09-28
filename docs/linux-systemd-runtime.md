# Bounded systemd runtime adapter

This adapter is the first concrete systemd actuator for the qualified cutover state machine. Its mutation surface is intentionally smaller than general `systemctl`.

## Command boundary

The adapter:

- invokes only the admitted absolute binary `/usr/bin/systemctl`;
- never invokes a shell;
- always adds `--no-ask-password --no-pager`;
- may observe exactly three unit identities:
  - `wow-sidecar-worker.service`;
  - `wow-sidecar-host-control.service`;
  - co-resident `wow-sidecar-stack.service`;
- may mutate only `wow-sidecar-worker.service` and `wow-sidecar-host-control.service`; the MCP/tunnel stack is observation-only;
- exposes only `start`, `stop`, `enable`, `disable`, `daemon-reload`, state observation, and a bounded candidate-health probe;
- converts nonzero command results to a sanitized error without surfacing stdout/stderr.

## First-migration systemd-state admission

The cutover journal stores enabled state as a boolean. Systemd has additional states such as `enabled-runtime`, `static`, `masked`, `linked`, and others whose semantics cannot be restored exactly by that binary model.

The first migration therefore admits only loaded units whose `UnitFileState` is exactly `enabled` or `disabled`. Transitional/failed active states and nonbinary unit-file states fail closed. A later extension must explicitly model additional states before they can be admitted.

## Candidate unit staging

The candidate unit is create-only under `/etc/systemd/system/wow-sidecar-worker.service`:

- an exact existing regular file is accepted idempotently;
- a differing existing target is never overwritten;
- symlinked privileged path components are rejected;
- new content is fsynced before create-only hard-link publication;
- a racing target causes failure instead of replacement.

After staging, preparation performs `daemon-reload` and requires the candidate to observe as present, inactive, and disabled.

## Candidate health

The bounded post-start health probe requires:

- loaded;
- active;
- substate `running`;
- positive `MainPID`;
- zero restarts;
- `NeedDaemonReload=no`;
- fragment path exactly `/etc/systemd/system/wow-sidecar-worker.service`.

This is a systemd/process health gate, not yet an end-to-end execution-control compatibility proof.

Public tests use a scripted fake command runner and synthetic filesystem root. They assert exact argv and state parsing. No public qualification test calls the host's real systemd instance.
