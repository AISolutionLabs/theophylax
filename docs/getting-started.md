# Getting started

This guide takes you from nothing to a working gateway that Claude Code, Codex CLI and the Anthropic and OpenAI SDKs can call. It has four parts:

1. [Run Theophylax](#1-run-theophylax): try it on a laptop, run the binary, or deploy to Kubernetes.
2. [First sign-in and setup](#2-first-sign-in-and-setup): connect a provider, add models and create a key.
3. [Connect your clients](#3-connect-your-clients).
4. [Before you go to production](#4-before-you-go-to-production).

Allow about ten minutes for the local trial and an hour for a production install.

## Requirements

| What | Version | Needed for |
|---|---|---|
| PostgreSQL | 14 or later (16 recommended) | Every install. All state, logs, receipts and the audit trail live here. |
| Docker with Compose | Any recent version | The local trial and building the container image |
| Go | 1.26 or later | Building from source only |
| Node.js | 22 | Building the console from source only |
| Kubernetes and Helm 3 | | The Kubernetes install only |

You also need credentials for at least one model provider, such as an Anthropic API key, AWS access to Bedrock, a Google Cloud project with Vertex AI, or an Azure AI Foundry resource. The local trial runs without any: it uses a built-in mock model.

## 1. Run Theophylax

Pick the option that fits.

### Option A: local trial with Docker Compose

This runs Postgres and the gateway, with demo data.

```bash
git clone https://github.com/Theophylax/phylax.git theophylax
cd theophylax
docker compose -f deploy/compose/docker-compose.yml up --build
```

Then open <http://localhost:8080> and sign in as `admin@example.com` with the password `change-me-please`.

The demo seeds:

- a mock provider and a `demo` model, so the playground and API work without credentials;
- Secret Shield and personal-data guardrail policies;
- sample marketplace content.

To try a real provider, export its credentials before `up`. The compose file passes `ANTHROPIC_API_KEY` and the `AWS_*` variables into the container. Then reference them from a connection string as `${env:ANTHROPIC_API_KEY}` (see [step 2](#2-first-sign-in-and-setup)):

```bash
export ANTHROPIC_API_KEY=sk-ant-…
docker compose -f deploy/compose/docker-compose.yml up --build
```

> The compose file sets a fixed master key and `THEOPHYLAX_DEV_MODE=true`. Both are for local use only. Never deploy this file as it is.

To stop the stack, run `docker compose -f deploy/compose/docker-compose.yml down`. Add `-v` to also delete the database.

### Option B: a single binary

Release binaries for Linux, macOS and Windows (amd64 and arm64) are attached to each [GitHub release](https://github.com/Theophylax/phylax/releases), with a cosign-signed `checksums.txt`. You can also build from source:

```bash
git clone https://github.com/Theophylax/phylax.git theophylax && cd theophylax
make ui build                 # builds the console, then bin/theophylax
```

Create a database and a master key, then start the gateway:

```bash
createdb theophylax
export THEOPHYLAX_DATABASE_URL='postgres://user:pass@localhost:5432/theophylax?sslmode=disable'
export THEOPHYLAX_MASTER_KEY=$(openssl rand -hex 32)      # store this safely: see below
export THEOPHYLAX_PUBLIC_URL=http://localhost:8080
export THEOPHYLAX_BOOTSTRAP_ADMIN_EMAIL=you@example.com
export THEOPHYLAX_BOOTSTRAP_ADMIN_PASSWORD='a-long-password-here'
export THEOPHYLAX_BOOTSTRAP_ORG=acme
./bin/theophylax
```

The gateway runs its database migrations on startup. It listens on `:8080` (`THEOPHYLAX_ADDR` changes this) and creates the first admin and organization only while the database has no users.

**Keep the master key.** It encrypts provider credentials, identity-provider secrets and MCP tokens, and it derives the session and receipt keys. If you lose it, every stored credential becomes unreadable.

Check that the gateway is up:

```bash
curl -s localhost:8080/healthz      # process is alive
curl -s localhost:8080/readyz       # database and configuration are loaded
./bin/theophylax version
```

### Option C: Kubernetes with Helm

The chart in `deploy/helm/theophylax` runs three stateless replicas. It includes autoscaling, a disruption budget, zone spread, and non-root, read-only pods, plus an optional ServiceMonitor and NetworkPolicy.

**1. Provision PostgreSQL**, for example RDS or Aurora PostgreSQL 15 or later, or Cloud SQL. Use `sslmode=require` in the connection string.

**2. Create the two secrets** the chart expects:

```bash
kubectl create namespace theophylax
kubectl -n theophylax create secret generic theophylax-db \
  --from-literal=DATABASE_URL='postgres://theophylax:…@db.example.internal:5432/theophylax?sslmode=require'
kubectl -n theophylax create secret generic theophylax-master-key \
  --from-literal=MASTER_KEY=$(openssl rand -hex 32)
kubectl -n theophylax create secret generic theophylax-admin \
  --from-literal=ADMIN_PASSWORD='a-long-password-here'
```

In production, sync these from AWS Secrets Manager or Vault with External Secrets instead of creating them by hand. Back up the master key separately from the database.

**3. Install the chart:**

```bash
helm install theophylax deploy/helm/theophylax -n theophylax \
  --set publicURL=https://ai-gateway.example.com \
  --set bootstrap.adminEmail=you@example.com \
  --set bootstrap.existingSecret=theophylax-admin \
  --set bootstrap.orgName=acme \
  --set ingress.enabled=true \
  --set ingress.hosts[0].host=ai-gateway.example.com \
  --set ingress.hosts[0].paths[0].path=/ \
  --set ingress.hosts[0].paths[0].pathType=Prefix
```

Tagged releases also publish the image to `ghcr.io/theophylax/theophylax` and the chart to `oci://ghcr.io/theophylax/charts/theophylax`. Before the first release, build the image yourself (`make docker`), push it to your registry and set `--set image.repository=… --set image.tag=…`.

**4. Give the pods cloud identity**, if you use Bedrock, Vertex or Azure without static keys:

- **Bedrock on EKS (IRSA):** create an IAM role allowed `bedrock:InvokeModel`, `bedrock:InvokeModelWithResponseStream`, `bedrock:ListFoundationModels` and `bedrock:ListInferenceProfiles`. Then `--set serviceAccount.annotations."eks\.amazonaws\.com/role-arn"=arn:aws:iam::123456789012:role/theophylax-bedrock`.
- **Azure workload identity:** `--set serviceAccount.annotations."azure\.workload\.identity/client-id"=<client-id>`.
- **Vertex:** use GKE workload identity, or workload identity federation from EKS or AKS. See [Providers](providers.md#google-vertex-ai).

**5. Configure streaming at the load balancer.** Server-sent events need buffering off and long idle timeouts:

- **ingress-nginx:** set `nginx.ingress.kubernetes.io/proxy-buffering: "off"` and `nginx.ingress.kubernetes.io/proxy-read-timeout: "900"`.
- **AWS ALB:** set `alb.ingress.kubernetes.io/load-balancer-attributes: idle_timeout.timeout_seconds=900`.

Put these in `ingress.annotations`.

**6. Check the rollout:**

```bash
kubectl -n theophylax rollout status deploy/theophylax
curl -s https://ai-gateway.example.com/readyz
```

All settings are in `deploy/helm/theophylax/values.yaml`. Environment variables the chart doesn't expose go in `config.extraEnv`. The full list is in [Configuration and operations](operations.md#environment).

## 2. First sign-in and setup

Open the public URL and sign in with the bootstrap admin. Then do the following steps in the console.

### Connect a provider

Go to **Providers** and click **Connect a provider**. Fill in the guided form, or paste a connection string. Reference secrets from the environment or from files instead of pasting them in:

```text
anthropic://api.anthropic.com?api_key=${env:ANTHROPIC_API_KEY}
bedrock://eu-west-1?auth=irsa
vertex://us-east5?project=my-proj&auth=metadata
azure-foundry://my-ai.services.ai.azure.com?auth=workload
azure-openai://my-oai.openai.azure.com?auth=api_key&api_key=${env:AZURE_OPENAI_KEY}&api_version=v1
gemini://?api_key=${env:GEMINI_API_KEY}
```

Every option for every provider is in [Providers and connection strings](providers.md). Click **Test connection** to check the credentials before you save. Stored credentials are encrypted, and the console only shows them redacted.

### Add models

On the provider, click **Discover models** and pick the models to offer. This works for Anthropic, Bedrock (including cross-region inference profiles) and OpenAI-compatible providers. For Vertex and Foundry, add models on **Models & routing** instead. Use the upstream model ID, or for Foundry the deployment name.

Each catalog entry has a public name, which is what clients send as `model`, and one or more deployments behind it. Several deployments give you failover and load balancing; [Complexity routing](routing.md) covers routing by request difficulty. Prices come from the providers' published list prices automatically.

### Create a team, a project and a key

1. Go to **Teams & projects** and create a team, then a project inside it. Keys, budgets and usage reports are scoped to projects.
2. Go to **API keys** and click **Create key**. Pick the project, and optionally restrict models or set rate limits.
3. Copy the `tpx_…` key. It is shown only once.

### Try it

Open the **Playground**, pick a model and send a message. Or call the API directly:

```bash
curl https://ai-gateway.example.com/v1/messages \
  -H "x-api-key: tpx_…" -H "anthropic-version: 2023-06-01" -H "content-type: application/json" \
  -d '{"model":"demo","max_tokens":256,"messages":[{"role":"user","content":"Hello"}]}'
```

The request appears under **Requests** with its tokens, cost, guardrail actions and signed receipt.

## 3. Connect your clients

Use your gateway URL and a `tpx_…` key in place of the provider's own URL and key.

### Claude Code

```bash
export ANTHROPIC_BASE_URL=https://ai-gateway.example.com
export ANTHROPIC_AUTH_TOKEN=tpx_…
claude
```

To have each developer use their own SSO identity instead of a shared key, turn on **Accept this IdP's access tokens** on the identity provider and use an `apiKeyHelper` in Claude Code's `settings.json`:

```json
{
  "apiKeyHelper": "az account get-access-token --resource api://theophylax --query accessToken -o tsv",
  "env": {
    "ANTHROPIC_BASE_URL": "https://ai-gateway.example.com",
    "ANTHROPIC_CUSTOM_HEADERS": "X-Theophylax-Project: prj_…"
  }
}
```

Skills and MCP servers you install from the marketplace are listed at `GET /v1/skills` and `GET /v1/mcp/servers`. The latter includes a ready-made `.mcp.json` document.

### Codex CLI and other Responses API clients

The gateway serves the OpenAI Responses API at `/v1/responses`. Requests to OpenAI-format providers are relayed. Requests to Claude models on Anthropic, Bedrock, Vertex or Foundry are translated to the Messages API. In `~/.codex/config.toml`:

```toml
model = "claude-sonnet"            # a model name from your catalog
model_provider = "theophylax"

[model_providers.theophylax]
name = "Theophylax"
base_url = "https://ai-gateway.example.com/v1"
env_key = "THEOPHYLAX_API_KEY"
wire_api = "responses"
```

```bash
export THEOPHYLAX_API_KEY=tpx_…
codex
```

Claude models reject forced tool use. Clients that send `tool_choice: "required"` should send `auto` instead (see the [API reference](api.md#forced-tool-use-on-claude)).

### SDKs

```python
from anthropic import Anthropic
client = Anthropic(base_url="https://ai-gateway.example.com", api_key="tpx_…")

from openai import OpenAI
client = OpenAI(base_url="https://ai-gateway.example.com/v1", api_key="tpx_…")
```

You can label requests for chargeback with a default header, such as `X-Theophylax-Tags: feature=search` (see [Request tags](api.md#request-tags)).

### Verify a receipt

Every response carries a request ID, and its receipt is at `GET /v1/receipts/{request_id}`. Streams end with an SSE comment, `: theophylax-receipt <jws>`. To verify a receipt offline:

```bash
theophylax verify-receipt \
  --keys https://ai-gateway.example.com/.well-known/theophylax-receipt-keys.json \
  --request request.json --response response.json @receipt.jws
```

[Receipts](receipts.md) explains what a receipt proves and what it doesn't.

## 4. Before you go to production

Work through this list before sending real traffic:

- [ ] **Master key** generated with `openssl rand -hex 32`, stored in a secret manager, and backed up separately from the database.
- [ ] **`THEOPHYLAX_DEV_MODE` unset** and the compose file's demo key not in use.
- [ ] **TLS** in front of the gateway, with `THEOPHYLAX_PUBLIC_URL` set to the `https://` address.
- [ ] **Postgres backups.** Request logs, receipts and the audit trail are your evidence.
- [ ] **Single sign-on** configured under **Single sign-on** (OIDC with PKCE, plus SCIM provisioning). Then set `THEOPHYLAX_LOCAL_LOGIN=false` (Helm: `config.localLogin=false`). See [Identity](identity.md).
- [ ] **Guardrails** attached at the org level: Secret Shield, PII and prompt injection, in *monitor* mode first and then *enforce*. See [Guardrails](guardrails.md).
- [ ] **Budgets and rate limits** per org, team, project or person, with alert channels under **Settings → Alerts**. See [Budgets](operations.md#budgets) and [Alerts](operations.md#alerts).
- [ ] **Observability:** scrape `/metrics` with Prometheus. Optionally set `THEOPHYLAX_OTLP_ENDPOINT` for traces, and `THEOPHYLAX_LOG_SINK_URL` to stream logs and the audit log to your SIEM.
- [ ] **Price catalog:** for air-gapped installs, or to pin prices to a reviewed copy, set `THEOPHYLAX_PRICING_URL` to a `file://` mirror or a fixed commit URL. See [Pricing](operations.md#pricing).
- [ ] **Streaming** works through every proxy and load balancer in the path, with buffering off and idle timeouts of at least 900 s.
- [ ] **Configuration as code**, if you want reviewed changes: `theophylax config export | plan | apply`. See [Configuration as code](config-as-code.md).

## Coming from LiteLLM

`theophylax import-litellm` copies your providers, models, teams, users, keys and budgets. Existing `sk-…` keys keep working:

```bash
theophylax import-litellm -config litellm.yaml -litellm-db "$LITELLM_DATABASE_URL" \
  -litellm-salt-key "$LITELLM_SALT_KEY" -dry-run      # review the report, then run again without -dry-run
```

[Configuration and operations](operations.md#migrating-from-litellm) has the full mapping.

## Troubleshooting

| Symptom | Fix |
|---|---|
| `THEOPHYLAX_MASTER_KEY is required` at startup | Set a 32-byte key in hex or base64: `openssl rand -hex 32`. |
| `/readyz` fails | The database is unreachable or migrations failed. Check `THEOPHYLAX_DATABASE_URL` and the startup logs, which are JSON on stdout. |
| Locked out of the console | Run `theophylax reset-password -email you@example.com` where the gateway runs. Add `-platform-admin` to restore platform-admin rights. See [Recovering a locked-out account](operations.md#recovering-a-locked-out-account). |
| Streams stall or arrive all at once | A proxy is buffering. Turn buffering off and raise idle timeouts (see [step 5](#option-c-kubernetes-with-helm) of the Kubernetes install). |
| `413` on large requests | Raise `THEOPHYLAX_MAX_BODY_MB` (default 32). Each body is held in memory. |
| `401` from the gateway | The key was revoked or expired, or it belongs to a different project. With SSO tokens, check the `X-Theophylax-Project` header and that the person is a member of that project. |
| A provider call fails with 4xx | Look the request up under **Requests**, which shows the upstream status and message. For connection problems, open the provider and click **Test connection**. |
| A model is missing from `/v1/models` | The key's project has no access, or the model has no healthy deployment. Check **Models & routing** and the key's allowed models. |
| Requests show $0 cost | The upstream model is not in the price catalog. **Billing** lists unpriced requests, and **Billing → Price source** shows when the catalog was last refreshed. |
