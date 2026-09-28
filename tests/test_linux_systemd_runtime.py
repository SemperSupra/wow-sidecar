from __future__ import annotations

from pathlib import Path
import tempfile
import unittest

from wow_sidecar.errors import SidecarError
from wow_sidecar.integrations.linux_cutover import CANDIDATE_UNIT, CORESIDENT_UNITS, LEGACY_UNITS, ServiceState
from wow_sidecar.integrations.linux_systemd import LinuxServiceSpec, UNIT_PATH, render_systemd_unit
from wow_sidecar.integrations.linux_systemd_runtime import (
    CommandResult,
    SystemctlControl,
    prepare_candidate_unit,
    stage_candidate_unit,
)


REVISION = "a" * 40


class ScriptedRunner:
    def __init__(self, responses: list[CommandResult]):
        self.responses = list(responses)
        self.commands: list[tuple[str, ...]] = []

    def __call__(self, argv: tuple[str, ...]) -> CommandResult:
        self.commands.append(argv)
        if not self.responses:
            raise AssertionError("unexpected systemctl command")
        return self.responses.pop(0)


def show(*, load: str = "loaded", active: str = "inactive", unit_file: str = "disabled") -> CommandResult:
    return CommandResult(
        0,
        f"LoadState={load}\nActiveState={active}\nUnitFileState={unit_file}\n",
    )


