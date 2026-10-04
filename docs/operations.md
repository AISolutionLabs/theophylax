# Configuration and operations

## Environment

| Variable | Default | Purpose |
|---|---|---|
| `THEOPHYLAX_DATABASE_URL` | — | PostgreSQL 14+ connection string |
| `THEOPHYLAX_MASTER_KEY` | — (required) | 32 bytes hex/base64. Encrypts provider credentials, IdP secrets and MCP tokens; derives session, redaction and receipt keys. Back it up: losing it makes stored credentials unreadable. |
| `THEOPHYLAX_PUBLIC_URL` | `http://localhost:8080` | External URL (SSO redirects, receipts issuer) |
| `THEOPHYLAX_ADDR` | `:8080` | Listen address |
| `THEOPHYLAX_BOOTSTRAP_ADMIN_EMAIL` / `_PASSWORD` / `THEOPHYLAX_BOOTSTRAP_ORG` | — | First platform admin and org, created only when no users exist |
| `THEOPHYLAX_SEED_DEMO` | `false` | Mock provider, demo model and marketplace content |
| `THEOPHYLAX_LOCAL_LOGIN` | `true` | Password login (turn off once SSO works) |
| `THEOPHYLAX_LOG_RETENTION_DAYS` | `90` | Request logs and payloads |
| `THEOPHYLAX_STDOUT_REQUEST_LOGS` | `true` | One JSON line per request |
| `THEOPHYLAX_UPSTREAM_TIMEOUT` | `10m` | Per-provider override in provider options |
| `THEOPHYLAX_MAX_BODY_MB` | `32` | Request size limit |
| `THEOPHYLAX_KEY_CACHE_TTL` | `30s` | Revocation propagation across replicas |
| `THEOPHYLAX_DB_MAX_CONNS` | `20` | Pool size per replica |
| `THEOPHYLAX_RECEIPT_SEED` | derived | Explicit Ed25519 seed (base64) |
| `THEOPHYLAX_RECEIPT_CHECKPOINT_INTERVAL` | `2m` | Merkle anchoring interval |
| `THEOPHYLAX_TRUST_PROXY_HEADERS` | `true` | Use `X-Forwarded-For` for client IPs |
| `THEOPHYLAX_PRICING_URL` | LiteLLM catalog on GitHub | Upstream price catalog (`https://…`, `file:///path` for an air-gapped mirror, or `off` to use only the stored copy) |
| `THEOPHYLAX_PRICING_REFRESH` | `6h` | How often the price catalog is re-fetched |
| `HTTPS_PROXY`, `NO_PROXY`, `SSL_CERT_FILE` | | Standard egress settings; per-provider `proxy=` and `ca_bundle=` override |

## Endpoints

`/healthz` (liveness), `/readyz` (DB + config), `/metrics` (Prometheus): `theophylax_requests_total{model,provider,status}`, `theophylax_request_duration_seconds`, `theophylax_ttfb_seconds`, `theophylax_gateway_overhead_seconds`, `theophylax_tokens_total{type}`, `theophylax_cost_usd_total`, `theophylax_upstream_errors_total`, `theophylax_guardrail_actions_total`, `theophylax_log_dropped_total`, `theophylax_log_queue_depth`.

## Load balancers and streaming

Disable proxy buffering and allow long idle times (≥ 10 minutes) for SSE: ingress-nginx `proxy-buffering: off`, `proxy-read-timeout: 900`; AWS ALB `idle_timeout.timeout_seconds=900`. Pods drain in-flight streams for up to 60 s on shutdown (`terminationGracePeriodSeconds: 90`, `preStop` sleep).

## Performance

On 2 shared vCPUs with Postgres, the load generator and a zero-latency mock upstream on the same host, with Secret Shield, PII and injection guardrails, receipts and logging all enabled: ~2,300 req/s sustained, ~1 ms p50 end to end at low concurrency, gateway processing under 1 ms for 94% of requests, ~37 MB RSS. Scale horizontally; replicas share nothing but Postgres.

## Backups and keys

Back up Postgres (logs, receipts and audit are your evidence) and the master key separately. To rotate the master key, re-enter provider and IdP credentials after switching. To rotate the receipt key, set a new `THEOPHYLAX_RECEIPT_SEED` and keep publishing the old public key alongside for verification of older receipts.

## Security notes

- Credentials are encrypted at rest (AES-256-GCM); secrets are never returned by the API.
- Session cookies are HttpOnly, SameSite=Lax and Secure on HTTPS; cookie-authenticated mutations require a custom header (CSRF).
- The console sends a strict Content-Security-Policy; containers run non-root with a read-only root filesystem.
- Every control-plane change, sign-in (including failures), payload view, SCIM action and rollout decision is in the hash-chained audit log.

## Pricing

Spend, dollar budgets, receipts and the **Billing** page use the upstream providers' published list prices, never admin-entered values. At startup and every `THEOPHYLAX_PRICING_REFRESH`, the gateway fetches a machine-readable catalog ([LiteLLM's `model_prices_and_context_window.json`](https://github.com/BerriAI/litellm/blob/main/model_prices_and_context_window.json) by default) and stores it in Postgres. Replicas without internet egress, and restarts, use the stored copy. If a refresh fails, the previous prices stay in use and the error appears under Billing → Price source. Platform admins can force a refresh there or with `POST /api/pricing/refresh`.

Each request is priced when it completes, using the provider kind and upstream model ID of the deployment that served it (after failover). Each token type is billed at its own catalog rate, and long-context tiers apply when the prompt crosses their threshold. The matched catalog entry is recorded on the request log as `price_key`. Requests whose model is not in the catalog are recorded at $0 with an empty `price_key`, and Billing lists them as unpriced. Costs already recorded are not recalculated when prices change later.

## Budgets

Budgets are either **pooled** (one cap on the combined spend of an org, team, project or key) or **per-person**. A person's own limit for each period (daily, monthly, all time) is the first of these that exists:

1. their personal budget (`scope_type=user`), measured on their spend in the organization;
2. their team's per-person budget (`scope_type=team`, `per_user=true`), measured on their spend in that team;
3. the organization's per-person budget (`scope_type=org`, `per_user=true`), measured on their spend in the organization.

Pooled budgets still apply on top, so an organization total still stops everyone. Requests made with service keys that belong to no person are only limited by pooled budgets.
