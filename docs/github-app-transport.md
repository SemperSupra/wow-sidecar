# GitHub App transport boundary

Historical source:
- `SemperSupra/agent-dispatch-private@eec035dd40a70c1e6d15136963d86963aa24d626:prototype/sidecar/github_app.py`

WOW Sidecar retains only the reusable GitHub App transport needed by trusted-host authority/control:

- environment-backed App identity and private-key loading;
- GitHub App JWT creation;
- generic JSON HTTP transport with strict object/array shape checks;
- repository-scoped installation discovery;
- repository-scoped installation-token acquisition.

The following historical Agent Dispatch behaviors are intentionally **not** WOW transport:

- GitHub-file workset materialization (`repository_file_json`);
- Agent Dispatch workflow-dispatch construction/execution;
- workflow-run discovery/normalization;
- workset, assignment, projection, candidate, validation, or Jules semantics.

Higher WOW protocol layers may use the generic transport to implement their own bounded Git-data operations. That does not move Agent Dispatch product semantics into this module.

The transport boundary is mechanically guarded by `tests/test_github_app_transport.py`. Public qualification uses only synthetic identities and no GitHub App secret.
