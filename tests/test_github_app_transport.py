from __future__ import annotations

import inspect
import json
import unittest

from wow_sidecar import github_app
from wow_sidecar.github_app import GitHubAppClient, GitHubAppConfig


class FakeResponse:
    def __init__(self, payload):
        self.payload = payload

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, tb):
        return False

    def read(self):
        return self.payload


class RecordingClient(GitHubAppClient):
    def __init__(self):
        super().__init__(GitHubAppConfig(app_id="123", private_key="synthetic"))
        self.calls = []

    def app_jwt(self, *, now=None):
        return "synthetic-app-jwt"

    def _request_json(self, method, path, *, token, body=None, expected_shape="object", opener=None):
        self.calls.append((method, path, token, body, expected_shape))
        if path.endswith("/installation"):
            return {"id": 77}
        if path == "/app/installations/77/access_tokens":
            return {"token": "synthetic-installation-token"}
        raise AssertionError(f"unexpected call: {method} {path}")


class GitHubAppTransportBoundaryTests(unittest.TestCase):
    def test_source_excludes_historical_agent_dispatch_methods(self):
        source = inspect.getsource(github_app)
        for symbol in (
            "repository_file_json",
            "WorkflowDispatchRequest",
            "dispatch_workflow",
            "def dispatch(",
            "def workflow_runs(",
        ):
            with self.subTest(symbol=symbol):
                self.assertNotIn(symbol, source)
        client = GitHubAppClient(GitHubAppConfig(app_id="123", private_key="synthetic"))
        self.assertFalse(hasattr(client, "repository_file_json"))
        self.assertFalse(hasattr(client, "dispatch"))
        self.assertFalse(hasattr(client, "workflow_runs"))

    def test_request_json_is_generic_object_or_array_transport(self):
        payloads = [
            (b'{"ok":true}', "object", {"ok": True}),
            (b'[{"id":1}]', "array", [{"id": 1}]),
            (b"", "object", {}),
            (b"", "array", []),
        ]
        for raw, shape, expected in payloads:
            with self.subTest(shape=shape, raw=raw):
                seen = []
                def opener(request, timeout):
                    seen.append((request.full_url, request.method, timeout, request.headers.get("Authorization")))
                    return FakeResponse(raw)

                client = GitHubAppClient(
                    GitHubAppConfig(app_id="123", private_key="synthetic", api_base="https://api.example.test"),
                    opener=opener,
                )
                result = client._request_json("GET", "/transport-proof", token="synthetic-token", expected_shape=shape)
                self.assertEqual(result, expected)
                self.assertEqual(
                    seen,
                    [("https://api.example.test/transport-proof", "GET", 20.0, "Bearer synthetic-token")],
                )

    def test_installation_token_flow_is_repository_scoped_transport(self):
        client = RecordingClient()
        token = client.installation_token_for_repository("ExampleOrg/example-repo")
        self.assertEqual(token, "synthetic-installation-token")
        self.assertEqual(
            client.calls,
            [
                (
                    "GET",
                    "/repos/ExampleOrg/example-repo/installation",
                    "synthetic-app-jwt",
                    None,
                    "object",
                ),
                (
                    "POST",
                    "/app/installations/77/access_tokens",
                    "synthetic-app-jwt",
                    {"repositories": ["example-repo"]},
                    "object",
                ),
            ],
        )

    def test_repository_locator_validation_fails_closed(self):
        client = RecordingClient()
        for repository in ("missing-slash", "too/many/parts", "/repo", "owner/"):
            with self.subTest(repository=repository):
                with self.assertRaises(Exception):
                    client.installation_token_for_repository(repository)


if __name__ == "__main__":
    unittest.main()
