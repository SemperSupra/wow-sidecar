# Wheelhouse candidate provenance

Conventional Linux deployment consumes an admitted, hash-bound wheelhouse rather than resolving packages during installation.

The public `wheelhouse-candidate.yml` workflow is a **candidate producer and qualification plane**, not an automatic admission authority.

For the exact pull-request source revision it:

1. checks out that exact revision;
2. builds the WOW product wheel twice with a source-derived `SOURCE_DATE_EPOCH` and requires byte-identical SHA-256 output;
3. downloads binary dependency wheels for the pinned direct requirement `PyJWT[crypto]==2.10.1` on the declared public runner/Python substrate;
4. creates a strict `wow-sidecar.wheelhouse.v1` manifest containing the exact source revision and SHA-256 digest of every wheel;
5. re-loads that manifest with the product verifier;
6. constructs the real revision-scoped environment from those wheels with the qualified offline installer;
7. uploads the complete candidate wheelhouse as a short-lived public Actions artifact.

This closes two gaps: the product wheel is proven reproducible within the qualification run, and the exact dependency bytes used by the offline installer are preserved as evidence.

It intentionally does **not** claim that dependency resolution is stable over time. `PyJWT` is directly pinned, but transitive packages can move unless an admitted lock records the candidate filenames/digests. Candidate generation therefore precedes a separate admission/locking gate.

Likewise, the build frontend/backend environment is not yet a trusted release toolchain merely because two builds agree in one runner. A later release-hardening gate may pin or attest the build toolchain if that additional machinery earns its keep.
