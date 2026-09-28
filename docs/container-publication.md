# GHCR image publication from admitted wheelhouse

The TrueNAS/App image path does not run a fresh Python dependency resolver.

The v2 publication descriptor binds:

- exact qualified WOW source revision and complete source tree;
- exact Python base-image digest;
- exact persistent wheelhouse release asset and bundle digest;
- exact Debian Git package version required by the pinned-repository operator;
- deterministic runtime UID/GID `10001:10001`;
- exact GHCR repository and source-bound tag;
- an explicit invariant that GitHub CLI is not required.

`Dockerfile.wheelhouse` installs Python dependencies only from the admitted wheelhouse with `pip --no-index --no-deps`. It installs only the remaining generic OS-level operator tool, `git=1:2.47.3-0+deb13u1`, from the digest-pinned Debian-based Python image's configured repository and records that version in the image.

The runtime is then switched to numeric `10001:10001`. The state directory is created for that identity. Qualification requires bash and Git, rejects an installed `gh`, verifies the exact Git package version, and checks the source/tree/wheelhouse/runtime provenance labels.

Pull requests are qualification-only. Only a push to `main` receives `packages: write`. The source-bound tag is create-once: an existing tag is verified rather than overwritten.

The earlier image digest `sha256:6240b57e69134ad69379fbfbd7ab7aa1768a49c4c598a7d29f2aa0e65d200df3` remains falsification/provenance evidence only. It lacks Git and is not downstream-deployment admissible.

The root-required GARM integration is not part of this image and does not alter the generic WOW privilege envelope.
