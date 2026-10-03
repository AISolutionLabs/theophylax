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

Any base auth can chain an extra `role_arn` for cross-account access. Requests use InvokeModel / InvokeModelWithResponseStream with SigV4; `anthropic-beta` headers are moved into the body as Bedrock requires. Discovery lists Anthropic foundation models and inference profiles (`us.`, `eu.`, `global.` …) — use profile IDs as upstream model IDs for cross-region inference.

IAM permissions: `bedrock:InvokeModel`, `bedrock:InvokeModelWithResponseStream`, and for discovery `bedrock:ListFoundationModels`, `bedrock:ListInferenceProfiles`; for Bedrock Guardrails `bedrock:ApplyGuardrail`.

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

## Model catalog tips

- Use **aliases** (`sonnet-latest`) and repoint them, or run a **rollout**, when new versions ship.
- A model named `claude-*` with upstream model `{model}` passes any matching name straight through (day-0 access to new models).
- Prices are per million tokens. If cache prices are blank, cache reads cost 0.1× and writes 1.25× the input price. Always check your provider's current price list.
