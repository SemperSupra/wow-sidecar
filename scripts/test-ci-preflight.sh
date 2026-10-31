#!/usr/bin/env bash
# Mock-only contract test: NEVER executes real Act or Docker.
set -euo pipefail
temp="$(mktemp -d)"
trap 'rm -rf "$temp"' EXIT
cat > "$temp/act" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
if [[ -n "${GITHUB_TOKEN:-}" || -n "${GH_TOKEN:-}" ]]; then
  echo "unexpected ambient GitHub credential" >&2
  exit 7
fi
printf '%s\n' "$@" > "$ACT_ARGS_FILE"
SH
chmod +x "$temp/act"
args="$temp/act-args"
image="example.invalid/wow-ci@sha256:$(printf 'a%.0s' {1..64})"
ACT_ARGS_FILE="$args" WOW_ACT_RUNNER_IMAGE="$image" \
  GITHUB_TOKEN=DO_NOT_LEAK GH_TOKEN=DO_NOT_LEAK PATH="$temp:$PATH" \
  bash scripts/ci-preflight.sh --act-go
grep -Fx -- "workflow_dispatch" "$args"
grep -Fx -- ".github/workflows/go-conformance.yml" "$args"
grep -Fx -- "conformance" "$args"
grep -Fx -- "ubuntu-24.04=$image" "$args"
if grep -Eq 'publication|deployment|release|peer-rendezvous' "$args"; then
  echo "unsafe act workflow selected" >&2
  exit 1
fi
for invalid in '' 'example.invalid/wow-ci:latest' 'example.invalid/wow-ci@sha256:aa'; do
  if ACT_ARGS_FILE="$args" WOW_ACT_RUNNER_IMAGE="$invalid" \
    PATH="$temp:$PATH" bash scripts/ci-preflight.sh --act-go >/dev/null 2>&1; then
    echo "unsafe act image reference admitted" >&2
    exit 1
  fi
done
echo "mock act allowlist and digest constraints: PASS"
