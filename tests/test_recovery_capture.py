from __future__ import annotations

import json
import os
from pathlib import Path
import tempfile
import unittest

from wow_sidecar.errors import SidecarError
from wow_sidecar.recovery_capture import (
    CaptureItem,
    capture_recovery_bundle,
    verify_captured_bundle,
)


class RecoveryCaptureTests(unittest.TestCase):
    def test_copy_first_capture_preserves_sources_and_emits_sanitized_receipt(self):
        with tempfile.TemporaryDirectory() as raw_tmp:
            root = Path(raw_tmp).resolve()
            source_a = root / "app.pem"
            source_b = root / "runtime.env"
            secret_a = b"synthetic-key-value"
            secret_b = b"API_TOKEN=synthetic-token-value\n"
            source_a.write_bytes(secret_a)
            source_b.write_bytes(secret_b)
            destination = root / "recovery"

            capture = capture_recovery_bundle(
                source_state="legacy-unmanaged",
                items=(
                    CaptureItem("app-key", source_a),
                    CaptureItem("runtime-config", source_b),
                ),
                destination=destination,
            )

            self.assertEqual(source_a.read_bytes(), secret_a)
            self.assertEqual(source_b.read_bytes(), secret_b)
            self.assertTrue(destination.is_dir())
            self.assertTrue(verify_captured_bundle(capture)["verified"])

            receipt = json.dumps(capture.evidence_receipt(), sort_keys=True)
            self.assertNotIn(str(source_a), receipt)
            self.assertNotIn(str(source_b), receipt)
            self.assertNotIn(secret_a.decode(), receipt)
            self.assertNotIn("synthetic-token-value", receipt)
            self.assertIn("sha256:", receipt)
            self.assertFalse(capture.evidence_receipt()["source_mutation_performed"])
            self.assertFalse(capture.evidence_receipt()["contains_source_paths"])

    def test_local_manifest_keeps_restore_mapping_out_of_evidence_receipt(self):
        with tempfile.TemporaryDirectory() as raw_tmp:
            root = Path(raw_tmp).resolve()
            source = root / "secret"
            source.write_bytes(b"value")
            capture = capture_recovery_bundle(
                source_state="managed",
                items=(CaptureItem("credential", source),),
                destination=root / "recovery",
            )
            local = capture.local_manifest()
            evidence = capture.evidence_receipt()
            self.assertEqual(local["items"][0]["source_path"], str(source))
            self.assertNotIn("source_path", evidence["items"][0])
            self.assertIn("source_path_token", evidence["items"][0])

    def test_destination_must_be_new(self):
        with tempfile.TemporaryDirectory() as raw_tmp:
            root = Path(raw_tmp).resolve()
            source = root / "secret"
            source.write_bytes(b"value")
            destination = root / "recovery"
            destination.mkdir()
            with self.assertRaisesRegex(SidecarError, "already exists"):
                capture_recovery_bundle(
                    source_state="managed",
                    items=(CaptureItem("credential", source),),
                    destination=destination,
                )
            self.assertEqual(source.read_bytes(), b"value")

    def test_source_symlink_is_rejected(self):
        if not hasattr(os, "symlink"):
            self.skipTest("symlink unsupported")
        with tempfile.TemporaryDirectory() as raw_tmp:
            root = Path(raw_tmp).resolve()
            source = root / "secret"
            source.write_bytes(b"value")
            link = root / "link"
            link.symlink_to(source)
            with self.assertRaisesRegex(SidecarError, "symlinks are not allowed"):
                capture_recovery_bundle(
                    source_state="managed",
                    items=(CaptureItem("credential", link),),
                    destination=root / "recovery",
                )
            self.assertFalse((root / "recovery").exists())

    def test_source_must_be_absolute(self):
        with self.assertRaisesRegex(SidecarError, "absolute"):
            capture_recovery_bundle(
                source_state="managed",
                items=(CaptureItem("credential", Path("relative-secret")),),
                destination=Path("/tmp/recovery-do-not-create"),
            )

    def test_tampered_bundle_fails_verification(self):
        with tempfile.TemporaryDirectory() as raw_tmp:
            root = Path(raw_tmp).resolve()
            source = root / "secret"
            source.write_bytes(b"value")
            capture = capture_recovery_bundle(
                source_state="managed",
                items=(CaptureItem("credential", source),),
                destination=root / "recovery",
            )
            bundle_file = capture.destination / capture.items[0].bundle_name
            bundle_file.write_bytes(b"tampered")
            with self.assertRaisesRegex(SidecarError, "size mismatch|digest mismatch"):
                verify_captured_bundle(capture)

    def test_bundle_files_are_private(self):
        if os.name == "nt":
            self.skipTest("POSIX mode semantics not applicable")
        with tempfile.TemporaryDirectory() as raw_tmp:
            root = Path(raw_tmp).resolve()
            source = root / "secret"
            source.write_bytes(b"value")
            capture = capture_recovery_bundle(
                source_state="managed",
                items=(CaptureItem("credential", source),),
                destination=root / "recovery",
            )
            item_mode = (capture.destination / capture.items[0].bundle_name).stat().st_mode & 0o777
            manifest_mode = (capture.destination / "manifest.json").stat().st_mode & 0o777
            self.assertEqual(item_mode, 0o600)
            self.assertEqual(manifest_mode, 0o600)


if __name__ == "__main__":
    unittest.main()
