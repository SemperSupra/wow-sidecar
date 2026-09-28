# Conventional Linux release materialization

This slice performs the first real conventional-Linux runtime mutation while leaving the active service untouched.

`materialize_linux_release()`:

- accepts only a local absolute Git repository path plus an exact 40-hex source revision;
- clones into a revision-specific `.partial` directory under `/usr/local/lib/wow-sidecar/releases`;
- checks out the requested revision detached and verifies the resulting checkout is clean and exact;
- removes the clone's `origin` remote so local paths or credential-bearing source URLs are not retained;
- rejects symlinks in the materialized checkout;
- computes a content-tree digest excluding `.git`, fsyncs the checkout, and atomically renames it into the final revision path;
- refuses an existing release target and cleans failed partial materializations.

This stage does **not** create a virtual environment, install dependencies, write configuration, replace the systemd unit, run `systemctl`, stop/start the historical Agent Dispatch Sidecar, or change durable request/claim/receipt state.

The resulting release is therefore additive and non-activating. Environment construction and activation remain separate qualification gates, preserving a rollback boundary before any live cutover.
