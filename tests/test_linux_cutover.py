from __future__ import annotations

from dataclasses import replace
from pathlib import Path
import tempfile
import unittest

from wow_sidecar.errors import SidecarError
from wow_sidecar.integrations.linux_cutover import (
    CANDIDATE_UNIT,
    CORESIDENT_UNITS,
    LEGACY_UNITS,
    CutoverJournal,
    ServiceState,
    begin_legacy_cutover,
    recover_legacy_cutover,
)


REVISION = "a" * 40


class SimulatedCrash(BaseException):
    pass


class FakeSystemd:
    def __init__(
        self,
        *,
        legacy_primary: ServiceState = ServiceState(True, True, True),
        coresident: ServiceState = ServiceState(True, True, True),
        candidate: ServiceState = ServiceState(True, False, False),
        crash_after_mutation: int | None = None,
        fail_once_operation: str | None = None,
    ):
        self.states = {
            LEGACY_UNITS[0]: legacy_primary,
            CORESIDENT_UNITS[0]: coresident,
            CANDIDATE_UNIT: candidate,
        }
        self.mutation_count = 0
        self.crash_after_mutation = crash_after_mutation
        self.fail_once_operation = fail_once_operation
        self.failed = False
        self.dual_active_ever = False
        self.mutated_units: list[str] = []

    def observe(self, unit: str) -> ServiceState:
        return self.states[unit]

    def _mutate(self, operation: str, unit: str, **changes: bool) -> None:
        if self.fail_once_operation == operation and not self.failed:
            self.failed = True
            raise SidecarError(f"synthetic {operation} failure")
        current = self.states[unit]
        if not current.present:
            raise SidecarError("cannot mutate absent unit")
        self.states[unit] = replace(current, **changes)
        self.mutated_units.append(unit)
        self.mutation_count += 1
        candidate = self.states[CANDIDATE_UNIT]
        legacy_active = any(self.states[name].active for name in LEGACY_UNITS)
        self.dual_active_ever = self.dual_active_ever or (
            candidate.active and legacy_active
        )
        if self.crash_after_mutation == self.mutation_count:
            raise SimulatedCrash(f"crash after mutation {self.mutation_count}")

    def start(self, unit: str) -> None:
        self._mutate("start:" + unit, unit, active=True)

    def stop(self, unit: str) -> None:
        self._mutate("stop:" + unit, unit, active=False)

    def enable(self, unit: str) -> None:
        self._mutate("enable:" + unit, unit, enabled=True)

    def disable(self, unit: str) -> None:
        self._mutate("disable:" + unit, unit, enabled=False)


