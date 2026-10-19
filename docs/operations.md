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
| `THEOPHYLAX_BATCH_WORKERS` | `4` | [Message batch](api.md#message-batches) requests this replica runs at once; `0` disables batch processing on it |
| `THEOPHYLAX_BATCH_MAX_MB` | `64` | Size limit for one message batch |
| `THEOPHYLAX_KEY_CACHE_TTL` | `30s` | Revocation propagation across replicas |
| `THEOPHYLAX_DB_MAX_CONNS` | `20` | Pool size per replica |
| `THEOPHYLAX_RECEIPT_SEED` | derived | Explicit Ed25519 seed (base64) |
| `THEOPHYLAX_RECEIPT_CHECKPOINT_INTERVAL` | `2m` | Merkle anchoring interval |
| `THEOPHYLAX_TRUST_PROXY_HEADERS` | `true` | Use `X-Forwarded-For` for client IPs |
| `THEOPHYLAX_PRICING_URL` | LiteLLM catalog on GitHub | Upstream price catalog (`https://…`, `file:///path` for an air-gapped mirror, or `off` to use only the stored copy) |
| `THEOPHYLAX_SMTP_URL` | — | SMTP server for email alerts: `smtp://user:pass@host:587?from=alerts@example.com` (STARTTLS) or `smtps://…:465` (implicit TLS). Unset disables email channels |
| `THEOPHYLAX_PRICING_REFRESH` | `6h` | How often the price catalog is re-fetched |
| `THEOPHYLAX_RATE_LIMIT_SYNC` | `500ms` | How often replicas share RPM/TPM counts through Postgres; `0` makes limits per replica |
| `THEOPHYLAX_MODEL_WATCH_INTERVAL` | `6h` | How often connected providers are checked for new models; `0` disables |
| `THEOPHYLAX_OTLP_ENDPOINT` / `THEOPHYLAX_OTLP_HEADERS` | — | OTLP/HTTP trace export, e.g. `http://alloy:4318` or `https://cloud.langfuse.com/api/public/otel` with `Authorization=Basic ${env:LANGFUSE_AUTH}` (`OTEL_EXPORTER_OTLP_*` also honoured) |
| `THEOPHYLAX_LOG_SINK_URL` / `_FORMAT` / `_HEADERS` / `_AUDIT` | — / `json` / — / `true` | Stream request logs (metadata only) and the audit log: `json`, `ndjson`, `splunk_hec`, `elastic_bulk` |
| `HTTPS_PROXY`, `NO_PROXY`, `SSL_CERT_FILE` | | Standard egress settings; per-provider `proxy=` and `ca_bundle=` override |

## Endpoints

`/healthz` (liveness), `/readyz` (DB + config), `/metrics` (Prometheus): `theophylax_requests_total{model,provider,status}`, `theophylax_request_duration_seconds`, `theophylax_ttfb_seconds`, `theophylax_gateway_overhead_seconds`, `theophylax_tokens_total{type}`, `theophylax_cost_usd_total`, `theophylax_upstream_errors_total`, `theophylax_guardrail_actions_total`, `theophylax_log_dropped_total`, `theophylax_log_queue_depth`.

## Load balancers and streaming

Disable proxy buffering and allow long idle times (≥ 10 minutes) for SSE: ingress-nginx `proxy-buffering: off`, `proxy-read-timeout: 900`; AWS ALB `idle_timeout.timeout_seconds=900`. Pods drain in-flight streams for up to 60 s on shutdown (`terminationGracePeriodSeconds: 90`, `preStop` sleep).

## Performance

On a shared 2-vCPU VM with Postgres, the load generator and a zero-latency mock upstream on the same host: about 1,450 req/s with Secret Shield, PII and injection guardrails, receipts and logging enabled (p50 5.9 ms, p99 24 ms at 10 concurrent), about 2,800 req/s with guardrails off, ~37 MB RSS, ready in under 0.1 s on restart. Comparisons with LiteLLM and Portkey and the harness to reproduce them are in [bench/README.md](../bench/README.md). Scale horizontally; replicas share nothing but Postgres.

## Telemetry export

Set `THEOPHYLAX_OTLP_ENDPOINT` to send one span per request using the OpenTelemetry GenAI conventions (`gen_ai.provider.name`, `gen_ai.request.model`, `gen_ai.usage.*`) plus Theophylax attributes (org, team, project, key, cost, guardrail actions, receipt hash). A client's `traceparent` header makes the span a child of the caller's trace. Set `THEOPHYLAX_LOG_SINK_URL` to stream request metadata and every audit event to a SIEM. Both are batched and retried off the request path; if a destination is down, events are dropped and counted in `theophylax_export_dropped_total` rather than slowing traffic. Prompt and response content is never exported.

## Model watch

Every `THEOPHYLAX_MODEL_WATCH_INTERVAL` one replica lists the models each connected provider serves. The first scan is a silent baseline; afterwards, newly available models appear on **Models & routing** for platform admins, with catalog price, context window and capabilities, and can be added or trialled as a rollout in one click. Models whose catalog entry has a retirement date within 90 days (or past) are flagged. Every scan and change is in the audit log (`model_watch.*`), so it also reaches your SIEM. Providers without a listing API (Vertex, Foundry) are skipped.

## Migrating from LiteLLM

`theophylax import-litellm` runs with the gateway's own environment (`THEOPHYLAX_DATABASE_URL`, `THEOPHYLAX_MASTER_KEY`) and writes everything in one transaction:

| LiteLLM | Theophylax |
|---|---|
| `model_list` entries (`config.yaml` or `LiteLLM_ProxyModelTable`) | providers (de-duplicated by endpoint and credentials), models, weighted deployments |
| `router_settings.model_group_alias` | model aliases |
| organizations | organizations |
| teams (models, RPM/TPM, budget) | teams with settings and a *LiteLLM keys* project, team budgets |
| users and team/org roles | users (sign in with SSO or after a reset) and memberships |
| keys (hash, alias, models, RPM/TPM, expiry, blocked) | gateway keys with the same hash, so `sk-…` keys keep working; key budgets |

`os.environ/NAME` becomes `${env:NAME}`. Models LiteLLM stored in its database are encrypted with its salt key (`LITELLM_SALT_KEY`, or the master key if no salt key was set); pass it with `-litellm-salt-key`. Costs are not copied: each request is priced from the provider's list price for the upstream model. Spend history stays in key metadata.

## Backups and keys

Back up Postgres (logs, receipts and audit are your evidence) and the master key separately. To rotate the master key, re-enter provider and IdP credentials after switching. To rotate the receipt key, set a new `THEOPHYLAX_RECEIPT_SEED` and keep publishing the old public key alongside for verification of older receipts.

## Recovering a locked-out account

If an admin loses their password, reset it from wherever the gateway runs. The command talks to Postgres directly, so nobody needs to be signed in:

```bash
# Generates a new password and prints it on stdout
theophylax reset-password -email admin@example.com

# Or set one yourself (at least 12 characters)
echo 'new-password-here' | theophylax reset-password -email admin@example.com -password-stdin

# Kubernetes / Compose
kubectl exec -n theophylax deploy/theophylax -- theophylax reset-password -email admin@example.com
docker compose exec theophylax theophylax reset-password -email admin@example.com
```

It needs `THEOPHYLAX_DATABASE_URL` (already set inside the gateway's container). It reactivates a suspended account unless you pass `-keep-status`, and `-platform-admin` also restores platform-admin rights if the last admin was demoted. Every reset is written to the audit log as `auth.password_reset`. Existing browser sessions stay valid until they expire; rotate the session signing key if you need to end them all.

## Security notes

- Credentials are encrypted at rest (AES-256-GCM); secrets are never returned by the API.
- Session cookies are HttpOnly, SameSite=Lax and Secure on HTTPS; cookie-authenticated mutations require a custom header (CSRF).
- The console sends a strict Content-Security-Policy; containers run non-root with a read-only root filesystem.
- Every control-plane change, sign-in (including failures), payload view, SCIM action and rollout decision is in the hash-chained audit log.

## Pricing

Spend, dollar budgets, receipts and the **Billing** page use the upstream providers' published list prices, never admin-entered values. At startup and every `THEOPHYLAX_PRICING_REFRESH`, the gateway fetches a machine-readable catalog ([LiteLLM's `model_prices_and_context_window.json`](https://github.com/BerriAI/litellm/blob/main/model_prices_and_context_window.json) by default) and stores it in Postgres. Replicas without internet egress, and restarts, use the stored copy. If a refresh fails, the previous prices stay in use and the error appears under Billing → Price source. Platform admins can force a refresh there or with `POST /api/pricing/refresh`.

Each request is priced when it completes, using the provider kind and upstream model ID of the deployment that served it (after failover). Each token type is billed at its own catalog rate, and long-context tiers apply when the prompt crosses their threshold. Claude fast mode (`speed: "fast"`, reported as `usage.speed`) is billed at the catalog's fast multiplier on every rate; Azure AI Foundry deployments use Anthropic's multiplier, and an entry with no multiplier is billed at standard rates. Service tiers (OpenAI `service_tier`) and data-residency multipliers are not yet applied. The matched catalog entry is recorded on the request log as `price_key`. Requests whose model is not in the catalog are recorded at $0 with an empty `price_key`, and Billing lists them as unpriced. Costs already recorded are not recalculated when prices change later.

## Alerts

Org admins add alert channels under **Settings → Alerts** (or `POST /api/orgs/{org}/notifications`): Slack incoming webhooks, Microsoft Teams workflow webhooks, generic webhooks and email. Each channel receives every alert, or only the types it subscribes to:

| Event | When |
|---|---|
| `budget.threshold` | A budget passes its alert threshold (`alert_pct`, default 80%) — once per budget per period |
| `budget.exhausted` | A budget reaches 100% — once per budget per period; the message says whether requests are now blocked |
| `guardrail.blocked` | A guardrail policy blocked requests — one summary per policy per 5-minute window, with the count and the latest rule and detail |

Alerts are evaluated every 30 seconds on every replica and claimed in the database before sending, so each is delivered once across the fleet. They never include prompt or response content. Per-person default budgets are not alerted on yet, because they have no single figure to compare.

Channel URLs are stored encrypted, shown only as their host, and fetched through the same SSRF guard as other admin-configured URLs. **Send test** (`POST /api/notifications/{id}/test`) checks delivery.

Generic webhooks receive the event as JSON (`event`, `org_id`, `title`, `text`, `link`, `fields`, `ts`) with an `X-Theophylax-Event` header. With a signing secret, each request also carries `X-Theophylax-Timestamp` and `X-Theophylax-Signature: sha256=<hex HMAC-SHA256 of "<timestamp>.<body>">`; verify the signature and reject old timestamps to prevent replays.

## Response cache

Identical requests can be answered from a cache instead of calling the provider again — no latency, no cost. It is **off by default**. Turn it on per organization, team or project with `cache_ttl_seconds` (console: Settings → Organization defaults, or a team or project's settings; up to 7 days). The most specific scope that sets it wins, and `-1` turns caching off for that scope.

- **What matches:** the organization and project, the deployment and upstream model, streaming or not, the exact request as sent upstream (after guardrail redaction and managed-agent overlays), and the `anthropic-version` and `anthropic-beta` headers. Entries are never shared across organizations or projects.
- **How hits are served:** the stored upstream response is replayed through the normal pipeline, so streaming, format translation, output guardrails (re-evaluated with the current policies), Secret Shield restoration, usage and receipts all behave as for a live call. Because the upstream only ever saw placeholders, cached entries contain no secrets, and they are encrypted with the master key.
- **Accounting:** a hit is logged with `price_key = "cache"` and $0 cost, does not count against budgets or token rate limits, and its receipt carries `"route": {"cache": "hit"}`.
- **What is stored:** only complete, successful responses up to 4 MiB. Requests to rollout canaries are not cached. Supported on `/v1/messages`, `/v1/chat/completions` and managed-agent endpoints.
- **Per request:** responses carry `X-Theophylax-Cache: hit | miss | refresh` when caching applies. Clients can send `X-Theophylax-Cache: off` to bypass the cache or `refresh` to skip the lookup and store a fresh answer.
- **Clearing:** `DELETE /api/orgs/{org}/cache` (optionally `?project_id=…`), or **Clear cache** in the console. Entries are not invalidated when a provider's configuration changes; clear the cache if that matters. Expired entries are removed every few minutes. Metric: `theophylax_cache_total{result}`.

Caching returns the same answer for the same request, which is usually what you want for evaluation suites, CI, retrieval pipelines and repeated agent steps, but not where you rely on sampling variety; leave it off (or send `off`) for those.

## Budgets

Budgets are either **pooled** (one cap on the combined spend of an org, team, project or key) or **per-person**. A person's own limit for each period (daily, monthly, all time) is the first of these that exists:

1. their personal budget (`scope_type=user`), measured on their spend in the organization;
2. their team's per-person budget (`scope_type=team`, `per_user=true`), measured on their spend in that team;
3. the organization's per-person budget (`scope_type=org`, `per_user=true`), measured on their spend in the organization.

Pooled budgets still apply on top, so an organization total still stops everyone. Requests made with service keys that belong to no person are only limited by pooled budgets.
