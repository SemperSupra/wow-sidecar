# Deployment identity

WOW Sidecar supports two explicitly distinct deployment-identity mechanisms:

1. **Clean Git checkout** — used by the conventional Linux compatibility path. The worker derives `operator_revision` from the exact clean checkout HEAD.
2. **Immutable revision file** — intended for container/image packaging where a `.git` checkout does not exist. The file must be absolute, regular, non-symlink, non-writable and contain exactly one 40-hex source revision.

Exactly one mechanism may be selected.

The revision file is not sufficient by itself to prove image integrity. A container integration must separately bind:

- exact image digest;
- exact source revision embedded in the image;
- read-only image filesystem semantics for the revision file;
- candidate provenance from the public WOW source authority.

The runtime only consumes the already-qualified source revision identity. TrueNAS Foundry owns image/App materialization and HIL evidence.