class LinuxCutoverTests(unittest.TestCase):
    def _journal(self, root: Path) -> CutoverJournal:
        return CutoverJournal(root)

    def test_happy_path_never_dual_active_and_commits_candidate_only(self):
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw).resolve()
            control = FakeSystemd()
            receipt = begin_legacy_cutover(
                control=control,
                journal=self._journal(root),
                desired_revision=REVISION,
                health_check=lambda: True,
            )

            self.assertEqual(receipt["outcome"], "committed")
            self.assertFalse(control.dual_active_ever)
            self.assertEqual(control.observe(CANDIDATE_UNIT), ServiceState(True, True, True))
            self.assertEqual(control.observe(LEGACY_UNITS[0]), ServiceState(True, False, False))
            self.assertEqual(control.observe(CORESIDENT_UNITS[0]), ServiceState(True, True, True))
            self.assertNotIn(CORESIDENT_UNITS[0], control.mutated_units)
            self.assertEqual(self._journal(root).read().phase, "committed")

    def test_health_failure_rolls_back_exact_legacy_state(self):
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw).resolve()
            original = ServiceState(True, True, True)
            control = FakeSystemd(legacy_primary=original)
            receipt = begin_legacy_cutover(
                control=control,
                journal=self._journal(root),
                desired_revision=REVISION,
                health_check=lambda: False,
            )

            self.assertEqual(receipt["outcome"], "rolled-back")
            self.assertEqual(control.observe(LEGACY_UNITS[0]), original)
            self.assertEqual(control.observe(CANDIDATE_UNIT), ServiceState(True, False, False))
            self.assertFalse(control.dual_active_ever)
            self.assertEqual(self._journal(root).read().phase, "rolled-back")

    def test_normal_mutation_failures_roll_back_without_dual_active(self):
        operations = (
            "stop:" + LEGACY_UNITS[0],
            "disable:" + LEGACY_UNITS[0],
            "start:" + CANDIDATE_UNIT,
            "enable:" + CANDIDATE_UNIT,
        )
        for operation in operations:
            with self.subTest(operation=operation):
                with tempfile.TemporaryDirectory() as raw:
                    root = Path(raw).resolve()
                    original = ServiceState(True, True, True)
                    control = FakeSystemd(
                        legacy_primary=original,
                        fail_once_operation=operation,
                    )
                    receipt = begin_legacy_cutover(
                        control=control,
                        journal=self._journal(root),
                        desired_revision=REVISION,
                        health_check=lambda: True,
                    )
                    self.assertEqual(receipt["outcome"], "rolled-back")
                    self.assertEqual(control.observe(LEGACY_UNITS[0]), original)
                    self.assertEqual(control.observe(CANDIDATE_UNIT), ServiceState(True, False, False))
                    self.assertFalse(control.dual_active_ever)

    def test_process_crash_after_each_mutation_recovers_to_legacy(self):
        # Successful path performs stop legacy, disable legacy, start candidate,
        # enable candidate. A crash after any mutation occurs before the next
        # durable phase can declare the transition committed.
        for mutation in range(1, 5):
            with self.subTest(mutation=mutation):
                with tempfile.TemporaryDirectory() as raw:
                    root = Path(raw).resolve()
                    original = ServiceState(True, True, True)
                    control = FakeSystemd(
                        legacy_primary=original,
                        crash_after_mutation=mutation,
                    )
                    journal = self._journal(root)
                    with self.assertRaises(SimulatedCrash):
                        begin_legacy_cutover(
                            control=control,
                            journal=journal,
                            desired_revision=REVISION,
                            health_check=lambda: True,
                        )

                    control.crash_after_mutation = None
                    receipt = recover_legacy_cutover(
                        control=control,
                        journal=journal,
                    )
                    self.assertEqual(receipt["outcome"], "rolled-back")
                    self.assertTrue(receipt["recovered"])
                    self.assertEqual(control.observe(LEGACY_UNITS[0]), original)
                    self.assertEqual(control.observe(CANDIDATE_UNIT), ServiceState(True, False, False))
                    self.assertFalse(control.dual_active_ever)

    def test_crash_at_transient_checkpoints_recovers_to_legacy(self):
        transient = {
            "prepared",
            "quiescing-legacy",
            "legacy-quiesced",
            "starting-candidate",
            "candidate-started",
            "verifying-candidate",
            "committing",
        }
        for phase in transient:
            with self.subTest(phase=phase):
                with tempfile.TemporaryDirectory() as raw:
                    root = Path(raw).resolve()
                    original = ServiceState(True, True, True)
                    control = FakeSystemd(legacy_primary=original)
                    journal = self._journal(root)

                    def crash_at(observed: str) -> None:
                        if observed == phase:
                            raise SimulatedCrash(observed)

                    with self.assertRaises(SimulatedCrash):
                        begin_legacy_cutover(
                            control=control,
                            journal=journal,
                            desired_revision=REVISION,
                            health_check=lambda: True,
                            checkpoint_hook=crash_at,
                        )

                    receipt = recover_legacy_cutover(
                        control=control,
                        journal=journal,
                    )
                    self.assertEqual(receipt["outcome"], "rolled-back")
                    self.assertEqual(control.observe(LEGACY_UNITS[0]), original)
                    self.assertEqual(control.observe(CANDIDATE_UNIT), ServiceState(True, False, False))
                    self.assertFalse(control.dual_active_ever)

    def test_crash_after_committed_checkpoint_recovers_forward(self):
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw).resolve()
            control = FakeSystemd()
            journal = self._journal(root)

            def crash_at(phase: str) -> None:
                if phase == "committed":
                    raise SimulatedCrash(phase)

            with self.assertRaises(SimulatedCrash):
                begin_legacy_cutover(
                    control=control,
                    journal=journal,
                    desired_revision=REVISION,
                    health_check=lambda: True,
                    checkpoint_hook=crash_at,
                )

            receipt = recover_legacy_cutover(
                control=control,
                journal=journal,
            )
            self.assertEqual(receipt["outcome"], "committed")
            self.assertTrue(receipt["recovered"])
            self.assertEqual(control.observe(CANDIDATE_UNIT), ServiceState(True, True, True))
            self.assertEqual(control.observe(LEGACY_UNITS[0]), ServiceState(True, False, False))
            self.assertFalse(control.dual_active_ever)

    def test_candidate_must_be_staged_inactive_disabled_before_cutover(self):
        invalid = (
            ServiceState(False, False, False),
            ServiceState(True, True, False),
            ServiceState(True, False, True),
        )
        for candidate in invalid:
            with self.subTest(candidate=candidate):
                with tempfile.TemporaryDirectory() as raw:
                    root = Path(raw).resolve()
                    control = FakeSystemd(candidate=candidate)
                    with self.assertRaises(SidecarError):
                        begin_legacy_cutover(
                            control=control,
                            journal=self._journal(root),
                            desired_revision=REVISION,
                            health_check=lambda: True,
                        )

    def test_existing_journal_and_missing_live_legacy_fail_closed(self):
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw).resolve()
            journal = self._journal(root)
            control = FakeSystemd(
                legacy_primary=ServiceState(True, False, True),
            )
            with self.assertRaisesRegex(SidecarError, "no legacy unit is active"):
                begin_legacy_cutover(
                    control=control,
                    journal=journal,
                    desired_revision=REVISION,
                    health_check=lambda: True,
                )

        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw).resolve()
            journal = self._journal(root)
            control = FakeSystemd()
            receipt = begin_legacy_cutover(
                control=control,
                journal=journal,
                desired_revision=REVISION,
                health_check=lambda: True,
            )
            self.assertEqual(receipt["outcome"], "committed")
            with self.assertRaisesRegex(SidecarError, "journal already exists"):
                begin_legacy_cutover(
                    control=control,
                    journal=journal,
                    desired_revision=REVISION,
                    health_check=lambda: True,
                )


if __name__ == "__main__":
    unittest.main()
