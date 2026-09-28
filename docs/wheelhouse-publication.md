# Persistent wheelhouse publication

The admitted wheelhouse must remain available after short-lived GitHub Actions evidence expires.

The publication workflow therefore packages the already-admitted wheel bytes into a deterministic ZIP archive and, only after an exact publication descriptor has been committed to `main`, publishes it as a GitHub **prerelease evidence** asset.

Safety and provenance boundaries:

- the workflow downloads the exact admitted candidate artifact; it never re-resolves dependencies;
- run/artifact identity and all wheel hashes are reverified before packaging;
- the bundle is built twice and must be byte-identical;
- pull requests never publish a release;
- a push to `main` refuses publication unless the committed descriptor matches the exact source revision, asset name and bundle SHA-256;
- publication uses a dedicated `wheelhouse-...` tag and a prerelease explicitly identified as evidence, not a WOW Sidecar product release;
- an existing release is never clobbered; it must already point at the same source revision and its downloaded asset must match the expected digest.

This produces a durable distribution source for the conventional-Linux installer while keeping product release/versioning semantics separate.
