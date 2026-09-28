from __future__ import annotations

import unittest

from wow_sidecar.errors import SidecarError
from wow_sidecar.lifecycle import (
    ObservedInstallation,
    plan_lifecycle,
    recovery_manifest,
    serialized_receipt_contains,
    verify_recovery_copy,
)


REV_A = "a" * 40
REV_B = "b" * 40


class LifecyclePlanningTests(unittest.TestCase):
    def test_first_install_has_remove_new_target_rollback_only(self):
        plan = plan_lifecycle(
            operation="install",
            observed=ObservedInstallation(False, False, None),
            desired_revision=REV_A,
        )
        self.assertEqual(plan.observed_state, "absent")
        self.assertEqual(plan.steps, ("materialize-new-target", "verify-desired-revision"))
        self.assertEqual(plan.rollback_steps, ("remove-new-target-only",))

    def test_unexpected_preexisting_target_is_never_overwritten(self):
        observed = ObservedInstallation(True, False, None, data_present=True)
        for operation in ("install", "upgrade", "repair", "uninstall"):
            with self.subTest(operation=operation):
                with self.assertRaisesRegex(SidecarError, "explicit import-existing"):
                    plan_lifecycle(
                        operation=operation,
                        observed=observed,
                        desired_revision=REV_B if operation != "uninstall" else None,
                        recovery_verified=True,
                    )

    def test_upgrade_requires_copy_first_verification_and_exact_restore(self):
        observed = ObservedInstallation(True, True, REV_A, data_present=True)
        with self.assertRaisesRegex(SidecarError, "verified copy-first recovery"):
            plan_lifecycle(operation="upgrade", observed=observed, desired_revision=REV_B)
        plan = plan_lifecycle(
            operation="upgrade",
            observed=observed,
            desired_revision=REV_B,
            recovery_verified=True,
        )
        self.assertEqual(
            plan.steps,
            (
                "retain-original-source-state",
                "verify-recovery-copy",
                "materialize-desired-revision",
                "verify-desired-revision",
            ),
        )
        self.assertEqual(
            plan.rollback_steps,
            ("restore-exact-prior-revision", "verify-restored-revision"),
        )

    def test_import_existing_is_explicit_copy_first_migration(self):
        observed = ObservedInstallation(True, False, None, data_present=True)
        plan = plan_lifecycle(
            operation="import-existing",
            observed=observed,
            desired_revision=REV_B,
            recovery_verified=True,
        )
        self.assertEqual(plan.observed_state, "legacy-unmanaged")
        self.assertEqual(plan.steps[0:2], ("retain-original-source-state", "verify-recovery-copy"))
        self.assertEqual(
            plan.rollback_steps,
            ("remove-new-managed-target", "preserve-original-source-state"),
        )

    def test_uninstall_preserve_data_requires_recovery_if_data_exists(self):
        observed = ObservedInstallation(True, True, REV_A, data_present=True)
        with self.assertRaisesRegex(SidecarError, "verified recovery"):
            plan_lifecycle(operation="uninstall", observed=observed, preserve_data=True)
        plan = plan_lifecycle(
            operation="uninstall",
            observed=observed,
            preserve_data=True,
            recovery_verified=True,
        )
        self.assertEqual(plan.steps, ("verify-recovery-copy", "remove-managed-runtime"))
        self.assertNotIn("remove-managed-data", plan.steps)

    def test_destructive_uninstall_is_explicit(self):
        plan = plan_lifecycle(
            operation="uninstall",
            observed=ObservedInstallation(True, True, REV_A, data_present=True),
            preserve_data=False,
        )
        self.assertEqual(plan.steps, ("remove-managed-runtime", "remove-managed-data"))
        self.assertFalse(plan.preserve_data)

    def test_recovery_manifest_contains_hashes_and_sizes_not_values(self):
        secret_a = b"synthetic-private-key-value"
        secret_b = b"synthetic-token-value"
        manifest = recovery_manifest(
            source_state="legacy-unmanaged",
            material=(("app-key", secret_a), ("runtime-config", secret_b)),
        )
        receipt = manifest.receipt()
        self.assertFalse(receipt["contains_secret_values"])
        self.assertFalse(serialized_receipt_contains(receipt, secret_a.decode()))
        self.assertFalse(serialized_receipt_contains(receipt, secret_b.decode()))
        self.assertEqual(receipt["items"][0]["size"], len(secret_a))
        self.assertTrue(receipt["items"][0]["digest"].startswith("sha256:"))

    def test_recovery_copy_verification_is_exact(self):
        raw = {"app-key": b"secret-one", "runtime-config": b"config-two"}
        manifest = recovery_manifest(source_state="managed", material=raw.items())
        receipt = verify_recovery_copy(manifest, dict(raw))
        self.assertTrue(receipt["verified"])
        self.assertFalse(receipt["contains_secret_values"])

        wrong = dict(raw)
        wrong["app-key"] = b"changed"
        with self.assertRaisesRegex(SidecarError, "size mismatch|digest mismatch"):
            verify_recovery_copy(manifest, wrong)

        missing = {"app-key": raw["app-key"]}
        with self.assertRaisesRegex(SidecarError, "item set mismatch"):
            verify_recovery_copy(manifest, missing)

    def test_receipts_never_encode_platform_or_product_integration_policy(self):
        plan = plan_lifecycle(
            operation="upgrade",
            observed=ObservedInstallation(True, True, REV_A, data_present=True),
            desired_revision=REV_B,
            recovery_verified=True,
        )
        serialized = str(plan.receipt()).lower()
        for forbidden in ("garm", "/opt/", "systemd", "truenas", "agent-dispatch"):
            with self.subTest(forbidden=forbidden):
                self.assertNotIn(forbidden, serialized)


if __name__ == "__main__":
    unittest.main()
