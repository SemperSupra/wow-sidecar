# Conventional Linux offline environment

The revision-scoped Linux environment consumes a prequalified wheelhouse. It does not resolve dependencies from a package index.

The wheelhouse contains:
- `manifest.json` with schema `wow-sidecar.wheelhouse.v1`;
- the exact 40-hex WOW source revision;
- an explicit set of wheel basenames and SHA-256 digests;
- exactly one `wow_sidecar-*.whl` artifact.

Before mutation, the installer verifies the manifest schema, exact source revision, exact wheel set, regular/non-symlink wheel files, and every wheel digest. It installs the enumerated wheels with `pip --no-index --no-deps`, with user-site/Python path injection removed and pip index configuration disabled.

A virtual environment is created directly at `/usr/local/lib/wow-sidecar/venvs/<revision>`. This is intentional: Python console-script shebangs embed the environment's absolute path, so building a venv under a temporary directory and renaming it would produce stale interpreter paths. If creation or verification fails, only the newly created revision-scoped environment is removed.

The installed `wow-sidecar-worker --help` path is executed as an import/entrypoint smoke test, then an immutable environment stamp records only the source revision and wheelhouse-manifest digest.

This stage does **not** change configuration, replace the systemd unit, invoke `systemctl`, stop/start the historical Agent Dispatch Sidecar, or touch durable request/claim/receipt state.

The wheelhouse manifest proves byte identity, not publisher trust. Producing and admitting the real WOW/dependency wheel set remains a separate provenance gate.
