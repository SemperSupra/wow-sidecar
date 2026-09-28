# Conventional Linux systemd layout

The conventional Linux integration uses FHS-style product locations:

- immutable source checkout: `/usr/local/lib/wow-sidecar/releases/<revision>`;
- revision-specific virtual environment: `/usr/local/lib/wow-sidecar/venvs/<revision>`;
- configuration and profiles: `/etc/wow-sidecar`;
- mutable local state/recovery: `/var/lib/wow-sidecar`;
- service unit: `/etc/systemd/system/wow-sidecar-worker.service`.

The rendered unit runs as a dedicated `wow-sidecar` user/group, not root, and binds the worker to the exact release checkout via `--repo-root`. The worker therefore continues to derive `operator_revision` from a clean exact Git HEAD.

This slice renders and observes the Linux layout only. It performs no clone, package install, user creation, systemd activation, service stop/start, rollback, or uninstall. Those mutation stages must consume the already-qualified lifecycle/recovery contract and are separately gated.
