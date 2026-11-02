# Providers and connection strings

Every provider is configured by a connection string (the console's guided form builds one for you). Two syntaxes are accepted:

```text
<kind>://<host-or-region>?key=value&key=value
Provider=<kind>;Key=Value;Key=Value
```

Any value can reference a secret instead of embedding it: `${env:NAME}` or `${file:/mnt/secrets/name}`. The full string is encrypted at rest (AES-256-GCM, key derived from `THEOPHYLAX_MASTER_KEY`); the console only ever shows a redacted form.

Common options for every kind: `proxy=http://proxy:8080` (or `none`), `ca_bundle=/path/ca.pem` (TLS-inspecting egress), and in the provider's JSON options `drop_params`, `headers`, `timeout_seconds`.

## Anthropic

```text
anthropic://api.anthropic.com?api_key=${env:ANTHROPIC_API_KEY}
anthropic://gateway.internal?auth_token=…&base_url=https://gateway.internal
```

## AWS Bedrock

```text
bedrock://eu-west-1?auth=irsa                       # EKS IAM Roles for Service Accounts (recommended)
bedrock://eu-west-1?auth=pod_identity               # EKS Pod Identity
bedrock://eu-west-1?auth=default                    # env → IRSA → pod identity → IMDSv2
bedrock://eu-west-1?auth=assume_role&role_arn=arn:aws:iam::111:role/bedrock&external_id=…
bedrock://eu-west-1?auth=static&access_key_id=…&secret_access_key=${env:AWS_SECRET_ACCESS_KEY}
bedrock://us-east-1?auth=api_key&api_key=${env:AWS_BEARER_TOKEN_BEDROCK}
bedrock://eu-west-1?auth=irsa&endpoint=https://vpce-0abc.bedrock-runtime.eu-west-1.vpce.amazonaws.com
```

Any base auth can chain an extra `role_arn` for cross-account access. Requests use InvokeModel / InvokeModelWithResponseStream with SigV4; `anthropic-beta` headers are moved into the body as Bedrock requires. Discovery lists Anthropic foundation models that can be invoked on demand in the region, and inference profiles (`us.`, `eu.`, `global.` …) — use profile IDs as upstream model IDs for cross-region inference. Models launched from September 2026 have no date or `-v1:0` suffix (`anthropic.claude-opus-5-5`, `global.anthropic.claude-opus-5-5`); older ones keep it (`us.anthropic.claude-haiku-4-5-20251001-v1:0`). Some models are only served through cross-region profiles: Claude Haiku 5.5 has no in-region `bedrock-runtime` endpoint, so use `global.anthropic.claude-haiku-5-5` or a geo profile (`us.`, `eu.`, `au.`, `jp.`) as the upstream model ID; AWS lists no region where the bare `anthropic.claude-haiku-5-5` can be invoked directly, so discovery and the model watcher offer only its profiles.

IAM permissions: `bedrock:InvokeModel`, `bedrock:InvokeModelWithResponseStream`, and for discovery `bedrock:ListFoundationModels`, `bedrock:ListInferenceProfiles`; for Bedrock Guardrails `bedrock:ApplyGuardrail`.

### Entra ID → AWS federation

For fleets that already run on Azure workload identity, Bedrock can authenticate with an Entra ID token exchanged at STS for a role in each AWS account (one provider per account keeps per-team billing isolation):

```text
bedrock://eu-west-1?auth=entra&entra_auth=workload&audience=api://bedrock-federation&role_arn=arn:aws:iam::111122223333:role/team-a
```

The IAM role trusts `sts.windows.net/<tenant-id>/` as an OIDC provider with the Entra application ID URI as audience. `entra_auth` is `workload` (AKS/EKS with `AZURE_FEDERATED_TOKEN_FILE`), `managed` or `entra` (client secret). `sts_endpoint=` points at an STS VPC endpoint.

## Google Vertex AI

```text
vertex://us-east5?project=my-proj&auth=workload_identity&audience=//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/eks/providers/eks&token_file=/var/run/secrets/eks.amazonaws.com/serviceaccount/token&service_account=gateway@my-proj.iam.gserviceaccount.com
vertex://global?project=my-proj&auth=metadata                 # GKE workload identity
vertex://europe-west1?project=my-proj&credentials=${file:/secrets/sa.json}
vertex-openai://us-central1?project=my-proj&auth=metadata     # Gemini and partner models (google/gemini-2.5-pro)
```

`vertex://` serves Claude through `rawPredict`/`streamRawPredict` in the Messages format. Upstream IDs for models launched from September 2026 have no date suffix (`claude-opus-5-5`, `claude-sonnet-5-5`); older models keep it (`claude-haiku-4-5@20251001`). `vertex-openai://` uses Vertex's OpenAI-compatible endpoint. Workload identity federation lets EKS or AKS pods call Vertex without a key file; an `external_account` credentials file from `gcloud iam workload-identity-pools create-cred-config` also works.

## Gemini API

```text
gemini://?api_key=${env:GEMINI_API_KEY}
```

## Azure AI Foundry (Claude on Azure)

```text
azure-foundry://my-ai.services.ai.azure.com?auth=api_key&api_key=${env:FOUNDRY_KEY}
azure-foundry://my-ai.services.ai.azure.com?auth=entra&tenant_id=…&client_id=…&client_secret=${env:AZ_SECRET}
azure-foundry://my-ai?auth=workload            # AKS — or EKS with a federated credential on the app registration
azure-foundry://my-ai?auth=managed&client_id=… # user-assigned managed identity
```

Upstream model IDs are your Foundry deployment names.

## Azure OpenAI

```text
azure-openai://my-oai.openai.azure.com?auth=api_key&api_key=…&api_version=v1
Provider=AzureOpenAI;Endpoint=https://my-oai.openai.azure.com;Auth=Entra;TenantId=…;ClientId=…;ClientSecret=…;ApiVersion=2024-10-21
```

`api_version=v1` uses the `/openai/v1` surface (model = deployment name); a dated version uses `/openai/deployments/{deployment}`.

## OpenAI-compatible

```text
openai://api.openai.com?api_key=${env:OPENAI_API_KEY}
openai://vllm.internal:8000?base_url=http://vllm.internal:8000/v1
```

### Shorthand presets

`groq://`, `mistral://`, `together://`, `deepseek://`, `xai://`, `openrouter://`, `fireworks://`, `cerebras://` and `perplexity://` fill in the public API base (`groq://?api_key=${env:GROQ_API_KEY}`); `ollama://gpu-box:11434`, `vllm://host:8000` and `lmstudio://host:1234` point at a local server. Give the local presets just `host:port` — the `http://` scheme and the `/v1` path are added for you (a trailing `/v1` or a full `http://…` URL after the scheme are tolerated too). To set the full base URL yourself, use `base_url`, e.g. `Provider=lmstudio;base_url=http://127.0.0.1:1234/v1`.

## Custom credential provider

For any OpenAI- or Anthropic-compatible API that sits behind credentials the gateway must manage. The gateway adds the credentials to every call, keeps access tokens fresh, and renews and retries once when the upstream rejects a token (401).

```
# HTTP Basic on every request
custom://api.acme.com/v1?name=acme&auth=basic&username=svc&password=${env:ACME_PASSWORD}

# Basic client credentials exchanged for an access + refresh token
custom://api.acme.com/v1?name=acme&auth=basic&username=<client id>&password=${env:ACME_SECRET}&token_url=https://auth.acme.com/oauth/token

# Bearer token, renewed with the refresh token
custom://api.acme.com/v1?name=acme&auth=bearer&token=${env:ACME_TOKEN}&refresh_token=${env:ACME_REFRESH}&token_url=https://auth.acme.com/oauth/token&client_id=gateway
```

| Parameter | |
|---|---|
| `name` | **Required.** The credential name. Every catalog model routed through this credential must be named `<model>@<name>`, e.g. `gpt-4o@acme`. Discovery and import add the suffix; adding a deployment to a model without it is refused. |
| `format` | `openai` (default; calls `<url>/chat/completions` and `<url>/models`) or `anthropic` (calls `<url>/v1/messages` and `<url>/v1/models`). |
| `auth` | `basic` or `bearer`. |
| `username`, `password` | Basic credentials. Sent on every request, or, with `token_url`, used as client credentials at the token endpoint. |
| `grant_type` | With Basic and `token_url`: `client_credentials` (default) or `password` (sends username and password as resource-owner credentials; add `client_id`/`client_secret` if the endpoint needs client authentication). |
| `token`, `refresh_token` | Bearer access token and optional refresh token. A refresh token needs `token_url`. |
| `token_url`, `client_id`, `client_secret`, `scope`, `audience` | OAuth 2.0 token endpoint and optional parameters. |
| `base_url` | Use instead of the host for a plain `http://` endpoint. |

Access tokens are renewed a minute before `expires_in`, or when the upstream returns 401. The refresh token is tried first; with Basic, the gateway falls back to the original grant. Tokens obtained this way are stored encrypted in Postgres (`provider_tokens`) and shared by every replica under a lock, so refresh tokens the identity provider rotates are never lost or reused. Changing the credentials discards the stored tokens. These requests have no upstream list price, so Billing shows them as unpriced.

## OAuth 2.0

Anthropic, OpenAI-compatible and custom credential providers accept `auth=oauth`. The gateway obtains access tokens, renews them before they expire, and on a 401 renews the token and retries the request once. Tokens are stored encrypted in Postgres and shared by every replica.

```
# Client credentials (service to service)
openai://api.acme.com?auth=oauth&token_url=https://auth.acme.com/oauth/token&client_id=gateway&client_secret=${env:ACME_SECRET}&scope=models.invoke

# Authorization code: an admin signs in once
anthropic://llm.acme.com?auth=oauth&grant_type=authorization_code&authorize_url=https://auth.acme.com/authorize&token_url=https://auth.acme.com/oauth/token&client_id=gateway&client_secret=${env:ACME_SECRET}&scope=offline_access%20models
```

| Parameter | |
|---|---|
| `grant_type` | `client_credentials` (default) or `authorization_code`. |
| `token_url`, `client_id` | Required. |
| `client_secret` | Required for client credentials. Optional for the authorization code grant, where PKCE is always used. |
| `client_auth` | `basic` (default, `client_secret_basic`) or `post` (`client_secret_post`). |
| `authorize_url` | Required for `authorization_code`. |
| `scope`, `audience` | Optional. Include the scope your provider needs to issue a refresh token (often `offline_access`). |
| `base_url` | API URL, if it isn't `https://<host>` (Anthropic) or `https://<host>/v1` (OpenAI-compatible). |

For the authorization code grant, register the redirect URI `<THEOPHYLAX_PUBLIC_URL>/auth/provider-oauth/callback` with the provider's OAuth app. Then, under **Providers**, click **Connect** and sign in at the provider. Until it's connected, the provider shows **not connected** and its requests fail with a message saying so. The access token is refreshed with the refresh token from then on. If the provider revokes the authorization, requests fail with a message asking an admin to reconnect. **Disconnect** deletes the stored tokens. Changing the OAuth settings also invalidates them, so the provider has to be connected again.

## Model catalog tips

- Use **aliases** (`sonnet-latest`) and repoint them, or run a **rollout**, when new versions ship.
- A model named `claude-*` with upstream model `{model}` passes any matching name straight through (day-0 access to new models).
- Prices are not entered by hand. Each request is billed at the upstream provider's list price for the deployment that served it, looked up by its upstream model ID in the price catalog (see [operations](operations.md#pricing)). Input, output, cache-read and cache-write tokens (including 1-hour cache writes, long-context tiers and Claude fast mode) are each priced at the catalog rate, with no markup.
- Use the provider's exact model or inference-profile ID so it can be priced: Bedrock regional (`us.`, `eu.`, `apac.`) and `global.` profiles have their own list prices. For Azure OpenAI deployments with custom names, the model name Azure reports in the response is used. Deployments that can't be priced show **no list price** in Models & routing, and their requests are listed separately in Billing.
