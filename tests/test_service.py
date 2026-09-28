from __future__ import annotations

import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from wow_sidecar import service


REVISION = "a" * 40
AUTH_REV = "sha256:" + "b" * 64


def profile_document(name: str = "synthetic-proof"):
    return {
        "schema": "wow-sidecar.operator-profile.v1",
        "profile": name,
        "authority": {
            "record": "github-issue-comment:ExampleOrg/authority#7:11",
            "revision": AUTH_REV,
            "state": "open",
        },
        "operator": {
            "kind": "pinned-repository",
            "repository": "ExampleOrg/operator-source",
            "revision": "c" * 40,
            "operator_path": "tools/operator.sh",
            "require_main_revision": True,
            "environment": {},
        },
    }


class FakeClient:
    def installation_token_for_repository(self, repository: str) -> str:
        return "token-for-" + repository.replace("/", "-")


class ServiceEntrypointTests(unittest.TestCase):
    def test_build_cycle_loads_profiles_and_clean_revision(self):
        with tempfile.TemporaryDirectory() as raw_tmp:
            root = Path(raw_tmp)
            profile = root / "profile.json"
            profile.write_text(json.dumps(profile_document()), encoding="utf-8")
            client = FakeClient()
            with (
                patch.object(service, "resolve_operator_revision", return_value=REVISION) as resolver,
                patch.object(service, "GitHubIssueAuthorityReader", return_value="reader"),
                patch.object(service, "process_cycle", return_value=[{"state": "receipted"}]) as process,
            ):
                cycle, revision = service.build_cycle(
                    control_repository="ExampleOrg/control",
                    profile_paths=[profile],
                    repo_root=root,
                    client=client,
                )
                result = cycle()

            resolver.assert_called_once_with(repo_root=root, revision_file=None)
            self.assertEqual(revision, REVISION)
            self.assertEqual(result, [{"state": "receipted"}])
            kwargs = process.call_args.kwargs
            self.assertIs(kwargs["control_client"], client)
            self.assertEqual(kwargs["authority_reader"], "reader")
            self.assertEqual(kwargs["control_repository"], "ExampleOrg/control")
            self.assertEqual(set(kwargs["registry"]), {"synthetic-proof"})
            self.assertEqual(set(kwargs["authority_bindings"]), {"synthetic-proof"})
            self.assertEqual(kwargs["local_operator_revision"], REVISION)

    def test_revision_file_identity_drives_same_worker_cycle(self):
        with tempfile.TemporaryDirectory() as raw_tmp:
            root = Path(raw_tmp)
            profile = root / "profile.json"
            profile.write_text(json.dumps(profile_document()), encoding="utf-8")
            revision_file = root / "source-revision"
            revision_file.write_text(REVISION + "\n", encoding="utf-8")
            revision_file.chmod(0o444)
            client = FakeClient()
            with (
                patch.object(service, "GitHubIssueAuthorityReader", return_value="reader"),
                patch.object(service, "process_cycle", return_value=[]) as process,
            ):
                cycle, revision = service.build_cycle(
                    control_repository="ExampleOrg/control",
                    profile_paths=[profile],
                    revision_file=revision_file,
                    client=client,
                )
                cycle()

            self.assertEqual(revision, REVISION)
            self.assertEqual(process.call_args.kwargs["local_operator_revision"], REVISION)

    def test_build_cycle_requires_profile(self):
        with self.assertRaisesRegex(ValueError, "at least one"):
            service.build_cycle(
                control_repository="ExampleOrg/control",
                profile_paths=[],
                repo_root=Path("/tmp"),
                client=FakeClient(),
            )

    def test_once_emits_revision_and_results(self):
        with patch.object(service, "build_cycle", return_value=(lambda: [{"state": "receipted"}], REVISION)):
            with patch("builtins.print") as emit:
                rc = service.main([
                    "--control-repository", "ExampleOrg/control",
                    "--profile", "/tmp/profile.json",
                    "--repo-root", "/tmp/source",
                    "--once",
                ])
        self.assertEqual(rc, 0)
        value = json.loads(emit.call_args.args[0])
        self.assertEqual(value["operator_revision"], REVISION)
        self.assertEqual(value["results"], [{"state": "receipted"}])

    def test_cli_revision_file_reaches_build_cycle(self):
        with patch.object(service, "build_cycle", return_value=(lambda: [], REVISION)) as build:
            rc = service.main([
                "--control-repository", "ExampleOrg/control",
                "--profile", "/tmp/profile.json",
                "--revision-file", "/usr/share/wow-sidecar/source-revision",
                "--once",
            ])
        self.assertEqual(rc, 0)
        self.assertEqual(
            build.call_args.kwargs["revision_file"],
            Path("/usr/share/wow-sidecar/source-revision"),
        )
        self.assertIsNone(build.call_args.kwargs["repo_root"])

    def test_serve_delegates_bounded_polling(self):
        cycle = lambda: []
        with (
            patch.object(service, "build_cycle", return_value=(cycle, REVISION)),
            patch.object(service, "run_service", return_value=0) as run,
        ):
            rc = service.main([
                "--control-repository", "ExampleOrg/control",
                "--profile", "/tmp/profile.json",
                "--repo-root", "/tmp/source",
                "--serve",
                "--poll-seconds", "20",
            ])
        self.assertEqual(rc, 0)
        run.assert_called_once_with(cycle, poll_seconds=20)

    def test_failure_is_sanitized(self):
        with (
            patch.object(service, "build_cycle", side_effect=RuntimeError("synthetic secret")),
            patch("builtins.print") as emit,
        ):
            rc = service.main([
                "--control-repository", "ExampleOrg/control",
                "--profile", "/tmp/profile.json",
                "--repo-root", "/tmp/source",
                "--once",
            ])
        self.assertEqual(rc, 1)
        rendered = emit.call_args.args[0]
        self.assertIn("wow-sidecar-service-failed-closed", rendered)
        self.assertNotIn("synthetic secret", rendered)
        self.assertIn("retry_authorized", rendered)


if __name__ == "__main__":
    unittest.main()
