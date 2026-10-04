# API reference (summary)

## Inference (gateway key `tpx_…`, IdP JWT + `X-Theophylax-Project`, or console session)

| Method | Path | |
|---|---|---|
| POST | `/v1/messages` | Anthropic Messages (streaming, tools, betas) |
| POST | `/v1/messages/count_tokens` | Proxied to Anthropic; estimated elsewhere |
| POST | `/v1/chat/completions` | OpenAI Chat Completions (streaming, tools) |
| POST | `/v1/agents/{slug}/messages`, `/v1/agents/{slug}/chat/completions` | Managed agents |
| GET | `/v1/models` | Models visible to the caller (Anthropic shape when `anthropic-version` is sent) |
| GET | `/v1/whoami` | Caller context |
| GET | `/v1/skills`, `/v1/skills/{id}/download` | Skills installed for the key's scope |
| GET | `/v1/receipts/{request_id}` | Receipt + verification + anchoring |
| POST | `/v1/receipts/verify` | Public verification helper |
| GET | `/.well-known/theophylax-receipt-keys.json` | Receipt public keys |

## Control plane (session or personal access token `tpxp_…`)

Auth: `GET /api/auth/config`, `POST /api/auth/login`, `POST /api/auth/logout`, `GET /auth/oidc/{idp}/start`, `GET /api/me`, `POST /api/me/password`.

Hierarchy: `/api/orgs[/{org}]`, `/api/orgs/{org}/teams`, `/api/teams/{id}`, `/api/teams/{id}/projects`, `/api/orgs/{org}/projects`, `/api/projects/{id}`, `/api/members` (`scope_type`, `scope_id`), `/api/orgs/{org}/users`, `/api/users` (platform).

Keys: `GET /api/orgs/{org}/keys`, `POST /api/keys` (`kind: gateway|pat`), `PATCH /api/keys/{id}`, `POST /api/keys/{id}/revoke`, `POST /api/keys/{id}/rotate`.

Catalog: `/api/providers`, `POST /api/providers/validate`, `POST /api/providers/{id}/discover`, `/api/models`, `/api/models/{id}/deployments`, `/api/deployments/{id}`, `POST /api/models/import`.

Policy: `/api/orgs/{org}/guardrails`, `/api/guardrails/{id}`, `POST /api/guardrails/test`, `GET /api/guardrails/catalog`, `/api/orgs/{org}/budgets` (`per_user: true` on an org or team budget makes it a per-person default), `/api/orgs/{org}/budgets/personal` (`user_id`; the per-person budget that applies in each team and period), `/api/budgets/{id}`.

Identity: `/api/orgs/{org}/idps`, `/api/idps/{id}`, `POST /api/idps/{id}/scim-token`; SCIM at `/scim/v2/{Users,Groups,ServiceProviderConfig}`.

Registry: `/api/orgs/{org}/registry` (`view=marketplace|review`, `kind`, `q`), `/api/registry/{id}`, `…/versions`, `…/submit`, `…/review`, `…/install`, `…/rate`, `…/download`, `DELETE /api/installations/{id}`. Agents: `/api/orgs/{org}/agents`, `/api/agents/{id}`.

Rollouts: `/api/rollouts`, `/api/rollouts/{id}`, `POST /api/rollouts/{id}/{pause|resume|advance|promote|rollback}` (`rollback` on a promoted rollout restores the routing it replaced).

Evidence: `/api/orgs/{org}/logs`, `/api/logs/{id}`, `/api/orgs/{org}/usage` (`days`, `group_by`), `/api/orgs/{org}/billing` (`month=YYYY-MM`, `mine`), `/api/pricing`, `POST /api/pricing/refresh` (platform admin), `/api/orgs/{org}/overview`, `/api/orgs/{org}/audit`, `/api/audit`, `/api/audit/verify`, `/api/receipts/{id}`, `/api/receipts/checkpoints`, `/api/system`.

Errors use the Anthropic envelope `{"type":"error","error":{"type":"…","message":"…"}}` (OpenAI envelope on OpenAI endpoints).
