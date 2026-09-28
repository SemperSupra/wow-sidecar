from __future__ import annotations

from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

from wow_sidecar.errors import SidecarError
from wow_sidecar.host_control import HostOperatorRequest, REQUEST_SCHEMA
from wow_sidecar.integrations.pinned_repository import (
    PinnedRepositoryOperator,
    PinnedRepositoryOperatorSpec,
    github_git_env,
)


REVISION = "a" * 40
AUTH_REV = "sha256:" + "b" * 64
PROFILE = "fixed-operator"
REPOSITORY = "ExampleOrg/operator-repo"
TOKEN = "synthetic-installation-token"


def request(inputs=None):
    return HostOperatorRequest.parse(
        {
            "schema": REQUEST_SCHEMA,
            "request_id": "fixed-operator-001",
            "operator_profile": PROFILE,
            "operator_revision": REVISION,
            "authority": {
                "record": "github-issue-comment:ExampleOrg/authority#7:11",
                "revision": AUTH_REV,
                "state": "open",
            },
            "inputs": inputs if inputs is not None else {},
            "constraints": {
                "no_retry": True,
                "promotion_performed": False,
                "release_performed": False,
            },
        }
    )


def spec(**overrides):
    value = {
        "profile": PROFILE,
        "repository": REPOSITORY,
        "revision": REVISION,
        "operator_path": "tools/operator.sh",
        "require_main_revision": True,
        "environment": {"FIXED_MODE": "1"},
    }
    value.update(overrides)
    return PinnedRepositoryOperatorSpec(**value)


def token_provider(repository: str) -> str:
    if repository != REPOSITORY:
        raise AssertionError(repository)
    return TOKEN


