from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
import zipfile

from wow_sidecar.errors import SidecarError
from wow_sidecar.integrations.linux_environment import (
    ENVIRONMENT_SCHEMA,
    WHEELHOUSE_SCHEMA,
    _pip_install_command,
    build_linux_environment,
    load_wheelhouse_manifest,
)


REVISION = "a" * 40


def _fixture_wheel(directory: Path) -> Path:
    wheel = directory / "wow_sidecar-0.0.0-py3-none-any.whl"
    dist = "wow_sidecar-0.0.0.dist-info"
    with zipfile.ZipFile(wheel, "w", compression=zipfile.ZIP_DEFLATED) as archive:
        archive.writestr("wow_sidecar/__init__.py", "")
        archive.writestr(
            "wow_sidecar/service.py",
            "def main():\n    print('fixture worker')\n    return 0\n",
        )
        archive.writestr(
            f"{dist}/METADATA",
            "Metadata-Version: 2.1\nName: wow-sidecar\nVersion: 0.0.0\n",
        )
        archive.writestr(
            f"{dist}/WHEEL",
            "Wheel-Version: 1.0\n"
            "Generator: wow-sidecar-test\n"
            "Root-Is-Purelib: true\n"
            "Tag: py3-none-any\n",
        )
        archive.writestr(
            f"{dist}/entry_points.txt",
            "[console_scripts]\nwow-sidecar-worker = wow_sidecar.service:main\n",
        )
        archive.writestr(f"{dist}/RECORD", "")
    return wheel


def _wheelhouse(root: Path, *, revision: str = REVISION) -> Path:
    directory = root / "wheelhouse"
    directory.mkdir()
    wheel = _fixture_wheel(directory)
    digest = "sha256:" + hashlib.sha256(wheel.read_bytes()).hexdigest()
    document = {
        "schema": WHEELHOUSE_SCHEMA,
        "source_revision": revision,
        "wheels": [
            {
                "filename": wheel.name,
                "sha256": digest,
            }
        ],
    }
    (directory / "manifest.json").write_text(
        json.dumps(document, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    return directory


class LinuxEnvironmentTests(unittest.TestCase):
    def test_builds_hash_bound_offline_environment_without_activation(self):
        with tempfile.TemporaryDirectory() as raw_tmp:
            root = Path(raw_tmp).resolve()
            wheelhouse = _wheelhouse(root)
            target_root = root / "target"
            target_root.mkdir()

            result = build_linux_environment(
                root=target_root,
                wheelhouse=wheelhouse,
                revision=REVISION,
            )

            environment = result.environment_path
            worker = environment / "bin/wow-sidecar-worker"
            self.assertTrue(worker.is_file())
            clean_env = dict(os.environ)
            clean_env.pop("PYTHONPATH", None)
            clean_env.pop("PYTHONHOME", None)
            completed = subprocess.run(
                [str(worker)],
                check=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
                env=clean_env,
            )
            self.assertIn("fixture worker", completed.stdout)

            stamp = json.loads(
                (environment / "wow-sidecar-environment.json").read_text(encoding="utf-8")
            )
            self.assertEqual(stamp["schema"], ENVIRONMENT_SCHEMA)
            self.assertEqual(stamp["revision"], REVISION)
            self.assertEqual(stamp["manifest_digest"], result.manifest_digest)

            receipt = result.receipt()
            self.assertTrue(receipt["mutation_performed"])
            self.assertFalse(receipt["activation_performed"])
            self.assertFalse(receipt["active_unit_changed"])
            self.assertFalse(receipt["network_dependency"])
            self.assertFalse(receipt["contains_wheel_filenames"])
            self.assertEqual(receipt["wheel_count"], 1)
            self.assertFalse(
                (target_root / "etc/systemd/system/wow-sidecar-worker.service").exists()
            )

    def test_manifest_digest_mismatch_fails_before_environment_creation(self):
        with tempfile.TemporaryDirectory() as raw_tmp:
            root = Path(raw_tmp).resolve()
            wheelhouse = _wheelhouse(root)
            wheel = next(wheelhouse.glob("*.whl"))
            wheel.write_bytes(wheel.read_bytes() + b"tamper")
            target_root = root / "target"
            target_root.mkdir()

            with self.assertRaisesRegex(SidecarError, "digest mismatch"):
                build_linux_environment(
                    root=target_root,
                    wheelhouse=wheelhouse,
                    revision=REVISION,
                )

            self.assertFalse(
                (target_root / "usr/local/lib/wow-sidecar/venvs" / REVISION).exists()
            )

    def test_manifest_rejects_unlisted_wheel_and_revision_mismatch(self):
        with tempfile.TemporaryDirectory() as raw_tmp:
            root = Path(raw_tmp).resolve()
            wheelhouse = _wheelhouse(root)
            (wheelhouse / "extra-1.0-py3-none-any.whl").write_bytes(b"not-listed")
            with self.assertRaisesRegex(SidecarError, "wheel set"):
                load_wheelhouse_manifest(wheelhouse, expected_revision=REVISION)

        with tempfile.TemporaryDirectory() as raw_tmp:
            root = Path(raw_tmp).resolve()
            wheelhouse = _wheelhouse(root, revision="b" * 40)
            with self.assertRaisesRegex(SidecarError, "source revision mismatch"):
                load_wheelhouse_manifest(wheelhouse, expected_revision=REVISION)

    def test_existing_environment_is_preserved(self):
        with tempfile.TemporaryDirectory() as raw_tmp:
            root = Path(raw_tmp).resolve()
            wheelhouse = _wheelhouse(root)
            target_root = root / "target"
            target = target_root / "usr/local/lib/wow-sidecar/venvs" / REVISION
            target.mkdir(parents=True)
            marker = target / "keep"
            marker.write_text("preserve\n", encoding="utf-8")

            with self.assertRaisesRegex(SidecarError, "already exists"):
                build_linux_environment(
                    root=target_root,
                    wheelhouse=wheelhouse,
                    revision=REVISION,
                )

            self.assertEqual(marker.read_text(encoding="utf-8"), "preserve\n")

    def test_pip_command_is_explicitly_offline_and_dependency_resolver_free(self):
        command = _pip_install_command(
            Path("/tmp/venv/bin/python"),
            (Path("/tmp/wheels/wow_sidecar.whl"),),
        )
        self.assertIn("--no-index", command)
        self.assertIn("--no-deps", command)
        self.assertNotIn("http://", " ".join(command))
        self.assertNotIn("https://", " ".join(command))


if __name__ == "__main__":
    unittest.main()
