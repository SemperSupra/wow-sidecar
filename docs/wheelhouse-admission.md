# Admitted conventional-Linux wheelhouse

`deploy/wheelhouse-linux-cp312-x86_64.lock.json` records the exact candidate bytes admitted for the current CPython 3.12 / x86_64 deployment profile.

Current candidate evidence:

- qualified source revision: `246a7f635c70cf93803b1932ef4129040f6ff458`;
- published equivalent source revision: `8c2949691265027acd039fec674787da05c4fd99`;
- both revisions have complete Git tree `5d43ddcfb8d36748448d8c3bebef0c4abfd12f0f`;
- public candidate workflow run: `36381580110`;
- GitHub Actions artifact id: `10952697781`;
- candidate manifest digest: `sha256:48f300d6ef2b755a5a20d1e17e4979402b8df78048494397b6c8cab8c6f7fe58`;
- product wheel: `sha256:adf67064e5c63d950a0301083c84e3c40f2bda87a85d67bec8a97036e8e5e871`;
- target profile: CPython 3.12, x86_64 Linux, glibc 2.34 or newer.

The admission workflow downloads the artifact produced by that exact run, verifies that the recorded artifact id/name belong to the run, compares the manifest and every wheel byte against the committed lock, then re-runs offline environment construction from the admitted bytes.

The candidate run built the WOW product wheel twice and required byte-identical output. The four third-party dependency wheel hashes are unchanged from the prior admission; the product wheel changed because the qualified WOW source advanced.

The GitHub Actions artifact remains short-lived evidence. Persistent prerelease evidence publication is a separate gate and must be refreshed to this lock before downstream image publication may consume it.
