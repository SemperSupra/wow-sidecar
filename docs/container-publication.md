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

## Published candidate

The source-bound tag was published and independently pulled/verified by push run `36408717442`.

Immutable registry reference:

`ghcr.io/sempersupra/wow-sidecar@sha256:6240b57e69134ad69379fbfbd7ab7aa1768a49c4c598a7d29f2aa0e65d200df3`

The publication job verified the pulled digest still reports:

- source revision `246a7f635c70cf93803b1932ef4129040f6ff458`;
- source tree `5d43ddcfb8d36748448d8c3bebef0c4abfd12f0f`;
- wheelhouse bundle `sha256:ae25ea04cd2ebd49bdf0d3954439811630045c131e4774a27bfc962895239e4b`;
- runtime user `wow-sidecar:wow-sidecar`.

The digest is now the only image identity downstream TrueNAS packaging should consume.
