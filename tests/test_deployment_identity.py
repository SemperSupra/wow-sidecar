from __future__ import annotations

import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from wow_sidecar.deployment_identity import resolve_operator_revision, revision_from_file
from wow_sidecar.errors import SidecarError


REVISION = "a" * 40


class DeploymentIdentityTests(unittest.TestCase):
    def test_clean_checkout_identity_is_preserved(self):
        with patch("wow_sidecar.deployment_identity.local_checkout_revision", return_value=REVISION) as resolver:
            value = resolve_operator_revision(repo_root=Path("/tmp/source"))
        self.assertEqual(value, REVISION)
        resolver.assert_called_once_with(Path("/tmp/source"))

    def test_revision_file_must_be_absolute_regular_non_symlink_and_read_only(self):
        with tempfile.TemporaryDirectory() as raw_tmp:
            root = Path(raw_tmp).resolve()
            revision_file = root / "source-revision"
            revision_file.write_text(REVISION + "\n", encoding="utf-8")
            revision_file.chmod(0o444)
            self.assertEqual(revision_from_file(revision_file), REVISION)

            revision_file.chmod(0o644)
            with self.assertRaisesRegex(SidecarError, "must not be writable"):
                revision_from_file(revision_file)

            revision_file.chmod(0o444)
            if hasattr(os, "symlink"):
                link = root / "revision-link"
                link.symlink_to(revision_file)
                with self.assertRaisesRegex(SidecarError, "symlink"):
                    revision_from_file(link)

    def test_revision_file_rejects_non_sha_content(self):
        with tempfile.TemporaryDirectory() as raw_tmp:
            path = Path(raw_tmp).resolve() / "source-revision"
            path.write_text("latest\n", encoding="utf-8")
            path.chmod(0o444)
            with self.assertRaisesRegex(SidecarError, "40-hex"):
                revision_from_file(path)

    def test_exactly_one_identity_source_is_required(self):
        with self.assertRaisesRegex(SidecarError, "exactly one"):
            resolve_operator_revision()
        with self.assertRaisesRegex(SidecarError, "exactly one"):
            resolve_operator_revision(
                repo_root=Path("/tmp/source"),
                revision_file=Path("/tmp/revision"),
            )


if __name__ == "__main__":
    unittest.main()
