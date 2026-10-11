# Proof-of-Inference receipts

Request logs answer "what happened?" only if you trust whoever runs the database. Receipts make the gateway's record of each request checkable: anyone without the signing key cannot alter or forge one undetected.

A receipt proves what the **gateway recorded**, not that a provider served the response or that it was correct. The exact guarantees and limits are in the [trust model](receipt-spec.md#1-trust-model--what-a-receipt-proves); the byte-level format and test vectors for independent verifiers are in the [draft receipt spec](receipt-spec.md).

## What a receipt attests

```json
{
  "v": 1, "rid": "req_…", "iss": "https://ai-gateway.example.com", "iat": 1791066507,
  "org": "org_…", "team": "team_…", "project": "prj_…", "sub": {"type": "key", "id": "key_…"}, "agent": "",
  "endpoint": "/v1/messages", "model_requested": "sonnet-latest",
  "route": {"model": "claude-sonnet", "provider_id": "prv_…", "provider_kind": "bedrock", "upstream_model": "eu.anthropic.…", "attempts": 2},
  "req_sha256": "…exact bytes received…", "resp_sha256": "…exact bytes returned…",
  "stream": false, "status": 200,
  "policies": [{"id": "grd_…", "name": "Secret Shield", "scope": "org", "mode": "enforce", "digest": "sha256:…"}],
  "actions": [{"policy": "Secret Shield", "rule": "secrets", "action": "redact", "phase": "input", "count": 1}],
  "usage": {"input": 812, "output": 164, "cache_read": 0, "cache_write": 0}, "cost_usd": 0.0049
}
```

- Signed with Ed25519 as a compact JWS (`alg: EdDSA`, `typ: tpx-receipt+jwt`). The public key is published at `/.well-known/theophylax-receipt-keys.json` (RFC 8037 JWK set).
- The key is derived from the master key, or set explicitly with `THEOPHYLAX_RECEIPT_SEED` (32 bytes, base64) so it can live in its own secret. Anyone with the key can sign receipts, so protect it like the master key. Only the current key is published: changing it means older receipts no longer verify against the live key set, so keep a copy of each key set you have used (published key history is on the [roadmap](../ROADMAP.md)).
- Policy `digest` identifies the exact rule set version in force — not just its name.
- **No content**: only digests, identities, routing, counts and cost. Receipts can be given to auditors, customers or a regulator.

## Delivery

- Header `X-Theophylax-Receipt: <request id>` on every response.
- Streaming responses end with an SSE comment `: theophylax-receipt <jws>` (ignored by SSE clients). The response digest covers everything sent before that line, so remove it before hashing a captured stream.
- `GET /v1/receipts/{request_id}` with the same credential returns the JWS, its verification status, and whether the gateway has included it in a checkpoint. That anchoring status is computed by the gateway; receipts do not yet carry an inclusion proof you can check independently.

## Anchoring

Every `THEOPHYLAX_RECEIPT_CHECKPOINT_INTERVAL` (default 2 m) one replica takes the un-anchored receipts in order, computes an RFC 6962-style Merkle root over their hashes and writes it as a `receipts.checkpoint` event into the hash-chained audit log. Deleting or altering a receipt changes the root; altering the audit log breaks the chain (`GET /api/audit/verify`). For external anchoring, export checkpoint roots to a WORM store or transparency log on a schedule.

## Verifying

```bash
theophylax verify-receipt --keys https://ai-gateway.example.com/.well-known/theophylax-receipt-keys.json \
  --request request.json --response response.json @receipt.jws
```

Or without the CLI: `POST /v1/receipts/verify {"jws": "…", "request": "<raw body>", "response": "<raw body>"}`.
Any JOSE library that supports EdDSA can verify the signature.
