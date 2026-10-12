# Roadmap

Public priorities for Theophylax. Dates are intentionally omitted: this is a statement of direction, reviewed in the open. Comment on the linked issues or open a new one to propose changes.

## Now — make the guarantees as strong as the claims

- **Verifiable receipts.** Return Merkle inclusion proofs with each receipt so anchoring can be checked without trusting the gateway; publish key history so older receipts stay verifiable after key changes; sign with a key independent of the master key, with an option to use a KMS/HSM signer. Then stabilise the [receipt format](docs/receipt-spec.md) as v1.
- **Receipt tooling.** Verify captured streams as-is ([#24](../../issues/24)), unit tests for the receipt package ([#25](../../issues/25)), and a standalone verifier library other projects can import.
- **Fail-safe defaults.** Explicit, documented failure behaviour for every control: fail-closed options for external guardrail webhooks and for rate-limit and budget enforcement when shared state is unavailable.
- **API compatibility matrix.** Conformance tests and a published matrix by endpoint, provider and feature (streaming, tool calls, usage, errors, Responses continuation, cancellation), instead of a blanket compatibility claim.

## Next

- **Identity chain.** Record and enforce the chain human → agent → tool → model → data, so a receipt can say which person or workflow was responsible, not only which key.
- **Provider batch offload.** Hand message batches to a provider's own batch service (Anthropic Message Batches, Bedrock batch inference, Vertex batch prediction) when every request routes there, so the provider's batch discount applies; and the OpenAI Batch and Files APIs alongside the Anthropic-shaped one.
- **Per-identity-provider accounts**, removing the global email-uniqueness limitation described below.
- **Independent injection evaluation.** Evaluation sets beyond the synthetic corpus, and a public log of reported bypasses and fixes.

## Later

- **External anchoring** of receipt checkpoints to a transparency log or WORM store.
- **Agent governance**: deeper MCP governance, skill approvals and managed-agent policies, once the core security boundary is reliable and measured.

## Known limitations

- Receipts prove what the gateway recorded. Provider and model identity are gateway-reported, not provider-attested, and anyone holding the signing key can sign receipts — see the [trust model](docs/receipt-spec.md#1-trust-model--what-a-receipt-proves).
- Receipts do not yet carry inclusion proofs; anchoring status is reported by the gateway.
- Fleet-wide rate limits and budgets are a close approximation (counts sync every 500 ms; if the database is unavailable each replica enforces its own counts).
- Prompt-injection results come from a small, synthetic, template-generated corpus and are regression floors, not production rates.
- Email addresses are unique across the whole gateway, so an account created through one organization's identity provider can block a same-email person from being linked by another organization's provider.
