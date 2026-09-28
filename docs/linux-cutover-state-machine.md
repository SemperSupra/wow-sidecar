# Legacy-to-WOW systemd cutover state machine

This gate qualifies the **transition semantics** before a real systemd actuator or live host is allowed to use them.

The first legacy-to-WOW cutover intentionally prefers a recoverable availability gap over concurrent workers that could both consume the same durable execution-control state.

## Preconditions

- the new `wow-sidecar-worker.service` unit is already staged;
- it is present, inactive, and disabled;
- the historical host-control unit is present and active;
- the co-resident `wow-sidecar-stack.service` MCP/tunnel role is outside this cutover and is never stopped, disabled, or otherwise mutated;
- release, environment, configuration, recovery capture, and health-probe qualification are separate prerequisites;
- no prior cutover journal is present.

## Write-ahead sequence

1. persist the exact initial candidate/legacy service-state snapshot;
2. enter `quiescing-legacy`;
3. stop the historical host-control unit if active;
4. disable the historical host-control unit if enabled;
5. verify that host-control unit is inactive and disabled while the co-resident MCP/tunnel stack remains outside the mutation set;
6. persist `legacy-quiesced`;
7. persist `starting-candidate`;
8. re-check that legacy is quiesced, then start the candidate **while it is still disabled**;
9. verify candidate active / legacy inactive and persist `candidate-started`;
10. run the independently supplied candidate health probe;
11. persist `committing`;
12. enable the candidate;
13. verify candidate active+enabled and every legacy unit inactive+disabled;
14. persist `committed`.

The journal is written atomically and fsynced under `/var/lib/wow-sidecar/cutover.json`.

## Crash/recovery rule

Any restart that finds a nonterminal journal phase rolls back to the exact initial legacy service state. A restart that finds `committed` verifies and preserves the candidate state.

Rollback first stops and disables the candidate and verifies it inert **before** re-enabling or restarting any legacy service. If candidate quiescence cannot be proven, legacy restart is not attempted. This is the core no-dual-consumer safety boundary.

The public qualification suite injects normal failures and simulated process crashes after every service mutation and across all transient journal checkpoints. It asserts exact legacy restoration and that candidate+legacy are never simultaneously active.

This slice does not invoke real `systemctl`, replace unit files, touch live credentials, mutate Agent Dispatch execution-control state, or perform a live migration. A real systemd backend and HIL cutover remain later gates.

## Role boundary correction

Historical discovery intentionally observes both `wow-sidecar-host-control.service` and `wow-sidecar-stack.service` for recovery purposes. They are not the same lifecycle role.

Only `wow-sidecar-host-control.service` is replaced by the generic WOW trusted-host worker. `wow-sidecar-stack.service` owns the Agent Dispatch MCP/tunnel stack and remains co-resident. The cutover state machine therefore excludes it from the legacy mutation set.
