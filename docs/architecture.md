# Architecture

```
            Claude Code · SDKs · agents · console
                         │  Anthropic /v1/messages · OpenAI /v1/chat/completions
                         ▼
 ┌──────────────────────────────── Theophylax replica (stateless) ───────────────────────────────┐
 │  auth ─► agent overlay ─► model resolve ─► allow-lists ─► rate limits ─► budgets               │
 │      ─► input guardrails (Secret Shield · PII · injection · policy) ─► route & failover        │
 │      ─► provider adapter (Anthropic · Bedrock SigV4 · Azure Entra · OpenAI) ─► translate ◄─┐   │
 │      ─► output guardrails / stream guard ─► restore secrets ─► client                     │   │
 │      ─► receipt (Ed25519 JWS) ─► async log batch (COPY) ─► usage counters                 │   │
 │  in-memory config snapshot  ◄── Postgres LISTEN/NOTIFY + 30s safety reload                │   │
 │  shadow / canary rollouts ── judge model (routed through the same gateway) ──────────────┘   │
 └─────────────────────────────────────────────┬─────────────────────────────────────────────────┘
                                               │
                                     PostgreSQL (state, logs, receipts, audit)
```

## Hot path

- **No database reads for configuration.** Models, deployments, providers (with live HTTP connection pools), guardrail policies, budgets, scopes, agents and approved registry content live in an immutable snapshot swapped atomically. Admin changes publish `NOTIFY theophylax_config`; every replica reloads within ~100 ms.
- **API keys** are SHA-256 hashed (they carry 200 bits of entropy, so slow hashing would only add latency) and cached for `THEOPHYLAX_KEY_CACHE_TTL` (default 30 s). Revocation on the replica that handled it is immediate; others converge within the TTL.
- **Logging never blocks.** Completed requests are queued and written in batches with `COPY`, together with usage-counter upserts and receipts, in one transaction. If the queue is full, entries are dropped and counted in `theophylax_log_dropped_total`; JSON stdout logs remain the second copy.
- **Streaming** is passed through event by event with flushes; only the events needed for accounting (`message_start`, `message_delta`, usage chunks) are parsed unless a guardrail, Secret Shield restore, payload capture or shadow evaluation needs the text.
- **Failover** happens only before the first byte reaches the client. Retryable statuses (408/409/425/429/5xx/529) and network errors move to the next deployment; failing deployments cool down exponentially (2 s → 64 s) and healthy ones are preferred.

## Data model

`orgs → teams → projects`; `memberships(user, scope, role)` with inheritance (org roles apply to every team/project, team roles to their projects). `api_keys` belong to a project (gateway keys) or to a user (personal access tokens). `providers` may be platform-wide or org-owned; `deployments` map a public `model` to `(provider, upstream model)`. `guardrail_policies` and `budgets` attach to scopes. `request_logs`, `request_payloads`, `receipts` and `receipt_checkpoints` are append-mostly; `audit_log` is hash-chained.

## Scaling

Replicas are stateless; scale on CPU. Rate limits are per replica (divide by replica count, or set them at the project/team level where the error is averaged out). Budgets are global: each replica adds its own pending spend to the shared totals refreshed every 5 s. For very high log volumes, partition `request_logs` by month (the BRIN index on `ts` already keeps time-range scans cheap) and tune `THEOPHYLAX_LOG_RETENTION_DAYS`.
