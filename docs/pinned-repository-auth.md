# Pinned repository authentication boundary

The generic WOW worker must be self-contained in its GitHub App trust boundary. It must not depend on a host user's `gh auth` state, SSH agent, SSH key, or credential-helper configuration.

## Repository-scoped installation token

For each exact configured operator repository, the worker:

1. resolves the GitHub App installation associated with that repository;
2. requests an installation token restricted by the GitHub API request body to that single repository name;
3. creates an ephemeral `GIT_ASKPASS` helper that contains **no token value**;
4. carries the token only in the subprocess environment used for that bounded checkout/operator lifetime;
5. disables ambient system/global Git configuration, terminal prompts, SSH-agent state, and inherited Git authentication variables;
6. uses only HTTPS `git` operations against the configured `owner/name` repository;
7. verifies canonical `main` when the profile requires it, then checks out and verifies the exact 40-hex revision and clean state;
8. executes only the configured normalized non-symlink operator path.

The exact trusted operator inherits the same repository-scoped Git transport so it can read/write that one repository when its code requires it. The control request cannot select a repository, revision, path, command, environment, or token.

The token value is never written to the repository, askpass file, receipt, or durable evidence. Errors are sanitized.

## Container implication

The generic App still needs `bash` and `git`; it no longer needs GitHub CLI or a mounted `gh` login. Concrete integrations that require stronger host privilege or additional tools remain separately qualified integration policy and do not broaden WOW core.
