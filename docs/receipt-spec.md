# Proof-of-Inference receipt format — draft v0

**Status: draft, not stable.** This describes the format Theophylax emits today so that independent verifiers can be built and tested against [test vectors](../internal/receipts/testdata/vectors.json). Expect breaking changes before v1: Merkle inclusion proofs, published key history and a signing key independent of the gateway's master key are planned (see [ROADMAP.md](../ROADMAP.md)). Feedback on the format is welcome in GitHub issues.

The key words MUST, SHOULD and MAY are used as in RFC 2119.

## 1. Trust model — what a receipt proves

A receipt is a statement **signed by the gateway** about a request it handled. Verifying it establishes:

| Part | What a valid receipt establishes | What it does not establish |
|---|---|---|
| Signature | A holder of the gateway's signing key signed exactly this payload. | That the gateway was not compromised, or that its key was not misused. |
| `req_sha256` / `resp_sha256` | The bytes you hold are the bytes the gateway recorded receiving and returning. | That the response is correct, safe or true. |
| `policies` / `actions` | Which policy versions the gateway says were in force and what it says they did. | That the policies were well designed, or that monitor-mode findings were enforced. |
| `route` | Which provider, upstream model and number of attempts the gateway says it used. | That the provider actually served the response. Provider identity is gateway-reported, not provider-attested. |
| Anchoring | — (v0) The gateway reports whether the receipt is included in a checkpoint, but v0 receipts carry no inclusion proof, so this is not independently verifiable yet. | — |

In v0 the signing key is derived from the gateway's master key unless `THEOPHYLAX_RECEIPT_SEED` sets it explicitly. Anyone holding that key — including an operator with access to the master key — can mint receipts that verify. Receipts make tampering by anyone *without* the key detectable; they do not protect against a fully compromised gateway. External anchoring of checkpoints (for example to a transparency log) and protected signing keys make such an attack harder to conceal.

## 2. Encoding

A receipt is a JWS in compact serialization (RFC 7515): `BASE64URL(header) "." BASE64URL(payload) "." BASE64URL(signature)`, base64url without padding.

- Header: `{"alg":"EdDSA","kid":"<kid>","typ":"tpx-receipt+jwt"}`.
- `alg` MUST be `EdDSA` (Ed25519, RFC 8032 / RFC 8037). Verifiers MUST reject any other value, including `none`.
- The signature covers the ASCII bytes `BASE64URL(header) "." BASE64URL(payload)` exactly as transmitted. Verifiers MUST verify over the received segments and MUST NOT re-serialize the payload before verifying; no JSON canonicalization is involved.

## 3. Keys

- Public keys are published as a JWK set (RFC 8037: `kty: "OKP"`, `crv: "Ed25519"`, `x`) at `/.well-known/theophylax-receipt-keys.json`.
- `kid` is `"tpx-"` followed by the first 6 bytes of SHA-256(public key), hex-encoded.
- Verifiers MUST select the key by `kid` and MUST reject receipts whose `kid` is not in the key set they trust.
- v0 publishes only the current key. Changing the key replaces it, so verifiers that need to check older receipts SHOULD keep a copy of each key set they have trusted.

## 4. Payload

A JSON object with these members (types as in the example in [receipts.md](receipts.md)):

| Member | Meaning |
|---|---|
| `v` | Format version. `1` for receipts described by this draft. |
| `rid` | Gateway request ID. |
| `iss` | Gateway public URL. |
| `iat` | Issue time, Unix seconds. |
| `org`, `team`, `project` | Tenancy of the caller (`team` and `project` may be absent). |
| `sub` | Caller: `{"type": "key" \| "user", "id": …}`. |
| `agent` | Managed agent ID, if any. |
| `endpoint` | Request path, e.g. `/v1/messages`. |
| `model_requested` | Model name the client asked for. |
| `route` | `{model, provider_id, provider_kind, upstream_model, attempts, cache}` as recorded by the gateway. `cache` is `"hit"` when the response was served from the response cache (no provider call) and absent otherwise. `router` (`{model, score, classifier, tier}`) is present when a complexity router chose `model`; `router.model` is the router the client asked for. |
| `req_sha256` | Hex SHA-256 of the request body (§5). |
| `resp_sha256` | Hex SHA-256 of the response body (§5). |
| `stream` | Whether the response was streamed. |
| `status` | HTTP status returned to the client. |
| `policies` | Guardrail policies in force: `{id, name, scope, mode, digest}`; `digest` identifies the exact rule set version. |
| `actions` | What policies did: `{policy, rule, action, phase, count}`. |
| `usage`, `cost_usd` | Metered tokens and cost. |

Receipts contain no prompt or response content. Verifiers MUST ignore members they do not recognise.

## 5. Body digests

- `req_sha256` is computed over the request body bytes exactly as the gateway received them. For OpenAI Responses requests bridged to another format, it covers the client's original Responses request.
- `resp_sha256` is computed over every response body byte the gateway sent to the client.
- **Streaming:** the gateway ends a stream with an SSE comment line `: theophylax-receipt <jws>`. That line and anything after it are **not** included in `resp_sha256`. To verify a captured stream, remove the final receipt comment line before hashing.

## 6. Checkpoints and anchoring

At intervals the gateway groups un-anchored receipts into a checkpoint:

- Leaves, in order of `(issue time, rid)`: `leaf_i = SHA-256(JWS_i)` over the compact JWS string.
- Tree: RFC 6962-style. Leaf node `H(0x00 || leaf_i)`, interior node `H(0x01 || left || right)`; a node without a sibling is promoted to the next level unchanged.
- The root is written as a `receipts.checkpoint` event into the gateway's hash-chained audit log.

v0 does not return an inclusion proof with the receipt, so a third party cannot yet verify that a receipt belongs to a checkpoint without trusting the gateway's report or obtaining all leaves of the checkpoint.

## 7. Verifying

1. Split the JWS into three segments; decode the header; require `alg = EdDSA` and a trusted `kid`.
2. Verify the Ed25519 signature over `header "." payload` (§2).
3. Decode the payload.
4. If you hold the bodies, compare SHA-256 of the request and response bytes (with the stream trailer removed, §5) to `req_sha256` and `resp_sha256`.
5. Interpret the result using the trust model in §1.

The reference verifier is `theophylax verify-receipt`; `POST /v1/receipts/verify` performs the same checks server-side.

## Test vectors

[`internal/receipts/testdata/vectors.json`](../internal/receipts/testdata/vectors.json) contains a published test signing seed and its key set, valid receipts with their request and response bodies (non-streaming, streaming and an upstream error), invalid receipts that MUST be rejected (modified payload, unknown key, `alg: none`), and a Merkle root over the valid receipts' leaves. The seed is for testing only and must never be used to sign real receipts.
