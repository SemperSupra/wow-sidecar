from __future__ import annotations

import argparse
import json
from pathlib import Path
import sys
from typing import Any, Callable

from .deployment_identity import resolve_operator_revision
from .github_app import GitHubAppClient
from .host_authority import GitHubIssueAuthorityReader
from .profiles import compose_profiles, load_profile_file
from .worker import event, process_cycle, run_service


def build_cycle(
    *,
    control_repository: str,
    profile_paths: list[Path],
    repo_root: Path | None = None,
    revision_file: Path | None = None,
    client: GitHubAppClient | None = None,
) -> tuple[Callable[[], list[dict[str, Any]]], str]:
    if not profile_paths:
        raise ValueError("at least one operator profile is required")
    active_client = client or GitHubAppClient.from_env()
    token_provider = getattr(active_client, "installation_token_for_repository", None)
    if not callable(token_provider):
        raise ValueError("control client must provide repository-scoped installation tokens")
    profiles = [
        load_profile_file(
            path,
            repository_token_provider=token_provider,
        )
        for path in profile_paths
    ]
    registry, bindings = compose_profiles(profiles)
    authority_reader = GitHubIssueAuthorityReader(client=active_client)
    revision = resolve_operator_revision(
        repo_root=repo_root,
        revision_file=revision_file,
    )

    def cycle() -> list[dict[str, Any]]:
        return process_cycle(
            control_client=active_client,
            authority_reader=authority_reader,
            control_repository=control_repository,
            registry=registry,
            authority_bindings=bindings,
            local_operator_revision=revision,
        )

    return cycle, revision


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Run the WOW Sidecar bounded trusted-host worker")
    parser.add_argument("--control-repository", required=True, help="owner/name execution-control repository")
    parser.add_argument("--profile", action="append", required=True, dest="profiles", help="operator profile JSON file; repeatable")
    identity = parser.add_mutually_exclusive_group(required=True)
    identity.add_argument("--repo-root", type=Path, help="clean exact WOW Sidecar Git checkout")
    identity.add_argument("--revision-file", type=Path, help="read-only source revision file baked into an immutable image")
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--once", action="store_true", help="process at most one pending request and exit")
    mode.add_argument("--serve", action="store_true", help="run the bounded worker service")
    parser.add_argument("--poll-seconds", type=int, default=15)
    args = parser.parse_args(argv)

    try:
        cycle, revision = build_cycle(
            control_repository=args.control_repository,
            profile_paths=[Path(path) for path in args.profiles],
            repo_root=args.repo_root,
            revision_file=args.revision_file,
        )
        if args.once:
            print(json.dumps({"operator_revision": revision, "results": cycle()}, sort_keys=True))
            return 0
        return run_service(cycle, poll_seconds=args.poll_seconds)
    except KeyboardInterrupt:
        return 0
    except Exception as exc:
        print(
            event(
                "UNKNOWN",
                "wow-sidecar-service-failed-closed",
                reason=f"{type(exc).__name__}: raw error omitted",
                preserve_evidence=True,
            ),
            file=sys.stderr,
        )
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
