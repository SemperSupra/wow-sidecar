from __future__ import annotations

from pathlib import Path
import unittest

from wow_sidecar.container_contract import (
    container_build_inputs,
    inspect_dockerfile,
    validate_base_image_reference,
)
from wow_sidecar.errors import SidecarError


ROOT = Path(__file__).resolve().parents[1]
REVISION = "a" * 40
BASE = "docker.io/library/python:3.12-slim@sha256:" + "b" * 64


class ContainerContractTests(unittest.TestCase):
    def test_base_image_requires_exact_digest(self):
        self.assertEqual(validate_base_image_reference(BASE), BASE)
        for bad in (
            "python:3.12-slim",
            "docker.io/library/python:latest",
            "docker.io/library/python@sha256:1234",
            "DOCKER.IO/library/python@sha256:" + "b" * 64,
        ):
            with self.subTest(bad=bad):
                with self.assertRaises(SidecarError):
                    validate_base_image_reference(bad)

    def test_build_inputs_bind_source_and_base_identity(self):
        value = container_build_inputs(source_revision=REVISION, base_image=BASE)
        self.assertEqual(value["source_revision"], REVISION)
        self.assertEqual(value["base_image"], BASE)
        self.assertEqual(value["revision_file"], "/usr/share/wow-sidecar/source-revision")
        self.assertEqual(value["runtime_user"], "wow-sidecar")
        self.assertEqual(value["entrypoint"], "wow-sidecar-worker")

    def test_dockerfile_has_bounded_generic_runtime_contract(self):
        value = inspect_dockerfile(ROOT / "Dockerfile")
        self.assertTrue(value["base_image_requires_digest"])
        self.assertFalse(value["contains_integration_policy"])


if __name__ == "__main__":
    unittest.main()
