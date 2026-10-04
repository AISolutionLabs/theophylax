# Theophylax

**An enterprise AI gateway where every model call is governed — and provable.**

Theophylax (Greek *theos* "god" + *phylax* "guardian": a divine guardian) sits between your people, apps and agents and the models they use — Anthropic, AWS Bedrock, Azure AI Foundry, Azure OpenAI and any OpenAI-compatible endpoint. It speaks both the Anthropic Messages API and the OpenAI Chat Completions API, so Claude Code, the Anthropic and OpenAI SDKs and existing tools work unchanged.

It is a single 13 MB Go binary (two external dependencies: the pgx Postgres driver and golang.org/x/text) with an embedded console, backed by PostgreSQL.

## What makes it different

| | |
|---|---|
| **Proof-of-Inference receipts** | Every request gets an Ed25519-signed receipt (standard JWS) attesting who called, SHA-256 digests of the exact request and response bytes, the model and provider that actually served it (including failover), the guardrail policy *versions* in force and what they did, and tokens/cost. Receipts contain no prompt content, are batch-anchored as Merkle roots in a hash-chained audit log, and verify offline with a published key. Logs can be edited; receipts can be checked. |
| **Secret Shield** | Cloud keys, tokens, private keys, passwords and connection strings are replaced with stable placeholders before any model sees them — in prompts, system prompts, tool results and earlier tool calls. Optional *restore on return* puts the real values back into the response (streams included), so coding agents keep working while the model never sees a secret. |
| **Native prompt-injection defence** | Multi-layer, in-process detection: Unicode/homoglyph/leetspeak de-obfuscation, invisible "tag character" smuggling, base64/hex/rot13 payloads, multilingual attacks, and indirect injection via tool results scored as untrusted. Optional judge-model second opinion, canary tokens for system-prompt extraction, exfiltration-link blocking and a tool-call taint guard. |
| **Eval-gated model rollouts** | Shadow new model versions on live traffic, have a judge model compare answers (position-randomised), then canary and auto-promote or roll back against your thresholds. Clients never change the model name they call. |

## Everything else you'd expect

