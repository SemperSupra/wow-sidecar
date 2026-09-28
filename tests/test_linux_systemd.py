from __future__ import annotations

import inspect
from pathlib import Path
import tempfile
import unittest

from wow_sidecar.errors import SidecarError
from wow_sidecar.integrations import linux_systemd
from wow_sidecar.integrations.linux_systemd import (
    LinuxServiceSpec,
    observe_linux_layout,
    render_systemd_unit,
)


REVISION = "a" * 40


class LinuxSystemdTests(unittest.TestCase):
    def spec(self):
        return LinuxServiceSpec(
            revision=REVISION,
            control_repository="ExampleOrg/control",
            profile_paths=(
                "/etc/wow-sidecar/profiles/alpha.json",
                "/etc/wow-sidecar/profiles/beta.json",
            ),
        )

    def test_unit_uses_fhs_layout_exact_revision_and_hardening(self):
        unit = render_systemd_unit(self.spec())
        self.assertIn(f"WorkingDirectory=/usr/local/lib/wow-sidecar/releases/{REVISION}", unit)
        self.assertIn(f"/usr/local/lib/wow-sidecar/venvs/{REVISION}/bin/wow-sidecar-worker", unit)
        self.assertIn("--control-repository ExampleOrg/control", unit)
        self.assertIn("--profile /etc/wow-sidecar/profiles/alpha.json", unit)
        self.assertIn("--profile /etc/wow-sidecar/profiles/beta.json", unit)
        self.assertIn(f"--repo-root /usr/local/lib/wow-sidecar/releases/{REVISION}", unit)
        for invariant in (
            "User=wow-sidecar",
            "Group=wow-sidecar",
            "NoNewPrivileges=true",
            "PrivateTmp=true",
            "ProtectSystem=strict",
            "ProtectHome=true",
            "ReadOnlyPaths=/etc/wow-sidecar",
            "ReadWritePaths=/var/lib/wow-sidecar",
            "RestrictSUIDSGID=true",
            "LockPersonality=true",
        ):
            with self.subTest(invariant=invariant):
                self.assertIn(invariant, unit)
        self.assertNotIn("/opt/wow-sidecar", unit)
        self.assertNotIn("User=root", unit)
        self.assertNotIn("Group=root", unit)
        self.assertNotIn("Environment=HOME=/root", unit)

    def test_spec_rejects_shell_like_or_out_of_boundary_inputs(self):
        bad_repositories = ("owner/repo;id", "owner/repo extra", "owner/repo/name", "/repo")
        for repo in bad_repositories:
            with self.subTest(repo=repo):
                with self.assertRaises(SidecarError):
                    LinuxServiceSpec(
                        revision=REVISION,
                        control_repository=repo,
                        profile_paths=("/etc/wow-sidecar/profiles/alpha.json",),
                    )
        bad_profiles = (
            "/tmp/profile.json",
            "/etc/wow-sidecar/profiles/../secret.json",
            "/etc/wow-sidecar/profiles/a b.json",
            "/etc/wow-sidecar/profiles/a.json;id",
        )
        for path in bad_profiles:
            with self.subTest(path=path):
                with self.assertRaises(SidecarError):
                    LinuxServiceSpec(
                        revision=REVISION,
                        control_repository="ExampleOrg/control",
                        profile_paths=(path,),
                    )

    def test_observation_reports_expected_layout_without_mutation(self):
        with tempfile.TemporaryDirectory() as raw_tmp:
            root = Path(raw_tmp).resolve()
            release = root / "usr/local/lib/wow-sidecar/releases" / REVISION
            worker = root / "usr/local/lib/wow-sidecar/venvs" / REVISION / "bin"
            unit = root / "etc/systemd/system"
            config = root / "etc/wow-sidecar"
            state = root / "var/lib/wow-sidecar"
            for directory in (release, worker, unit, config, state):
                directory.mkdir(parents=True, exist_ok=True)
            (worker / "wow-sidecar-worker").write_text("#!/bin/sh\n", encoding="utf-8")
            (unit / "wow-sidecar-worker.service").write_text("unit\n", encoding="utf-8")

            before = sorted(str(p.relative_to(root)) for p in root.rglob("*"))
            observed = observe_linux_layout(root, REVISION)
            after = sorted(str(p.relative_to(root)) for p in root.rglob("*"))
            self.assertEqual(before, after)
            self.assertTrue(observed.release_present)
            self.assertTrue(observed.venv_worker_present)
            self.assertTrue(observed.unit_present)
            self.assertTrue(observed.config_root_present)
            self.assertTrue(observed.state_root_present)
            self.assertFalse(observed.receipt()["mutation_performed"])

    def test_observer_source_has_no_activation_or_mutation(self):
        source = inspect.getsource(linux_systemd)
        for forbidden in (
            "systemctl",
            ".write_text(",
            ".write_bytes(",
            ".unlink(",
            ".mkdir(",
            "git clone",
            "pip install",
        ):
            with self.subTest(forbidden=forbidden):
                self.assertNotIn(forbidden, source)


if __name__ == "__main__":
    unittest.main()
