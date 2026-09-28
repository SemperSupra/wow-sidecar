# Declarative operator profiles

WOW Sidecar operator admission is configuration, not request-supplied execution.

Schema `wow-sidecar.operator-profile.v1` binds exactly:
- profile name;
- canonical authority record/revision/state;
- one supported operator kind;
- local operator configuration.

Version 1 admits only `pinned-repository`. The document has an exact field set and cannot contain a shell command, request arguments, alternate executable, or arbitrary extension fields.

A deployment may load one or more trusted local profile files, then pass the resulting registry and authority map into the generic worker. The durable request still carries only the profile name plus its exact authority binding and must match the local deployment.

Concrete domain profiles such as the GARM capacity-two operator belong to the domain that owns the invoked operator and campaign. WOW Sidecar owns this schema/loader, not those domain values.
