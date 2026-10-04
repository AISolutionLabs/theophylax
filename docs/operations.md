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
| `HTTPS_PROXY`, `NO_PROXY`, `SSL_CERT_FILE` | | Standard egress settings; per-provider `proxy=` and `ca_bundle=` override |

## Endpoints

`/healthz` (liveness), `/readyz` (DB + config), `/metrics` (Prometheus): `theophylax_requests_total{model,provider,status}`, `theophylax_request_duration_seconds`, `theophylax_ttfb_seconds`, `theophylax_gateway_overhead_seconds`, `theophylax_tokens_total{type}`, `theophylax_cost_usd_total`, `theophylax_upstream_errors_total`, `theophylax_guardrail_actions_total`, `theophylax_log_dropped_total`, `theophylax_log_queue_depth`.

## Load balancers and streaming

Disable proxy buffering and allow long idle times (≥ 10 minutes) for SSE: ingress-nginx `proxy-buffering: off`, `proxy-read-timeout: 900`; AWS ALB `idle_timeout.timeout_seconds=900`. Pods drain in-flight streams for up to 60 s on shutdown (`terminationGracePeriodSeconds: 90`, `preStop` sleep).

## Performance

On 2 shared vCPUs with Postgres, the load generator and a zero-latency mock upstream on the same host, with Secret Shield, PII and injection guardrails, receipts and logging all enabled: ~2,300 req/s sustained, ~1 ms p50 end to end at low concurrency, gateway processing under 1 ms for 94% of requests, ~37 MB RSS. Scale horizontally; replicas share nothing but Postgres.

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
