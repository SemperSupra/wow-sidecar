from __future__ import annotations

import inspect
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

from wow_sidecar.integrations import legacy_agent_dispatch
from wow_sidecar.integrations.legacy_agent_dispatch import observe_legacy_agent_dispatch


class LegacyAgentDispatchObservationTests(unittest.TestCase):
    def _path(self, root: Path, absolute: str) -> Path:
        return root / absolute.removeprefix("/")

    def test_observation_is_read_only_secret_safe_and_discovers_known_layout(self):
        with tempfile.TemporaryDirectory() as raw_tmp:
            root = Path(raw_tmp).resolve()
            config = self._path(root, "/etc/wow-sidecar")
            units = self._path(root, "/etc/systemd/system")
            source = self._path(root, "/opt/wow-sidecar")
            state = self._path(root, "/var/lib/wow-sidecar")
            external = self._path(root, "/srv/private/github-app.pem")
            for path in (config, units, source, state, external.parent):
                path.mkdir(parents=True, exist_ok=True)

            secret = b"synthetic-secret-value"
            (config / "github-app.pem").write_bytes(secret)
            (config / "host-control.env").write_text(
                "EXTERNAL_CREDENTIAL_FILE=/srv/private/github-app.pem\n"
                "CONTAINER_ONLY_FILE=/run/secrets/not-a-host-source\n",
                encoding="utf-8",
            )
            external.write_bytes(b"external-secret")
            (units / "wow-sidecar-host-control.service").write_text("[Service]\n", encoding="utf-8")

            subprocess.run(["git", "init", "-q", str(source)], check=True)
            subprocess.run(["git", "-C", str(source), "config", "user.email", "test@example.invalid"], check=True)
            subprocess.run(["git", "-C", str(source), "config", "user.name", "Test"], check=True)
            (source / "README").write_text("baseline\n", encoding="utf-8")
            subprocess.run(["git", "-C", str(source), "add", "README"], check=True)
            subprocess.run(["git", "-C", str(source), "commit", "-qm", "baseline"], check=True)

            before = {
                p: p.read_bytes()
                for p in (config / "github-app.pem", config / "host-control.env", external)
            }
            observed = observe_legacy_agent_dispatch(root)
            after = {p: p.read_bytes() for p in before}
            self.assertEqual(before, after)

            self.assertTrue(observed.source_present)
            self.assertRegex(observed.source_git_head or "", r"^[0-9a-f]{40}$")
            self.assertEqual(observed.source_dirty_entry_count, 0)
            self.assertTrue(observed.state_present)
            self.assertEqual(len(observed.referenced_files), 1)
            self.assertEqual(observed.referenced_files[0].absolute_path, "/srv/private/github-app.pem")

            receipt = json.dumps(observed.evidence_receipt(), sort_keys=True)
            self.assertNotIn("synthetic-secret-value", receipt)
            self.assertNotIn("external-secret", receipt)
            self.assertNotIn("/etc/wow-sidecar", receipt)
            self.assertNotIn("/srv/private/github-app.pem", receipt)
            self.assertFalse(observed.evidence_receipt()["contains_secret_values"])
            self.assertFalse(observed.evidence_receipt()["contains_absolute_paths"])
            self.assertFalse(observed.evidence_receipt()["mutation_performed"])

    def test_container_runtime_file_reference_is_not_mistaken_for_host_source(self):
        with tempfile.TemporaryDirectory() as raw_tmp:
            root = Path(raw_tmp).resolve()
            config = self._path(root, "/etc/wow-sidecar")
            config.mkdir(parents=True)
            (config / "stack.env").write_text(
                "GITHUB_APP_PRIVATE_KEY_FILE=/run/secrets/github-app.pem\n",
                encoding="utf-8",
            )
            observed = observe_legacy_agent_dispatch(root)
            self.assertEqual(observed.referenced_files, ())

    def test_symlink_config_item_is_observed_but_not_capture_eligible(self):
        with tempfile.TemporaryDirectory() as raw_tmp:
            root = Path(raw_tmp).resolve()
            config = self._path(root, "/etc/wow-sidecar")
            config.mkdir(parents=True)
            target = root / "secret"
            target.write_bytes(b"value")
            (config / "link.pem").symlink_to(target)
            observed = observe_legacy_agent_dispatch(root)
            item = next(x for x in observed.config_items if x.logical_name == "config:link.pem")
            self.assertTrue(item.symlink)
            self.assertFalse(item.regular_file)
            capture_names = {x.logical_name for x in observed.capture_items(root)}
            self.assertNotIn("config:link.pem", capture_names)

    def test_dirty_count_does_not_reveal_filenames(self):
        with tempfile.TemporaryDirectory() as raw_tmp:
            root = Path(raw_tmp).resolve()
            source = self._path(root, "/opt/wow-sidecar")
            source.mkdir(parents=True)
            subprocess.run(["git", "init", "-q", str(source)], check=True)
            subprocess.run(["git", "-C", str(source), "config", "user.email", "test@example.invalid"], check=True)
            subprocess.run(["git", "-C", str(source), "config", "user.name", "Test"], check=True)
            (source / "baseline").write_text("x", encoding="utf-8")
            subprocess.run(["git", "-C", str(source), "add", "baseline"], check=True)
            subprocess.run(["git", "-C", str(source), "commit", "-qm", "baseline"], check=True)
            (source / "sensitive-filename.pem").write_text("dirty", encoding="utf-8")
            observed = observe_legacy_agent_dispatch(root)
            self.assertEqual(observed.source_dirty_entry_count, 1)
            self.assertNotIn("sensitive-filename.pem", json.dumps(observed.evidence_receipt()))

    def test_source_contains_no_mutating_filesystem_or_git_commands(self):
        source = inspect.getsource(legacy_agent_dispatch)
        for forbidden in (
            ".write_text(",
            ".write_bytes(",
            ".unlink(",
            ".mkdir(",
            "git checkout",
            "git reset",
            "git clean",
            "systemctl",
            "docker ",
        ):
            with self.subTest(forbidden=forbidden):
                self.assertNotIn(forbidden, source)


if __name__ == "__main__":
    unittest.main()
