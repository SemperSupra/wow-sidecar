#!/usr/bin/env bash
# Source-only CI preflight: no release, credentials or target deployment.
set -euo pipefail

usage() {
  printf '%s\n' 'Usage: bash scripts/ci-preflight.sh --static|--act-list|--act-go' >&2
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
    act --list
    ;;
  --act-go)
    command -v act >/dev/null || { echo 'BLOCKED: act not installed' >&2; exit 3; }
    image="${WOW_ACT_RUNNER_IMAGE:-}"
    # Reject mutable tags, empty references, arguments hidden in whitespace,
    # and unverified images. The owner must obtain a trusted digest out-of-band.
    if [[ "$image" != *@sha256:* || "$image" == *[[:space:]]* ]]; then
      echo 'BLOCKED: set WOW_ACT_RUNNER_IMAGE to an approved digest-pinned image' >&2
      exit 3
    fi
    digest="${image##*@sha256:}"
    if [[ "${#digest}" -ne 64 || "$digest" =~ [^0-9a-f] || "${image%@sha256:*}" == "" ]]; then
      echo 'BLOCKED: malformed SHA256 container image reference' >&2
      exit 3
    fi
    # An explicit allowlist selects ONE source-only conformance job.
    # No auto-detection, production credentials, private workflow or publish.
    env -u GITHUB_TOKEN -u GH_TOKEN -u GITHUB_APP_ID -u GITHUB_PRIVATE_KEY \
      act workflow_dispatch \
      -W .github/workflows/go-conformance.yml \
      -j conformance \
      -P "ubuntu-24.04=$image" \
      --pull=false \
      --secret-file /dev/null
    ;;
  *) usage ;;
esac
