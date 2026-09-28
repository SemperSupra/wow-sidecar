# GHCR image publication from admitted wheelhouse

The TrueNAS/App image path does not run a fresh dependency resolver.

The publication descriptor binds:

- exact qualified WOW source revision;
- exact published-equivalent source revision and complete Git tree;
- exact Python base-image digest;
- exact persistent wheelhouse release asset and bundle digest;
- exact GHCR repository and source-bound tag;
- non-root runtime identity.

`Dockerfile.wheelhouse` installs only the already-admitted wheel files with `pip --no-index --no-deps`. The wheelhouse manifest and lock are embedded read-only in the image alongside the exact source-revision file.

Pull requests qualify acquisition, lock verification, offline image construction, source identity, provenance labels, runtime user, and entrypoint without registry mutation.

Only a push to `main` receives `packages: write`. The source-bound tag is create-once: if it already exists, the workflow refuses to overwrite it and instead pulls/verifies the existing registry image. The resulting registry digest is evidence for a subsequent descriptor update and Foundry App pin.

The root-required GARM integration is not part of this image and does not alter the generic WOW privilege envelope.
