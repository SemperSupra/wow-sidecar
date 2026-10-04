# G6.11b peer credential admission

Authority: `SemperSupra/wow-sidecar-private#74`.

WOW peer authentication uses pairwise secret material loaded from a local file,
not from environment values, cards, DLE records, receipts, or command lines.

The credential document is strict JSON:

```json
{
  "schema": "wow-sidecar.peer-credentials.v1",
  "peers": [
    {
      "node_id": "example-peer-node",
      "key": "<standard-base64-encoded 32-128 byte pairwise secret>"
    }
  ]
}
```

The loader requires an absolute regular non-symlink file, rejects group/world
permission bits, bounds the file and peer count, rejects unknown/trailing JSON,
requires unique valid node IDs, and returns copies of decoded keys.

A future runtime adapter may accept only a path such as:

`WOW_PEER_CREDENTIALS_FILE=/run/secrets/wow-peer-credentials.json`

The secret value itself must not be placed in an environment variable.

This primitive does not generate, distribute, rotate, publish, log, or persist
keys beyond reading the explicitly configured local file. It adds no endpoint
or network mutation surface.
