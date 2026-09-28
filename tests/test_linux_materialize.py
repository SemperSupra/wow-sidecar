from __future__ import annotations

import json
from pathlib import Path
import subprocess
import tempfile
import unittest

from wow_sidecar.errors import SidecarError
from wow_sidecar.integrations.linux_materialize import materialize_linux_release


def _git(*args: str) -> str:
    return subprocess.run(
        ["git", *args],
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    ).stdout.strip()


def _source_repo(root: Path) -> tuple[Path, str]:
    source = root / "source"
    source.mkdir()
    _git("init", "-q", str(source))
    _git("-C", str(source), "config", "user.email", "wow-sidecar@example.invalid")
    _git("-C", str(source), "config", "user.name", "WOW Sidecar Test")
    (source / "artifact.txt").write_text("committed\n", encoding="utf-8")
    _git("-C", str(source), "add", "artifact.txt")
    _git("-C", str(source), "commit", "-qm", "fixture")
    revision = _git("-C", str(source), "rev-parse", "HEAD")
    return source, revision


class LinuxReleaseMaterializationTests(unittest.TestCase):
    def test_materializes_exact_detached_clean_revision_without_activation(self):
        with tempfile.TemporaryDirectory() as raw_tmp:
            root = Path(raw_tmp).resolve()
            source, revision = _source_repo(root)
            (source / "dirty-local-only.txt").write_text("not committed\n", encoding="utf-8")
            target_root = root / "target"
            target_root.mkdir()

            result = materialize_linux_release(
                root=target_root,
                source_repository=source,
                revision=revision,
            )

            release = result.release_path
            self.assertEqual(
                release,
                target_root / "usr/local/lib/wow-sidecar/releases" / revision,
            )
            self.assertEqual((release / "artifact.txt").read_text(encoding="utf-8"), "committed\n")
            self.assertFalse((release / "dirty-local-only.txt").exists())
            self.assertEqual(_git("-C", str(release), "rev-parse", "HEAD"), revision)
            self.assertEqual(_git("-C", str(release), "status", "--porcelain"), "")
            self.assertEqual(_git("-C", str(release), "remote"), "")

            receipt = result.receipt()
            self.assertTrue(receipt["mutation_performed"])
            self.assertFalse(receipt["activation_performed"])
            self.assertFalse(receipt["active_unit_changed"])
            self.assertFalse(receipt["contains_source_path"])
            self.assertNotIn(str(source), json.dumps(receipt, sort_keys=True))
            self.assertGreaterEqual(receipt["file_count"], 1)
            self.assertRegex(receipt["tree_digest"], r"^sha256:[0-9a-f]{64}$")

            self.assertFalse(
                (target_root / "etc/systemd/system/wow-sidecar-worker.service").exists()
            )

    def test_refuses_existing_revision_target(self):
        with tempfile.TemporaryDirectory() as raw_tmp:
            root = Path(raw_tmp).resolve()
            source, revision = _source_repo(root)
            target_root = root / "target"
            target = target_root / "usr/local/lib/wow-sidecar/releases" / revision
            target.mkdir(parents=True)
            marker = target / "keep"
            marker.write_text("preserve\n", encoding="utf-8")

            with self.assertRaisesRegex(SidecarError, "already exists"):
                materialize_linux_release(
                    root=target_root,
                    source_repository=source,
                    revision=revision,
                )

            self.assertEqual(marker.read_text(encoding="utf-8"), "preserve\n")

    def test_failed_revision_checkout_leaves_no_partial_or_release(self):
        with tempfile.TemporaryDirectory() as raw_tmp:
            root = Path(raw_tmp).resolve()
            source, _ = _source_repo(root)
            target_root = root / "target"
            target_root.mkdir()
            missing_revision = "f" * 40

            with self.assertRaisesRegex(SidecarError, "git materialization failed"):
                materialize_linux_release(
                    root=target_root,
                    source_repository=source,
                    revision=missing_revision,
                )

            releases = target_root / "usr/local/lib/wow-sidecar/releases"
            self.assertFalse((releases / missing_revision).exists())
            self.assertFalse((releases / (missing_revision + ".partial")).exists())

    def test_source_repository_must_be_local_absolute_directory(self):
        with tempfile.TemporaryDirectory() as raw_tmp:
            root = Path(raw_tmp).resolve()
            target_root = root / "target"
            target_root.mkdir()
            with self.assertRaisesRegex(SidecarError, "absolute"):
                materialize_linux_release(
                    root=target_root,
                    source_repository=Path("relative"),
                    revision="a" * 40,
                )


if __name__ == "__main__":
    unittest.main()
