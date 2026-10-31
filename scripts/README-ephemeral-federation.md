# Same-runner ephemeral federation qualification

This is a bounded test of the existing WOW Sidecar binary. Two disposable cloud
nodes run on loopback using separate generation state. One observes the other,
observes its disappearance, and sees a new generation and incarnation after
restart. A sanitized receipt records the result after both processes and the
workcell are retired. It does not establish remote connectivity, actor admission,
control-plane authority, real task claims, or operational deployment readiness.

The workflow is a public pull-request candidate; publication and execution
remain subject to the owning repository's projection and admission process.
