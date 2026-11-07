# Build, dependency and acceptance matrix — 2026-10-31

Source authority: public WOW Sidecar. Owner decisions: private #26 G9/G10.
This is a source-level inventory, **not** a verified production SBOM or
deployment approval. Every target-specific execution claim must bind to an
immutable candidate SHA, receipt digest and native observation.

| Component | Required dependency / provenance | Current proof | Missing gate |
|---|---|---|---|
| Go source | `go.mod` Go 1.23.0, standard library only; no `go.sum` by design | public workflows assert one module, `go vet ./...` and `go test ./...` | exact final accepted main SHA and external code review |
| Go build toolchain | public hosted workflows select Go 1.24.13; `GOTOOLCHAIN=local` on Go federation/publication jobs | source-pinned CI; native AMD64/ARM64 | pin CI/toolchain identity in final release receipt |
| Static OCI | `CGO_ENABLED=0`, `linux/amd64` and `linux/arm64`; `Dockerfile.wow-go` FROM scratch | nonroot `10001:10001`, no shell, health/ready/card and generation restart smoke in public CI | exact freshly published index/platform digests when new source is released |
| CA trust | `/etc/ssl/certs/ca-certificates.crt` copied explicitly into scratch image | native/container CI build | native TLS behavior and bundle provenance on admitted release |
| Python compatibility | Python >=3.11, `PyJWT[crypto]==2.10.1`; `setuptools>=69`, `wheel` build | `pyproject.toml`, separate Python container and wheelhouse-lock workflow | lock transitive crypto/wheelhouse and test legacy actual operator inventory before retirement |
| Workflow actions | upstream GitHub actions pinned to full commit SHAs; GH release artifacts independently SHA-checked where provided | Go and original `v0.0.1` publication CI | verify exact release workflow/action and attestations for each new image |
| Offline Compose admission | approved Docker Compose v2 `config --format json` and original v0.0.1 receipt | private source-only checker candidate, original receipt preserved | exact-head real render and independent checker qualification; no `compose up` |
| Native TrueNAS | supported TrueNAS Apps/lifecycle and persistent state | public render/source-only evidence | approved read-only live preflight, target-native HIL, reversible rollback |
| OCI edge | rootless Podman/Quadlet on admitted target | adapter/source studies, no operational claim | exact image rebind, arm64 target-native qualification, health/restart and recovery |
| Legacy migration | admitted Agent Dispatch host-control instance and state | code-defined compatibility inventory and source-level cutover state machine | physical inventory, copy-first/hash/backup, crash recovery, independent rollback and owner approval |
| Formal validation | upstream TLA+ TLC with exact SHA-pinned tool JAR | bounded `formal/PeerFence` model candidate | independently executed exact-head model; map to Go cross-process stale-mutation negative |
| CI parity | pinned actionlint; optional ShellCheck, zizmor, act | new static preflight candidate | separate exact-head public GHA; local act only with admitted Docker API |

## Reproducibility / evidence rule

For every proposed release, retain **source commit**, dependency/tool versions,
exact command and runner architecture, stdout/stderr first failure, successful
test matrix, hashes of publication/verification receipts, and native readback.
Do not reuse a green workflow from a different commit as release evidence.
Python transitive dependency completeness and real SBOM/provenance attestations
must be independently verified, not inferred from `go.sum` absence or from
image metadata alone.

## Security and merge governance

Source publication is constructive and reviewed. Independent reviewer/owner
acceptance must be recorded on the *exact* candidate; a `COMMENTED` review is
not `APPROVED`, and successful CI is not a waiver. A public branch report of
`protected=false` or lack of visible rulesets is a governance concern, but
API permission/plan limits prevent proving all effective repository controls
from this snapshot. Keep explicit DLE merge admission and require access by
a repository administrator for any branch-policy changes. Do not lower
review gates to accommodate unavailable private-hosted Actions minutes.

## Acceptable reuse versus custom development

Prefer Go standard library, the official TLC model checker, native GitHub
Actions, actionlint, ShellCheck, zizmor, Docker Compose v2, Git's built-in
worktree/branch/status porcelain, TrueNAS supported App primitives and
rootless Podman/Quadlet. The source-only diagnostic glue must not become a
second scheduler, task authority, provider, container builder or custom formal
proof engine. Additional runtime dependencies need concrete test evidence and
a versioned product acceptance boundary.