class PinnedRepositoryOperatorTests(unittest.TestCase):
    def test_spec_rejects_path_traversal_or_non_sha_revision(self):
        with self.assertRaises(SidecarError):
            spec(operator_path="../operator.sh")
        with self.assertRaises(SidecarError):
            spec(revision="main")

    def test_git_environment_uses_ephemeral_askpass_and_rejects_ambient_git_auth(self):
        env = github_git_env(
            TOKEN,
            Path("/tmp/wow-askpass"),
            {
                "PATH": "/usr/bin",
                "GIT_SSH_COMMAND": "ssh -i /secret",
                "GIT_ASKPASS": "/old/helper",
                "SSH_AUTH_SOCK": "/run/agent",
            },
        )
        self.assertEqual(env["PATH"], "/usr/bin")
        self.assertEqual(env["GIT_CONFIG_NOSYSTEM"], "1")
        self.assertEqual(env["GIT_CONFIG_GLOBAL"], "/dev/null")
        self.assertEqual(env["GIT_CONFIG_KEY_0"], "credential.helper")
        self.assertEqual(env["GIT_CONFIG_VALUE_0"], "")
        self.assertEqual(env["GIT_TERMINAL_PROMPT"], "0")
        self.assertEqual(env["GIT_ASKPASS_REQUIRE"], "force")
        self.assertEqual(env["GIT_ASKPASS"], "/tmp/wow-askpass")
        self.assertEqual(env["WOW_SIDECAR_GIT_TOKEN"], TOKEN)
        self.assertNotIn("GIT_SSH_COMMAND", env)
        self.assertNotIn("SSH_AUTH_SOCK", env)

    def test_request_cannot_supply_command_or_other_inputs(self):
        operator = PinnedRepositoryOperator(spec(), token_provider=token_provider)
        with self.assertRaisesRegex(SidecarError, "accepts no request inputs"):
            operator(request({"command": "whoami"}))

    def test_success_uses_only_exact_repo_token_revision_and_path_without_gh(self):
        calls = []
        def provider(repository):
            calls.append(repository)
            return TOKEN

        operator = PinnedRepositoryOperator(spec(), token_provider=provider)
        runs = [
            subprocess.CompletedProcess(["git", "ls-remote"], 0, REVISION + "\trefs/heads/main\n", ""),
            subprocess.CompletedProcess(["git", "clone"], 0, "", ""),
            subprocess.CompletedProcess(["git", "fetch"], 0, "", ""),
            subprocess.CompletedProcess(["git", "checkout"], 0, "", ""),
            subprocess.CompletedProcess(["git", "rev-parse"], 0, REVISION + "\n", ""),
            subprocess.CompletedProcess(["git", "status"], 0, "", ""),
            subprocess.CompletedProcess(["bash"], 0, "", ""),
        ]
        with (
            patch.object(operator, "_run", side_effect=runs) as run,
            patch("pathlib.Path.is_file", return_value=True),
            patch("pathlib.Path.is_symlink", return_value=False),
        ):
            result = operator(request())

        self.assertEqual(result["result"], "ELIGIBLE")
        self.assertEqual(calls, [REPOSITORY])
        self.assertEqual(run.call_count, 7)
        self.assertTrue(all(call.args[0][0] != "gh" for call in run.call_args_list))

        clone_argv = run.call_args_list[1].args[0]
        self.assertEqual(clone_argv[0:4], ["git", "clone", "--quiet", "--filter=blob:none"])
        self.assertIn(f"https://github.com/{REPOSITORY}.git", clone_argv)

        first_git_env = run.call_args_list[0].kwargs["env"]
        self.assertEqual(first_git_env["WOW_SIDECAR_GIT_TOKEN"], TOKEN)
        self.assertEqual(first_git_env["GIT_ASKPASS_REQUIRE"], "force")

        bash_call = run.call_args_list[6]
        self.assertEqual(bash_call.args[0][0], "bash")
        self.assertTrue(bash_call.args[0][1].endswith("/tools/operator.sh"))
        self.assertEqual(bash_call.kwargs["env"]["FIXED_MODE"], "1")
        self.assertEqual(bash_call.kwargs["env"]["WOW_SIDECAR_GIT_TOKEN"], TOKEN)

    def test_main_revision_drift_fails_before_clone(self):
        operator = PinnedRepositoryOperator(spec(), token_provider=token_provider)
        runs = [
            subprocess.CompletedProcess(["git", "ls-remote"], 0, "c" * 40 + "\trefs/heads/main\n", ""),
        ]
        with patch.object(operator, "_run", side_effect=runs) as run:
            with self.assertRaisesRegex(SidecarError, "main no longer matches"):
                operator(request())
        self.assertEqual(run.call_count, 1)

    def test_token_provider_failure_is_sanitized_before_git(self):
        def broken(_repository):
            raise RuntimeError("secret provider detail")
        operator = PinnedRepositoryOperator(spec(), token_provider=broken)
        with patch.object(operator, "_run") as run:
            with self.assertRaisesRegex(SidecarError, "^repository installation token is unavailable$"):
                operator(request())
        run.assert_not_called()

    def test_nonzero_operator_is_unknown_without_retry_semantics(self):
        operator = PinnedRepositoryOperator(spec(), token_provider=token_provider)
        runs = [
            subprocess.CompletedProcess(["git", "ls-remote"], 0, REVISION + "\trefs/heads/main\n", ""),
            subprocess.CompletedProcess(["git", "clone"], 0, "", ""),
            subprocess.CompletedProcess(["git", "fetch"], 0, "", ""),
            subprocess.CompletedProcess(["git", "checkout"], 0, "", ""),
            subprocess.CompletedProcess(["git", "rev-parse"], 0, REVISION + "\n", ""),
            subprocess.CompletedProcess(["git", "status"], 0, "", ""),
            subprocess.CompletedProcess(["bash"], 78, "", ""),
        ]
        with (
            patch.object(operator, "_run", side_effect=runs),
            patch("pathlib.Path.is_file", return_value=True),
            patch("pathlib.Path.is_symlink", return_value=False),
        ):
            result = operator(request())
        self.assertEqual(result["result"], "UNKNOWN")
        self.assertEqual(result["operator_exit_code"], 78)
        self.assertTrue(result["preserve_evidence"])


if __name__ == "__main__":
    unittest.main()
