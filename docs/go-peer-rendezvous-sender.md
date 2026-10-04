# G6.11d synthetic peer sender

Authority: `SemperSupra/wow-sidecar-private#81`.

This slice adds a small sender for the synthetic G6.11c handler so the complete
claim/complete transport can be exercised through an in-process HTTP server.

The sender is explicitly given:
- its source embodiment card;
- one destination node ID;
- file-backed pairwise credentials;
- an exact endpoint URL;
- exact authority reference/revision/state;
- one strict claim or complete payload;
- one request ID and clock.

It signs the exact payload with the G6.11a envelope and performs exactly one
HTTP POST. It contains no automatic retry, discovery, queue, wake, DNS/overlay
selection, relay policy, or endpoint registry.

Responses are size bounded and strictly decoded.

This is still a synthetic transport primitive. Neither the sender nor the
G6.11c handler is wired into daemon configuration or a product route.

Live/runtime composition remains blocked on independent security validation
#78 and a subsequent explicit #72 release.