class SystemctlRuntimeTests(unittest.TestCase):
    def test_observe_uses_fixed_binary_argv_and_exact_state_mapping(self):
        runner = ScriptedRunner([
            show(active="active", unit_file="enabled"),
            show(active="active", unit_file="enabled"),
        ])
        control = SystemctlControl(runner=runner)

        self.assertEqual(
            control.observe(LEGACY_UNITS[0]),
            ServiceState(True, True, True),
        )
        self.assertEqual(
            control.observe(CORESIDENT_UNITS[0]),
            ServiceState(True, True, True),
        )
        self.assertEqual(
            runner.commands[0],
            (
                "/usr/bin/systemctl",
                "--no-ask-password",
                "--no-pager",
                "show",
                "--property=LoadState",
                "--property=ActiveState",
                "--property=UnitFileState",
                LEGACY_UNITS[0],
            ),
        )
        self.assertEqual(runner.commands[1][-1], CORESIDENT_UNITS[0])

    def test_transitional_failed_or_nonbinary_enablement_states_fail_closed(self):
        for response in (
            show(active="activating"),
            show(active="failed"),
            show(unit_file="enabled-runtime"),
            show(unit_file="masked"),
            show(unit_file="static"),
        ):
            with self.subTest(output=response.stdout):
                control = SystemctlControl(runner=ScriptedRunner([response]))
                with self.assertRaises(SidecarError):
                    control.observe(LEGACY_UNITS[0])

    def test_mutation_surface_is_exact_unit_and_fixed_verbs(self):
        runner = ScriptedRunner([
            CommandResult(0, ""),
            CommandResult(0, ""),
            CommandResult(0, ""),
            CommandResult(0, ""),
            CommandResult(0, ""),
        ])
        control = SystemctlControl(runner=runner)
        control.stop(LEGACY_UNITS[0])
        control.disable(LEGACY_UNITS[0])
        control.start(CANDIDATE_UNIT)
        control.enable(CANDIDATE_UNIT)
        control.daemon_reload()

        expected = [
            ("stop", LEGACY_UNITS[0]),
            ("disable", LEGACY_UNITS[0]),
            ("start", CANDIDATE_UNIT),
            ("enable", CANDIDATE_UNIT),
            ("daemon-reload",),
        ]
        observed = [command[3:] for command in runner.commands]
        self.assertEqual(observed, expected)

        with self.assertRaisesRegex(SidecarError, "outside the admitted mutation"):
            control.start(CORESIDENT_UNITS[0])
        with self.assertRaisesRegex(SidecarError, "outside the admitted mutation"):
            control.start("ssh.service")

    def test_nonzero_systemctl_result_is_sanitized_failure(self):
        control = SystemctlControl(runner=ScriptedRunner([
            CommandResult(1, "sensitive stdout must not surface"),
        ]))
        with self.assertRaisesRegex(SidecarError, "^systemctl command failed$"):
            control.start(CANDIDATE_UNIT)

    def test_candidate_health_requires_running_exact_fragment_no_restart_or_reload(self):
        healthy = CommandResult(
            0,
            "LoadState=loaded\n"
            "ActiveState=active\n"
            "SubState=running\n"
            "MainPID=1234\n"
            "NRestarts=0\n"
            "NeedDaemonReload=no\n"
            f"FragmentPath={UNIT_PATH}\n",
        )
        control = SystemctlControl(runner=ScriptedRunner([healthy]))
        receipt = control.candidate_health()
        self.assertTrue(receipt["healthy"])
        self.assertEqual(receipt["restart_count"], 0)
        self.assertFalse(receipt["contains_command_output"])

        bad = (
            healthy.stdout.replace("SubState=running", "SubState=auto-restart"),
            healthy.stdout.replace("MainPID=1234", "MainPID=0"),
            healthy.stdout.replace("NRestarts=0", "NRestarts=1"),
            healthy.stdout.replace("NeedDaemonReload=no", "NeedDaemonReload=yes"),
            healthy.stdout.replace(f"FragmentPath={UNIT_PATH}", "FragmentPath=/tmp/other.service"),
        )
        for output in bad:
            with self.subTest(output=output):
                control = SystemctlControl(runner=ScriptedRunner([CommandResult(0, output)]))
                with self.assertRaises(SidecarError):
                    control.candidate_health()

    def test_stage_candidate_unit_is_create_only_and_exact_idempotent(self):
        spec = LinuxServiceSpec(
            revision=REVISION,
            control_repository="ExampleOrg/control",
            profile_paths=("/etc/wow-sidecar/profiles/fixed.json",),
        )
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw).resolve()
            first = stage_candidate_unit(root, spec)
            target = root / UNIT_PATH.removeprefix("/")
            self.assertTrue(first["mutation_performed"])
            self.assertEqual(target.read_text(encoding="utf-8"), render_systemd_unit(spec))
            self.assertEqual(target.stat().st_mode & 0o777, 0o644)

            second = stage_candidate_unit(root, spec)
            self.assertFalse(second["mutation_performed"])
            self.assertTrue(second["unit_already_exact"])

            target.write_text("different\n", encoding="utf-8")
            with self.assertRaisesRegex(SidecarError, "differs"):
                stage_candidate_unit(root, spec)

    def test_stage_rejects_symlinked_privileged_path(self):
        spec = LinuxServiceSpec(
            revision=REVISION,
            control_repository="ExampleOrg/control",
            profile_paths=("/etc/wow-sidecar/profiles/fixed.json",),
        )
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw).resolve()
            outside = root / "outside"
            outside.mkdir()
            etc = root / "etc"
            etc.symlink_to(outside, target_is_directory=True)
            with self.assertRaisesRegex(SidecarError, "symlink"):
                stage_candidate_unit(root, spec)

    def test_prepare_stages_reloads_and_requires_inactive_disabled_candidate(self):
        spec = LinuxServiceSpec(
            revision=REVISION,
            control_repository="ExampleOrg/control",
            profile_paths=("/etc/wow-sidecar/profiles/fixed.json",),
        )
        runner = ScriptedRunner([
            CommandResult(0, ""),
            show(active="inactive", unit_file="disabled"),
        ])
        control = SystemctlControl(runner=runner)
        with tempfile.TemporaryDirectory() as raw:
            receipt = prepare_candidate_unit(
                root=Path(raw).resolve(),
                spec=spec,
                control=control,
            )
        self.assertTrue(receipt["candidate_present"])
        self.assertFalse(receipt["candidate_active"])
        self.assertFalse(receipt["candidate_enabled"])
        self.assertEqual(runner.commands[0][3:], ("daemon-reload",))


if __name__ == "__main__":
    unittest.main()
