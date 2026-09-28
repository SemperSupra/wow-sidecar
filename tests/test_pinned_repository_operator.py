from __future__ import annotations

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


class PinnedRepositoryOperatorTests(unittest.TestCase):
    def test_spec_rejects_path_traversal_or_non_sha_revision(self):
        with self.assertRaises(SidecarError):
            spec(operator_path="../operator.sh")
        with self.assertRaises(SidecarError):
            spec(revision="main")

    def test_git_environment_uses_gh_credential_helper_and_no_prompt(self):
        env = github_git_env({})
        self.assertEqual(env["GIT_CONFIG_COUNT"], "1")
        self.assertEqual(env["GIT_CONFIG_KEY_0"], "credential.https://github.com.helper")
        self.assertEqual(env["GIT_CONFIG_VALUE_0"], "!gh auth git-credential")
        self.assertEqual(env["GIT_TERMINAL_PROMPT"], "0")

    def test_request_cannot_supply_command_or_other_inputs(self):
        operator = PinnedRepositoryOperator(spec())
        with self.assertRaisesRegex(SidecarError, "accepts no request inputs"):
            operator(request({"command": "whoami"}))

    def test_success_executes_only_fixed_repository_revision_and_path(self):
        operator = PinnedRepositoryOperator(spec())
        runs = [
            subprocess.CompletedProcess(["gh"], 0, "", ""),
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
        ):
            result = operator(request())

        self.assertEqual(result["result"], "ELIGIBLE")
        self.assertEqual(result["operator_revision"], REVISION)
        self.assertEqual(result["operator_exit_code"], 0)
        self.assertEqual(run.call_count, 8)

        clone_argv = run.call_args_list[2].args[0]
        self.assertEqual(clone_argv[0:4], ["git", "clone", "--quiet", "--filter=blob:none"])
        self.assertIn(f"https://github.com/{REPOSITORY}.git", clone_argv)

        bash_call = run.call_args_list[7]
        self.assertEqual(bash_call.args[0][0], "bash")
        self.assertTrue(bash_call.args[0][1].endswith("/tools/operator.sh"))
        self.assertEqual(bash_call.kwargs["env"]["FIXED_MODE"], "1")
        self.assertEqual(bash_call.kwargs["env"]["GIT_CONFIG_VALUE_0"], "!gh auth git-credential")

    def test_main_revision_drift_fails_before_clone(self):
        operator = PinnedRepositoryOperator(spec())
        runs = [
            subprocess.CompletedProcess(["gh"], 0, "", ""),
            subprocess.CompletedProcess(["git", "ls-remote"], 0, "c" * 40 + "\trefs/heads/main\n", ""),
        ]
        with patch.object(operator, "_run", side_effect=runs) as run:
            with self.assertRaisesRegex(SidecarError, "main no longer matches"):
                operator(request())
        self.assertEqual(run.call_count, 2)

    def test_nonzero_operator_is_unknown_without_retry_semantics(self):
        operator = PinnedRepositoryOperator(spec())
        runs = [
            subprocess.CompletedProcess(["gh"], 0, "", ""),
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
        ):
            result = operator(request())
        self.assertEqual(result["result"], "UNKNOWN")
        self.assertEqual(result["operator_exit_code"], 78)
        self.assertTrue(result["preserve_evidence"])


if __name__ == "__main__":
    unittest.main()
