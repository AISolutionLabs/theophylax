# API reference (summary)

## Inference (gateway key `tpx_…`, IdP JWT + `X-Theophylax-Project`, or console session)

| Method | Path | |
|---|---|---|
| POST | `/v1/messages` | Anthropic Messages (streaming, tools, betas) |
| POST | `/v1/messages/count_tokens` | Proxied to Anthropic; estimated elsewhere |
| POST | `/v1/chat/completions` | OpenAI Chat Completions (streaming, tools) |
| POST | `/v1/responses` | OpenAI Responses: relayed to OpenAI-format providers, or bridged to Claude (function and custom tools, streaming events). `previous_response_id` needs an OpenAI-format provider |
| POST | `/v1/embeddings` | OpenAI embeddings shape; OpenAI, Azure, Gemini, Vertex, Bedrock Titan/Cohere |
| POST | `/v1/images/generations`, `/v1/images/edits`, `/v1/images/variations` | Relayed to OpenAI-format providers |
| POST | `/v1/audio/speech`, `/v1/audio/transcriptions`, `/v1/audio/translations` | Relayed (multipart supported) |
| POST | `/v1/moderations` | Relayed |
| POST | `/v1/agents/{slug}/messages`, `/v1/agents/{slug}/chat/completions` | Managed agents |
| GET | `/v1/models` | Models visible to the caller (Anthropic shape when `anthropic-version` is sent) |
| GET | `/v1/whoami` | Caller context |
| GET | `/v1/skills`, `/v1/skills/{id}/download` | Skills installed for the key's scope |
| GET | `/v1/mcp/servers` | MCP servers installed for the key's scope, with an `.mcp.json` document (`mcp_json`) that points remote servers at the gateway; stored tokens are never returned |
| POST, GET, DELETE | `/mcp/{slug}` | [MCP gateway](#mcp-gateway): Streamable HTTP proxy to an installed MCP server |
| GET | `/v1/receipts/{request_id}` | Receipt + verification + anchoring |
| POST | `/v1/receipts/verify` | Public verification helper |
| GET | `/.well-known/theophylax-receipt-keys.json` | Receipt public keys |

### Request tags

Label requests for chargeback and analytics with up to 10 `key=value` pairs in `X-Theophylax-Tags`:

```http
X-Theophylax-Tags: feature=search, env=prod, ticket=OPS-123
```

Keys are lower-case letters, digits, `.`, `_` and `-` (up to 40 characters); values up to 128 characters. A gateway key can carry default tags in its metadata (`{"tags": {"cost_center": "cc-42"}}`), and header tags win on conflict. Invalid pairs are dropped and counted in the `X-Theophylax-Tags-Dropped` response header; the request still goes through. Tags are stored with the request log, exported as `theophylax.tag.<key>` span attributes and in log-sink records, and never sent to the model. With the SDKs, set them as default headers, e.g. `Anthropic(default_headers={"X-Theophylax-Tags": "feature=search"})`.

Filter with `?tag=feature:search,env:prod` (all must match) on `GET /api/orgs/{org}/logs` and `/usage`, and break usage down by one tag with `/usage?group_by=tag:feature`.

## Control plane (session or personal access token `tpxp_…`)

Auth: `GET /api/auth/config`, `POST /api/auth/login`, `POST /api/auth/logout`, `GET /auth/oidc/{idp}/start`, `GET /api/me`, `POST /api/me/password`.

Hierarchy: `/api/orgs[/{org}]`, `/api/orgs/{org}/teams`, `/api/teams/{id}`, `/api/teams/{id}/projects`, `/api/orgs/{org}/projects`, `/api/projects/{id}`, `/api/members` (`scope_type`, `scope_id`), `/api/orgs/{org}/users`, `/api/users` (platform).

Keys: `GET /api/orgs/{org}/keys`, `POST /api/keys` (`kind: gateway|pat`), `PATCH /api/keys/{id}`, `POST /api/keys/{id}/revoke`, `POST /api/keys/{id}/rotate`.

Catalog: `/api/providers`, `POST /api/providers/validate`, `POST /api/providers/{id}/discover`, `/api/models`, `/api/models/{id}/deployments`, `/api/deployments/{id}`, `POST /api/models/import`.

Policy: `/api/orgs/{org}/guardrails`, `/api/guardrails/{id}`, `POST /api/guardrails/test`, `GET /api/guardrails/catalog`, `/api/orgs/{org}/budgets` (`per_user: true` on an org or team budget makes it a per-person default), `/api/orgs/{org}/budgets/personal` (`user_id`; the per-person budget that applies in each team and period), `/api/budgets/{id}`.

Identity: `/api/orgs/{org}/idps`, `/api/idps/{id}`, `POST /api/idps/{id}/scim-token`; SCIM at `/scim/v2/{Users,Groups,ServiceProviderConfig}`.

Registry: `/api/orgs/{org}/registry` (`view=marketplace|review`, `kind`, `q`), `/api/registry/{id}`, `…/versions`, `…/submit`, `…/review`, `…/install` (optional `version` pins an approved version), `…/rate`, `…/download`, `PATCH /api/installations/{id}` (`{"version":"1.2.0"}` pins, `{"version":""}` follows the approved version), `DELETE /api/installations/{id}`. Clients get the version from the most specific installation for their key (project, then team, then org). Agents: `/api/orgs/{org}/agents`, `/api/agents/{id}`.

Rollouts: `/api/rollouts`, `/api/rollouts/{id}`, `POST /api/rollouts/{id}/{pause|resume|advance|promote|rollback}` (`rollback` on a promoted rollout restores the routing it replaced).

Evidence: `/api/orgs/{org}/logs`, `/api/logs/{id}`, `/api/orgs/{org}/usage` (`days`, `group_by`), `/api/orgs/{org}/billing` (`month=YYYY-MM`, `mine`), `/api/pricing`, `POST /api/pricing/refresh` (platform admin), `/api/orgs/{org}/overview`, `/api/orgs/{org}/audit`, `/api/audit`, `/api/audit/verify`, `/api/receipts/{id}`, `/api/receipts/checkpoints`, `/api/system`.

### Forced tool use on Claude

Claude Opus 5.5, Sonnet 5.5, Fable 5.1 and Mythos 5.1 reject forced tool use. When `/v1/chat/completions` or `/v1/responses` runs on one of them, `tool_choice: "required"` or a named function is translated to Anthropic's `any`/`tool` as usual and the upstream returns 400; the gateway does not silently downgrade it to `auto`. Clients (for example the Agents SDK with a required tool) should send `auto` for these models (Anthropic recommends `auto` with strict tool use), or be routed to an earlier model.

Errors use the Anthropic envelope `{"type":"error","error":{"type":"…","message":"…"}}` (OpenAI envelope on OpenAI endpoints).

## MCP gateway

`/mcp/{slug}` proxies a remote MCP server from the [registry](../README.md) (Streamable HTTP transport), so tool traffic gets the same controls as model traffic. Clients connect with their own gateway key. The `.mcp.json` from `/v1/mcp/servers` is already set up this way:

```json
{"mcpServers": {"jira": {"type": "http", "url": "https://ai.example.com/mcp/jira",
  "headers": {"Authorization": "Bearer ${THEOPHYLAX_API_KEY}"}}}}
```

For every request the gateway:

- **Checks installation.** It serves the server only if it is installed for the caller's org, team or project, at the version that applies there (a pinned version or the approved one).
- **Injects the credential.** It adds the server's stored token, which is encrypted at rest. People never hold upstream credentials, and the server never sees their gateway key. If the server rejects the stored token, the caller gets a 502 rather than an auth challenge.
- **Enforces the tool policy.** A server version's content may set `"tool_policy": {"allow": [...], "deny": [...]}`; a trailing `*` matches a prefix. Denied tools are removed from `tools/list`, and calls to them are refused before they reach the server. The policy is part of the reviewed, versioned server definition.
- **Applies guardrails to arguments and results.**
  - The caller's guardrail policies run over `tools/call` arguments before the tool sees them, so keyword, PII and Secret Shield rules can block or redact what goes to a third-party tool.
  - Tool results are evaluated as untrusted content before the model sees them. A result that prompt-injection detection or another blocking rule flags is replaced with an `isError` result saying the gateway withheld it.
- **Logs and signs.** Each exchange is logged under endpoint `/mcp/{slug}`, with the JSON-RPC method or `tools/call <tool>` as the upstream model and any guardrail findings (phase `tool_result` for results). Each one gets a signed [receipt](receipts.md) of the exact request and response bytes.
- **Binds sessions to the caller.** The server's `Mcp-Session-Id` is returned signed for this caller and project. Presenting it with another key, or altering it, returns 404, and MCP clients then start a new session.

Local (stdio) servers run on the developer's machine and cannot be proxied. Servers using the legacy HTTP+SSE transport are listed with their own URL.
