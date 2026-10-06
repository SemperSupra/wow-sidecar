from __future__ import annotations

import copy
import json
from pathlib import Path
import unittest

from wow_sidecar.errors import SidecarError
from wow_sidecar.host_control import HostOperatorRequest, _canonical_json, _receipt
from wow_sidecar.profiles import load_profile


VECTORS = (
    Path(__file__).resolve().parents[1] / "conformance" / "v1" / "vectors.json"
)


def _load() -> dict:
    return json.loads(VECTORS.read_text(encoding="utf-8"))


def _index(items: list[dict]) -> dict[str, dict]:
    return {item["id"]: item for item in items}


def _assign(value: dict, dotted: str, replacement) -> None:
    parts = dotted.split(".")
    cursor = value
    for part in parts[:-1]:
        cursor = cursor[part]
    cursor[parts[-1]] = replacement


def _materialize(vector: dict, index: dict[str, dict]) -> dict:
    if "value" in vector:
        value = copy.deepcopy(vector["value"])
    else:
        value = copy.deepcopy(index[vector["base"]]["value"])

    mutation = vector.get("mutate")
    if mutation is None:
        return value
    if "set" in mutation:
        dotted, replacement = mutation["set"]
        _assign(value, dotted, replacement)
    elif "add" in mutation:
        dotted, replacement = mutation["add"]
        _assign(value, dotted, replacement)
    elif "add_top_level" in mutation:
        key, replacement = mutation["add_top_level"]
        value[key] = replacement
    else:
        raise AssertionError(f"unknown conformance mutation: {mutation}")
    return value


class RequestConformanceTests(unittest.TestCase):
    def test_request_vectors_match_python_oracle(self):
        vectors = _load()["request_vectors"]
        index = _index(vectors)
        for vector in vectors:
            with self.subTest(vector=vector["id"]):
                value = _materialize(vector, index)
                if vector["accept"]:
                    parsed = HostOperatorRequest.parse(value)
                    self.assertEqual(parsed.raw, value)
                else:
                    with self.assertRaises(SidecarError) as caught:
                        HostOperatorRequest.parse(value)
                    self.assertIn(vector["error_contains"], str(caught.exception))


class ProfileConformanceTests(unittest.TestCase):
    def test_profile_vectors_match_python_oracle(self):
        vectors = _load()["profile_vectors"]
        index = _index(vectors)
        for vector in vectors:
            with self.subTest(vector=vector["id"]):
                value = _materialize(vector, index)
                if vector["accept"]:
                    loaded = load_profile(
                        value,
                        repository_token_provider=lambda _repository: "synthetic-token",
                    )
                    self.assertEqual(loaded.profile, value["profile"])
                else:
                    with self.assertRaises(SidecarError) as caught:
                        load_profile(
                            value,
                            repository_token_provider=lambda _repository: "synthetic-token",
                        )
                    self.assertIn(vector["error_contains"], str(caught.exception))


class CanonicalJsonConformanceTests(unittest.TestCase):
    def test_canonical_json_vectors_match_python_oracle(self):
        for vector in _load()["canonical_vectors"]:
            with self.subTest(vector=vector["id"]):
                self.assertEqual(
                    _canonical_json(vector["value"]).decode("utf-8"),
                    vector["canonical_json"],
                )


class ReceiptConformanceTests(unittest.TestCase):
    def test_receipt_vectors_match_python_oracle(self):
        for vector in _load()["receipt_vectors"]:
            with self.subTest(vector=vector["id"]):
                self.assertEqual(_receipt(**vector["args"]), vector["expected"])


if __name__ == "__main__":
    unittest.main()
