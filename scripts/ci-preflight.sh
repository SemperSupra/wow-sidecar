#!/usr/bin/env bash
# Lightweight local GitHub Actions preflight; never deploys or dispatches jobs.
# --static: run established linters already installed; --act-list: only list
# workflows; --act-run is deliberately NOT implemented.
set -euo pipefail

usage() {
  printf '%s\n' 'Usage: bash scripts/ci-preflight.sh --static|--act-list' >&2
  exit 2
}

[[ "$#" -eq 1 ]] || usage
case "$1" in
  --static)
    command -v actionlint >/dev/null || { echo 'BLOCKED: install pinned actionlint v1.7.12' >&2; exit 3; }
    echo "actionlint_version=$(actionlint -version | head -1)"
    actionlint -oneline .github/workflows/*.yml
    if command -v shellcheck >/dev/null; then
      shellcheck scripts/ci-preflight.sh
    else
      echo 'SKIPPED: shellcheck unavailable (non-authoritative)' >&2
    fi
    if command -v zizmor >/dev/null; then
      zizmor --offline .github/workflows
    else
      echo 'SKIPPED: zizmor unavailable (non-authoritative)' >&2
    fi
    ;;
  --act-list)
    command -v act >/dev/null || { echo 'BLOCKED: act not installed' >&2; exit 3; }
    # Only list workflow jobs. Deliberately do not run workflow commands, use
    # credentials, access Docker, publish images, or mutate the host.
    act --list
    ;;
  *) usage ;;
esac
