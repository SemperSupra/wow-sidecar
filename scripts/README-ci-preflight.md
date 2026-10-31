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
```

`--static` requires actionlint and fails closed if missing. ShellCheck/zizmor
are supplemental and report SKIPPED when missing. `--act-list` enumerates
only; it cannot execute any GitHub Actions job, call Docker, publish or deploy.

An actual `act` run must be separately admitted with an explicit workflow
and non-production fixture: do not treat `act` as an interchangeable ARM64,
`workflow_dispatch`, GHCR, privileged target or real `GITHUB_TOKEN` executor.
Document unsupported runner labels and any divergent artifact/permission
semantics. Prefer real **public-safe $0 hosted CI** for final qualification.

## 3. Evidence and dependencies

Keep source SHA, actionlint version+digest, ShellCheck/zizmor versions (or
SKIPPED), exact command/exit code/first failure and hosted run IDs in Git/DLE.
No private host addresses or tokens in public output. This script does not
replace the existing Go standard-library module contract or Python wheelhouse
lock, and does not alter Dockerfiles, deployment manifests or release actions.
