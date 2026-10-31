# Same-runner ephemeral federation qualification

This is a bounded test of the existing WOW Sidecar binary. Two disposable cloud
nodes (one cloud, one synthetic sovereign) run on loopback using separate generation state. They observe each other,
one observes its peer's disappearance, and sees a new generation and incarnation
after restart. A sanitized receipt records the result after both processes and the
workcell are retired. It does not establish remote connectivity, actor admission,
control-plane authority, real task claims, or operational deployment readiness.

Port selection is subject to a benign loopback allocation race; these identity
checks are not authenticated process ownership. When a child survives cleanup,
its PID is retained only in a local temporary workcell, not the public receipt.
The updated workflow is a public candidate; publication and execution
remain subject to the owning repository's projection and admission process.
