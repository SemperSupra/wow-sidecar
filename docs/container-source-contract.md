# Container source contract

The WOW container definition is a generic product packaging surface. It contains no TrueNAS, GARM or Agent Dispatch policy.

A qualified build must supply:

- `PYTHON_BASE` as an exact `name@sha256:<64-hex>` image reference;
- `WOW_SOURCE_REVISION` as the exact 40-hex public WOW source revision being built.

The image:

- installs the Python package from the candidate source;
- bakes the exact source revision into `/usr/share/wow-sidecar/source-revision` with mode 0444;
- runs as the dedicated non-root `wow-sidecar` user;
- executes `wow-sidecar-worker`.

A build run must independently verify the embedded revision through the package's `revision_from_file` logic and record the built image ID. Publishing to a registry and qualifying a TrueNAS App remain later authorities.
