# Recovery capture

The recovery-capture adapter is the first mutating lifecycle primitive, but its mutation is restricted to creating a **new recovery bundle**. It never edits or deletes the source installation.

Contract:

1. require explicitly enumerated absolute regular files;
2. reject symlink sources;
3. refuse an existing destination;
4. copy into a private partial directory using create-once files;
5. fsync files and directory state;
6. hash/size verify the copy against the source-derived manifest;
7. atomically rename the partial bundle into its final destination;
8. retain source files unchanged.

The local manifest may contain source paths because rollback/import needs a restore mapping. The sanitized evidence receipt never contains source paths or file contents; it contains only opaque path tokens, logical names, sizes and SHA-256 digests.

Discovery of historical configuration and referenced credential files remains a separate read-only integration step. This module deliberately does not parse shell configuration, infer files from environment content, stop services, or perform cutover.
