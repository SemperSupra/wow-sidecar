# AGENTS.md

## Repository role

This is the public source/release authority for WOW Sidecar.

Authoritative private development, adversarial testing, qualification, and release-candidate preparation occur in `SemperSupra/wow-sidecar-private`.

## Rules

- Do not add site credentials, private HIL evidence, private repository identifiers that are not intentionally public, or secret-bearing fixtures.
- Preserve request/claim/receipt compatibility deliberately; protocol changes require explicit versioning.
- No generic remote shell, arbitrary command field, or implicit authority expansion.
- Keep integrations thin. Domain policy belongs to the owning domain.
- Deployment claims require qualification for the named deployment target.
- Public material must be constructively projected from an immutable qualified private candidate.
