from __future__ import annotations

import argparse
import json
from pathlib import Path
import re

SHA = re.compile(r"^[0-9a-f]{40}$")
DIGEST = re.compile(r"^sha256:[0-9a-f]{64}$")
REGISTRY = re.compile(r"^ghcr\.io/sempersupra/wow-sidecar@sha256:[0-9a-f]{64}$")
GIT_VERSION = "1:2.47.3-0+deb13u1"


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
    require(value["schema"] == "wow-sidecar.container-publication.v2", "unsupported container publication schema")
    require(value["phase"] in {"candidate-unpublished", "published"}, "invalid publication phase")
    for key in ("source_revision", "published_equivalent_revision", "source_tree"):
        require(isinstance(value[key], str) and SHA.fullmatch(value[key]) is not None, f"{key} must be a full Git SHA")
    base = value["base_image"]
    require(
        isinstance(base, str)
        and "@sha256:" in base
        and DIGEST.fullmatch("sha256:" + base.rsplit("@sha256:", 1)[1]) is not None,
        "base image is not digest pinned",
    )

    wheelhouse = value["wheelhouse"]
    require(set(wheelhouse) == {"release_tag", "asset_name", "bundle_sha256", "manifest_sha256"}, "wheelhouse fields are invalid")
    require(DIGEST.fullmatch(wheelhouse["bundle_sha256"]) is not None, "wheelhouse bundle digest invalid")
    require(DIGEST.fullmatch(wheelhouse["manifest_sha256"]) is not None, "wheelhouse manifest digest invalid")
    require(value["source_revision"] in wheelhouse["release_tag"], "wheelhouse tag must bind source revision")
    require(value["source_revision"] in wheelhouse["asset_name"], "wheelhouse asset must bind source revision")

    registry = value["registry"]
    require(set(registry) == {"repository", "tag", "reference"}, "registry fields are invalid")
    require(registry["repository"] == "ghcr.io/sempersupra/wow-sidecar", "unexpected registry repository")
    require(registry["tag"] == "source-" + value["source_revision"], "registry tag must bind source revision")
    if value["phase"] == "candidate-unpublished":
        require(registry["reference"] is None, "unpublished candidate cannot claim registry digest")
    else:
        require(
            isinstance(registry["reference"], str)
            and REGISTRY.fullmatch(registry["reference"]) is not None,
            "published image must use exact GHCR digest",
        )

    runtime = value["runtime"]
    require(set(runtime) == {
        "user", "uid", "gid", "revision_file",
        "root_filesystem_compatible_read_only",
        "git_package_version", "github_cli_required",
    }, "runtime fields are invalid")
    require(runtime["user"] == "10001:10001", "runtime user drift")
    require(runtime["uid"] == 10001 and runtime["gid"] == 10001, "runtime UID/GID drift")
    require(runtime["revision_file"] == "/usr/share/wow-sidecar/source-revision", "revision file drift")
    require(runtime["root_filesystem_compatible_read_only"] is True, "root filesystem compatibility drift")
    require(runtime["git_package_version"] == GIT_VERSION, "git package version drift")
    require(runtime["github_cli_required"] is False, "GitHub CLI must not be required by generic WOW")

    print(json.dumps({
        "phase": value["phase"],
        "source_revision": value["source_revision"],
        "source_tree": value["source_tree"],
        "runtime_uid": runtime["uid"],
        "runtime_gid": runtime["gid"],
        "git_package_version": runtime["git_package_version"],
        "github_cli_required": runtime["github_cli_required"],
        "registry_reference": registry["reference"],
        "result": "PASS",
    }, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