- **Providers & auth to them** — Anthropic (API key / bearer), Bedrock (IRSA, EKS Pod Identity, instance profile, assume-role, static keys, Bedrock API keys, PrivateLink endpoints), Azure AI Foundry and Azure OpenAI (API key, Entra client secret, workload identity federation, managed identity), OpenAI-compatible (vLLM, Ollama…), and **custom credential providers** (Basic or Bearer auth with automatic access/refresh-token handling; models are named `<model>@<credential>`). **OAuth 2.0** for Anthropic, OpenAI-compatible and custom providers: client credentials, or authorization code where an admin connects the provider once in the console. Per-connection egress proxy and CA bundle for TLS-inspecting proxies. Configure with a guided form or a **connection string** (`bedrock://eu-west-1?auth=irsa`, `Provider=AzureOpenAI;Endpoint=…;Auth=Entra;…`) with `${env:…}` / `${file:…}` secret references.
- **Routing** — model catalog with aliases and wildcards, priority/weighted/latency strategies, automatic failover with circuit-breaking cooldowns, day-0 model discovery from Anthropic, Bedrock (incl. inference profiles) and OpenAI-compatible endpoints.
- **Format translation** — Anthropic ⇄ OpenAI in both directions, streaming and tools included; Bedrock event-stream converted to SSE.
- **Identity** — local admin bootstrap, OIDC SSO (Entra ID, Okta, Google, Auth0, Keycloak, Ping…) with PKCE, JIT provisioning and group → role mappings at org/team/project level, **SCIM 2.0** users and groups, and IdP access tokens accepted directly as gateway credentials (e.g. via Claude Code's `apiKeyHelper`).
- **Tenancy & RBAC** — Organizations → Teams → Projects, with owner/admin/member/viewer roles inherited downwards. Owners exist at org and team level. Gateway keys belong to projects; personal access tokens drive the admin API.
- **Guardrails** — pre-call and post-call (streams terminated mid-flight), layered org → team → project, enforce or monitor mode: Secret Shield, PII, prompt injection, keyword/regex deny-lists, model allow-lists, token caps, input size, external webhooks, AWS Bedrock Guardrails. Dry-run console.
- **Limits & budgets** — RPM/TPM per key, project, team and org; daily/monthly/total budgets in dollars or tokens per org, team, project, key or person, enforced across replicas. Per-person limits resolve in order: the person's own budget, then their team's per-person budget, then the organization's per-person budget.
- **Agents** — managed agents (model + system prompt + approved skills + MCP servers + defaults) behind `/v1/agents/{slug}/messages`, with optional keys bound to a single agent.
- **Skill registry & marketplace** — publish skills (SKILL.md), MCP servers (credentials encrypted), agent templates and prompts with immutable, content-addressed versions; review workflow; install per team/project; ratings; Claude Code skill sync via `/v1/skills`.
- **Observability & audit** — async batched request logs (COPY), optional post-redaction payload capture, usage and chargeback by model/team/project/key/person/provider/agent, Prometheus metrics, JSON stdout logs for Loki/Splunk, and a tamper-evident hash-chained audit trail with one-click verification.
- **Console** — a modern web UI for developers and admins: playground with streaming, keys with copy-paste quick-starts, receipts, rollouts, marketplace, guardrail builder, ⌘K command palette, light and dark themes.

## Quick start

```bash
docker compose -f deploy/compose/docker-compose.yml up --build
open http://localhost:8080          # admin@example.com / change-me-please
```

The demo seeds a built-in mock provider, a `demo` model, Secret Shield and personal-data policies, and marketplace content, so everything works without provider credentials. Connect a real provider under **Providers**, then **Discover models**.

### Claude Code

```bash
export ANTHROPIC_BASE_URL=https://ai-gateway.example.com
export ANTHROPIC_AUTH_TOKEN=tpx_…        # a gateway key from the console
claude
```

Or authenticate developers with their own SSO identity instead of shared keys (enable *Accept this IdP's access tokens* on the identity provider):

```json
{ "apiKeyHelper": "az account get-access-token --resource api://theophylax --query accessToken -o tsv",
  "env": { "ANTHROPIC_BASE_URL": "https://ai-gateway.example.com", "ANTHROPIC_CUSTOM_HEADERS": "X-Theophylax-Project: prj_…" } }
```

### SDKs

```python
from anthropic import Anthropic
client = Anthropic(base_url="https://ai-gateway.example.com", api_key="tpx_…")

from openai import OpenAI
client = OpenAI(base_url="https://ai-gateway.example.com/v1", api_key="tpx_…")
```

### Verify a receipt

```bash
theophylax verify-receipt \
  --keys https://ai-gateway.example.com/.well-known/theophylax-receipt-keys.json \
  --request request.json --response response.json @receipt.jws
```

The receipt for any request is at `GET /v1/receipts/{request_id}` (same key) and streams end with an SSE comment `: theophylax-receipt <jws>`.

## Production on Kubernetes (EKS)

```bash
kubectl create secret generic theophylax-db --from-literal=DATABASE_URL='postgres://…?sslmode=require'
kubectl create secret generic theophylax-master-key --from-literal=MASTER_KEY=$(openssl rand -hex 32)
helm install theophylax deploy/helm/theophylax \
  --set publicURL=https://ai-gateway.example.com \
  --set serviceAccount.annotations."eks\.amazonaws\.com/role-arn"=arn:aws:iam::123456789012:role/theophylax-bedrock \
  --set ingress.enabled=true
```

The chart ships HPA, PDB, zone spread, non-root read-only pods, optional ServiceMonitor and NetworkPolicy. Replicas are stateless; configuration changes propagate instantly to every replica through Postgres `LISTEN/NOTIFY`. See [docs/operations.md](docs/operations.md).

## Documentation

- [Architecture](docs/architecture.md)
- [Connection strings & provider auth](docs/providers.md)
- [Guardrails, Secret Shield & prompt injection](docs/guardrails.md)
- [Proof-of-Inference receipts](docs/receipts.md)
- [Model rollouts](docs/rollouts.md)
- [Identity: SSO, SCIM & RBAC](docs/identity.md)
- [Configuration & operations](docs/operations.md)
- [API reference](docs/api.md)

## Development

```bash
make ui build          # console + binary
make test              # unit tests
THEOPHYLAX_TEST_DATABASE_URL=postgres://user@localhost:5432/postgres go test ./internal/server/   # end-to-end
```

## License

Apache License 2.0 — see [LICENSE](LICENSE).
