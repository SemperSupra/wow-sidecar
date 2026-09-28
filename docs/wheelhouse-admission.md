# Admitted conventional-Linux wheelhouse

`deploy/wheelhouse-linux-cp312-x86_64.lock.json` records the exact candidate bytes admitted for the first conventional-Linux deployment profile.

The lock is evidence-backed, not resolver-backed:

- source candidate revision: `c83e56d4378853538a4ca8ae7ca56f9763d50fee`;
- public candidate workflow run: `36379398872`;
- GitHub Actions artifact id: `10952063379`;
- candidate manifest digest: `sha256:68b6fa6c8af4ef62c1d2ca3b9a9ac192bc7b2901edc1d4257321e94044dc78f0`;
- target profile: CPython 3.12, x86_64 Linux, glibc 2.34 or newer.

The admission workflow downloads the artifact produced by that exact run, verifies that the recorded artifact id/name belong to the run, compares the manifest and every wheel byte against the committed lock, then re-runs the offline environment construction from the admitted bytes.

The GitHub Actions artifact is intentionally short-lived evidence, not the final distribution channel. A persistent publication/storage gate remains required before an installer can rely on the admitted wheelhouse without re-resolving dependencies.
