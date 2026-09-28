# Legacy Agent Dispatch installation discovery

This integration recognizes the historical Agent Dispatch Sidecar host layout only so that it can be migrated safely. It is not a WOW core contract.

Observed historical locations:

- source checkout: `/opt/wow-sidecar`;
- configuration/credential directory: `/etc/wow-sidecar`;
- historical units: `wow-sidecar-host-control.service` and `wow-sidecar-stack.service`;
- local state root: `/var/lib/wow-sidecar`.

Discovery is read-only. It reports:

- whether the historical source path exists;
- exact Git HEAD and only the **count** of dirty entries when available;
- top-level config file metadata/digests without contents;
- known unit metadata/digests;
- absolute `*_FILE` references from simple env files, excluding container-only `/run/...` paths;
- whether the legacy state root exists.

The local observation retains paths so a later recovery capture can copy them. The evidence receipt replaces paths with SHA-256 tokens and never emits file contents.

Symlinks are visible as a risk signal but are not capture-eligible. Discovery performs no service, Git, filesystem, Docker, or TrueNAS mutation.
