# GitHub Actions CI preflight (G7 / G10)

Owner approval: private product DLE #26 release-audit disposition G7/G10.

**Public GitHub Actions remains the source-of-truth executor.** Local preflight
does not establish GitHub-hosted equivalence, native ARM64 behavior, provenance,
registry permission, real host HIL, or release readiness. Never execute image
publication workflows locally with production credentials merely to test syntax.

## 1. Pin and install existing tooling

From a non-privileged approved Linux/WSL2 workspace:

- `actionlint v1.7.12` Linux amd64 release archive:
  `https://github.com/rhysd/actionlint/releases/download/v1.7.12/actionlint_1.7.12_linux_amd64.tar.gz`
  upstream GitHub artifact SHA256:
  `8aca8db96f1b94770f1b0d72b6dddcb1ebb8123cb3712530b08cc387b349a3d8`.
- `shellcheck`: use an approved distro package or separately verified binary;
  record version and package provenance.
- `zizmor`: optional established security scanner; record version, use
  `--offline`, and report absent scanner as SKIPPED, not PASS.
- `nektos/act`: optional local simulator; use only if an approved
  Docker-compatible daemon exists in WSL2/Linux. No Docker Desktop requirement.

Do not run unaudited `curl | sh` or install privileged system components
automatically. The new public workflow uses the upstream release and checks
its exact SHA256 **before execution**; no network is required at runtime if the
pinned binary is pre-staged.

## 2. Preflight

```sh
bash scripts/ci-preflight.sh --static
bash scripts/ci-preflight.sh --act-list

# Explicit, opt-in local Go conformance ONLY, on an already authorized
# Docker-compatible Linux/WSL2 engine with no production credentials.
export WOW_ACT_RUNNER_IMAGE='approved/runner@sha256:<64-lowercase-hex>'
bash scripts/ci-preflight.sh --act-go

# Pure mocked allowlist validation; does NOT invoke Act/Docker:
bash scripts/test-ci-preflight.sh
```

`--static` requires actionlint and fails closed if missing. ShellCheck/zizmor
are supplemental and report SKIPPED when missing. `--act-list` enumerates
only. `--act-go` is a **deliberate, opt-in local Docker operation** limited
to the existing public `.github/workflows/go-conformance.yml` job
`conformance`; it requires an approved digest-pinned runner image, strips
common ambient GitHub token variables and passes an empty explicit secrets
file. The command neither selects the release workflow nor authorizes
publication or deployment. Other credentials, Docker socket privileges,
Act configuration and local file visibility must still be assessed by the
local operator before invocation.

The mocked preflight test is safe for public GHA and validates command
selection and digest constraints without invoking Docker. A real Act run
**has not been performed by this source-only workcell**. Act does not prove
native ARM64, registry authority, GitHub artifact semantics or target HIL.
Record the Act and runner-image versions/digests and any discrepancies;
prefer **public-safe $0 hosted CI** as final qualification authority.

## 3. Evidence and dependencies

Keep source SHA, actionlint version+digest, ShellCheck/zizmor versions (or
SKIPPED), exact command/exit code/first failure and hosted run IDs in Git/DLE.
No private host addresses or tokens in public output. This script does not
replace the existing Go standard-library module contract or Python wheelhouse
lock, and does not alter Dockerfiles, deployment manifests or release actions.
