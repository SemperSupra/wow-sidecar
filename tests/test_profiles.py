from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path

from wow_sidecar.errors import SidecarError
from wow_sidecar.profiles import (
    PROFILE_SCHEMA,
    compose_profiles,
    load_profile,
    load_profile_file,
)


PROFILE = "fixed-operator"
REVISION = "a" * 40
AUTH_REV = "sha256:" + "b" * 64


def profile_value(**overrides):
    value = {
        "schema": PROFILE_SCHEMA,
        "profile": PROFILE,
        "authority": {
            "record": "github-issue-comment:ExampleOrg/authority#7:11",
            "revision": AUTH_REV,
            "state": "open",
        },
        "operator": {
            "kind": "pinned-repository",
            "repository": "ExampleOrg/operator-repo",
            "revision": REVISION,
            "operator_path": "tools/operator.sh",
            "require_main_revision": True,
            "environment": {"FIXED_MODE": "1"},
        },
    }
    value.update(overrides)
    return value


class OperatorProfileTests(unittest.TestCase):
    def test_exact_profile_loads_registry_handler_and_authority(self):
        loaded = load_profile(profile_value())
        self.assertEqual(loaded.profile, PROFILE)
        self.assertEqual(loaded.authority.record, "github-issue-comment:ExampleOrg/authority#7:11")
        self.assertEqual(loaded.authority.revision, AUTH_REV)
        self.assertEqual(loaded.handler.spec.repository, "ExampleOrg/operator-repo")
        self.assertEqual(loaded.handler.spec.revision, REVISION)
        self.assertEqual(loaded.handler.spec.operator_path, "tools/operator.sh")
        self.assertEqual(dict(loaded.handler.spec.environment), {"FIXED_MODE": "1"})

    def test_unknown_top_level_field_or_operator_kind_fails_closed(self):
        extra = profile_value()
        extra["command"] = "whoami"
        with self.assertRaisesRegex(SidecarError, "fields do not match"):
            load_profile(extra)

        wrong_kind = profile_value()
        wrong_kind["operator"] = dict(wrong_kind["operator"])
        wrong_kind["operator"]["kind"] = "shell"
        with self.assertRaisesRegex(SidecarError, "unsupported operator kind"):
            load_profile(wrong_kind)

    def test_request_command_surface_cannot_be_encoded_in_operator_document(self):
        value = profile_value()
        value["operator"] = dict(value["operator"])
        value["operator"]["command"] = "bash -c whoami"
        with self.assertRaisesRegex(SidecarError, "fields do not match"):
            load_profile(value)

    def test_authority_requires_open_state_and_valid_revision(self):
        closed = profile_value()
        closed["authority"] = dict(closed["authority"])
        closed["authority"]["state"] = "closed"
        with self.assertRaisesRegex(SidecarError, "must be open"):
            load_profile(closed)

        latest = profile_value()
        latest["authority"] = dict(latest["authority"])
        latest["authority"]["revision"] = "latest"
        with self.assertRaisesRegex(SidecarError, "sha256"):
            load_profile(latest)

    def test_profile_file_round_trip_and_duplicate_rejection(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "profile.json"
            path.write_text(json.dumps(profile_value()), encoding="utf-8")
            loaded = load_profile_file(path)
        registry, authorities = compose_profiles([loaded])
        self.assertEqual(set(registry), {PROFILE})
        self.assertEqual(set(authorities), {PROFILE})
        with self.assertRaisesRegex(SidecarError, "duplicate operator profile"):
            compose_profiles([loaded, loaded])


if __name__ == "__main__":
    unittest.main()
