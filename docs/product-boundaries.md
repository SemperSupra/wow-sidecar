# Product boundaries

## WOW Sidecar

WOW Sidecar is the trusted-host execution boundary.

It accepts a deliberately narrow durable request contract and converts it into one registered local capability invocation after verifying exact operator revision, exact canonical authority, and profile-specific constraints.

It owns:

- trusted-host request discovery;
- create-once claims;
- canonical authority reread;
- registered operator admission;
- local credential isolation;
- sanitized immutable receipts;
- no-retry/UNKNOWN semantics;
- deployment-target lifecycle of the Sidecar itself.

## Agent Dispatch

Agent Dispatch owns planning, decomposition, delegation, reconciliation, retry/replan decisions, and selection of an authorized actuator. It may publish bounded WOW Sidecar requests but does not own the Sidecar worker lifecycle.

## GARM / TrueNAS

GARM owns desired runner capacity and runner orchestration.

`garm-provider-truenas` translates qualified GARM lifecycle requests into supported TrueNAS middleware operations.

TrueNAS App Foundry owns GARM appliance packaging/materialization and its qualification/promotion process.

WOW Sidecar may invoke a fixed, separately qualified GARM operator profile. That does not transfer GARM, provider, or Foundry authority into WOW Sidecar.

## Durable execution-control repository

`SemperSupra/agent-dispatch-execution-control-private` is a runtime coordination surface for immutable request/claim/receipt refs. It is not source authority for WOW Sidecar or Agent Dispatch and is not canonical execution authority.

## Dependency direction

```text
Agent Dispatch / other authorized controllers
                  |
                  v
     durable request/claim/receipt plane
                  |
                  v
             WOW Sidecar
                  |
          admitted operator profile
                  |
          +-------+---------+
          |                 |
          v                 v
   GARM/Foundry       other host capability
```

Each domain retains its own authority and qualification boundary.
