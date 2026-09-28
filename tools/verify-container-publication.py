from __future__ import annotations

import argparse
import json
from pathlib import Path
import re

SHA = re.compile(r"^[0-9a-f]{40}$")
DIGEST = re.compile(r"^sha256:[0-9a-f]{64}$")
REGISTRY = re.compile(r"^ghcr\.io/sempersupra/wow-sidecar@sha256:[0-9a-f]{64}$")


def require(condition: bool, message: str) -> None:
    if not condition:
        raise SystemExit(message)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("descriptor", type=Path)
    args = parser.parse_args()
    value = json.loads(args.descriptor.read_text(encoding="utf-8"))

    require(set(value) == {
        "schema", "phase", "source_revision", "published_equivalent_revision",
        "source_tree", "base_image", "wheelhouse", "registry", "runtime",
    }, "container publication fields are invalid")
    require(value["schema"] == "wow-sidecar.container-publication.v1", "unsupported container publication schema")
    require(value["phase"] in {"candidate-unpublished", "published"}, "invalid publication phase")
    for key in ("source_revision", "published_equivalent_revision", "source_tree"):
        require(isinstance(value[key], str) and SHA.fullmatch(value[key]) is not None, f"{key} must be a full Git SHA")
    base = value["base_image"]
    require(isinstance(base, str) and "@sha256:" in base and DIGEST.fullmatch("sha256:" + base.rsplit("@sha256:", 1)[1]) is not None, "base image is not digest pinned")

    wheelhouse = value["wheelhouse"]
    require(set(wheelhouse) == {"release_tag", "asset_name", "bundle_sha256", "manifest_sha256"}, "wheelhouse fields are invalid")
    require(DIGEST.fullmatch(wheelhouse["bundle_sha256"]) is not None, "wheelhouse bundle digest invalid")
    require(DIGEST.fullmatch(wheelhouse["manifest_sha256"]) is not None, "wheelhouse manifest digest invalid")

    registry = value["registry"]
    require(set(registry) == {"repository", "tag", "reference"}, "registry fields are invalid")
    require(registry["repository"] == "ghcr.io/sempersupra/wow-sidecar", "unexpected registry repository")
    require(registry["tag"] == "source-" + value["source_revision"], "registry tag must bind source revision")
    if value["phase"] == "candidate-unpublished":
        require(registry["reference"] is None, "unpublished candidate cannot claim registry digest")
    else:
        require(isinstance(registry["reference"], str) and REGISTRY.fullmatch(registry["reference"]) is not None, "published image must use exact GHCR digest")

    runtime = value["runtime"]
    require(runtime == {
        "user": "wow-sidecar:wow-sidecar",
        "revision_file": "/usr/share/wow-sidecar/source-revision",
        "root_filesystem_compatible_read_only": True,
    }, "runtime contract drift")
    print(json.dumps({
        "phase": value["phase"],
        "source_revision": value["source_revision"],
        "source_tree": value["source_tree"],
        "registry_reference": registry["reference"],
        "result": "PASS",
    }, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
