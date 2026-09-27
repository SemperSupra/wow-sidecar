# Core protocol extraction qualification candidate

This branch is a **qualification candidate**, not a public release.

Projection lineage:

- historical admitted source baseline: `SemperSupra/agent-dispatch-private@eec035dd40a70c1e6d15136963d86963aa24d626:prototype/sidecar`;
- private WOW extraction candidate: `SemperSupra/wow-sidecar-private@42f0f67ec6c70c206e0ddd06b37cc5cf5f0a9308`;
- projected scope: request/claim/receipt core, canonical authority reader, read-only receipt observer, minimal GitHub App API client, and exact historical compatibility tests;
- intentional public-safety transformation: private repository locators used only as synthetic test fixtures were replaced with `ExampleOrg/*` fixture identities.

The request/receipt schema constants remain unchanged for compatibility. No Agent Dispatch workset/delegation/Jules code, GARM operator profile, host credentials, site state, or private HIL evidence is included.

Acceptance for promotion to public `main`:

1. public-safety guard passes;
2. compatibility tests pass on public GitHub Actions;
3. private extraction PR remains traceable to the exact historical source;
4. no runtime/cutover claim is made by this source-only qualification.
