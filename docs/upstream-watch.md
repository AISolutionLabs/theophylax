# Upstream watch log

A weekly agent reviews provider changelogs, competitor gateways and security
advisories, and records what it found here so later runs can skip items
already handled. Classes: **(a)** handled already (no work), **(b)** needs
code, **(c)** docs only, **(d)** not relevant.

## 2026-10-05

Window: roughly 2026-09-20 to 2026-10-05 (first run, so September items are included).

### Anthropic

- **Claude Opus 5.5** (`claude-opus-5-5`, 2026-09-22) and **Sonnet 5.5** (`claude-sonnet-5-5`, 2026-09-28), on the API, Bedrock, Vertex and Foundry. **(a)** The model watcher discovers them and the price catalog already lists every platform ID, including Bedrock `global.`/`us.`/`eu.`/`au.`/`jp.` profiles. **(c)** New IDs have no date, `-v1:0` or `@date` suffix. Documented in providers.md, with regression tests for pricing lookup and the Bedrock/Vertex request paths. [Release notes](https://platform.claude.com/docs/en/release-notes/overview)
- **Forced tool use rejected** (`tool_choice` `any`/`tool` → 400) on Opus 5.5, Sonnet 5.5, Fable 5.1 and Mythos 5.1. **(c)** Bridges keep translating `required` to `any` rather than silently downgrading to `auto`. Documented in api.md.
- **Thinking `disabled`/`enabled` → 400 on Opus 5.5**; Sonnet 5.5 uses `thinking: {"type":"between_tools"}` instead of `disabled`. **(a)** The gateway never sets `thinking`; `/v1/messages` passes it through.
- **Sonnet 4.5 deprecated** 2026-09-30, retiring on the Claude API **2026-11-30**. **(a)** The catalog carries `deprecation_date` 2026-11-30, so the model watcher flags it. Operators must repoint aliases.
- **Cache diagnostics GA** (2026-09-23): Messages responses always include `diagnostics` (null unless requested). **(a)** Responses are parsed as generic maps.
- **Refusal billing** (2026-09-24): pre-output refusals in some `stop_details.category` values are billed again. **(a)** Billing uses reported usage.
- Betas: inline tools (`inline-tools-2026-09-15`), compaction on demand (`compact-2026-09-04`), thinking binding controls, per-message effort. **(a)** Passed through on `/v1/messages`, and moved into the body for Bedrock.
- Compliance API and Managed Agents changes. **(d)**

### AWS Bedrock

- Opus 5.5 and Sonnet 5.5 on Bedrock (commercial and GovCloud); new Claude regions in India, Seoul and Singapore (2026-09-29). **(a)**
- **Bedrock EOL for `anthropic.claude-sonnet-4-20250514-v1:0` (and its profiles) is 2026-10-14.** **(a)** The catalog's deprecation date flags it in the model watcher.
- Native Anthropic Messages endpoint on Bedrock ("bedrock-mantle"). **Not implemented; needs a decision.** It would be a new provider transport, not a fix. LiteLLM added it in v1.104.0.

### Google Vertex AI / Gemini

- Vertex generative-AI docs moved to the "Gemini Enterprise Agent Platform"; no API path change found. **(d)**
- Gemini 3.8 family GA, `/v1beta/voices`, and Gemini 2.5 capacity limits. **(a)/(d)** Native-API features are outside the OpenAI-compatible surface the gateway uses.

### Azure

- Azure OpenAI: no new GA or preview api-version and no retirements found. Foundry: Opus 5.5 and Sonnet 5.5 deployments (`claude-opus-5-5`, `claude-sonnet-5-5`). **(a)**

### OpenAI

- GPT-6 family (`gpt-6-astra`, `gpt-6-sol`, `gpt-6-luna`, `gpt-6.1-sol`) and new image models. **(a)** Discovered and priced through the catalog.
- New Responses features: async tool calls, programmatic tool calling (`program`/`program_output` items), `configuration_update` input items, `reasoning.context`/`reasoning.mode`, `service_tier: "ultrafast"`, and WebSocket mid-turn steering.
  - **(a)** when relayed to OpenAI-format providers.
  - When bridged to Claude, unknown input items return a clear 400.
  - Revisit if Codex CLI or the Agents SDK start to require any of them.
- Agents API beta and `v1/live/sessions`. **(d)** for now: new surfaces, not changes to the existing ones.
- Deprecations: GPT-5/o3 snapshots shut down 2026-12-11, and transcription models 2027-02-26. **(a)** Covered by the catalog and the model watcher.

### Competitors

- **LiteLLM** v1.103.0 and v1.104.0: Bedrock Mantle Messages, Foundry Responses on `/openai/v1/responses`, and a JWT claim → key mapping.
- **Bifrost** v2.2.2–v2.2.5: session affinity, `between_tools` thinking forwarding, an ultrafast pricing tier.
- **Portkey:** no release.
- Nothing adopted this run. Bedrock Mantle is noted for a decision.

### Security

- **Go 1.24 is out of support**, so the go1.24.x toolchain misses stdlib security fixes (net/http, crypto/tls, net/url, encoding/asn1 and others in go1.26.6 and later). **(b), needs a decision:** move to Go 1.26/1.27.
- **pgx v5.7.1:** GO-2026-4772 and GO-2026-4771 are fixed in v5.9.0. GO-2026-4772 is in `pgproto3.Backend`, the server side of the protocol, and is unlikely to be reachable from a client. **pgx ≥ 5.9 requires Go ≥ 1.25**, so it is coupled to the toolchain bump.
- **golang.org/x/text v0.18.0:** GO-2026-5970 (`unicode/norm` loop) is fixed in v0.39.0.
- Dependabot opened these bumps (#3, #8, #9), and they were closed on 2026-10-05.
- **Done (follow-up PR, 2026-10-05):** go directive 1.26.0 with toolchain go1.27.1, pgx v5.11.0, x/text v0.42.0, and the `golang:1.27-alpine` build image. The upgrade exposed a race between concurrent snapshot reloads, now serialised.
- Vite CVE-2026-39364 is exploited in the wild, but only against an exposed dev server. **(d)** for production builds.

## 2026-10-12

Window: 2026-10-02 to 2026-10-12.

### Anthropic

- **Claude Haiku 5.5** (`claude-haiku-5-5`, 2026-10-07) on the API, Bedrock, Vertex and Foundry. It has a 1M context window, 128K max output, adaptive thinking by default and `effort`. [Release notes](https://platform.claude.com/docs/en/release-notes/overview)
  - **(a)** The catalog already prices every platform ID, including its **100k-token long-context tier** (the first Claude tier below 200k) and the new combined `cache_creation_input_token_cost_above_1hr_above_100k_tokens` key. Both parse correctly. Regression tests were added.
  - **(a)** Moving from Haiku 4.5 can return a 400 for `budget_tokens` thinking, non-default `temperature`/`top_p`/`top_k`, assistant prefill, `computer_20250124`, or resent thinking blocks after `system`/`tools` changed. The gateway passes these through on `/v1/messages`. The Responses bridge forwards `temperature`/`top_p` only when the client sets them.
- **Sonnet 5.5 cache reads cut to $0.10/MTok** (2026-10-07). **(a)** The catalog was already updated.
- **Models API:** new fields `line` (10-01), `capabilities.thinking.types.disabled` (10-05) and `capabilities.server_tools` (10-06). **(a)** Discovery reads only `id` and `display_name`. These fields could later drive the bridges' thinking handling; not adopted.
- SDK toolsets (beta), Managed Agents web-tool host rules and dynamic workflows (`multiagent_20261001`), Compliance API, and plan credits. **(d)**
- No new deprecations. `claude-haiku-4-5-20251001` has no retirement notice yet.

### AWS Bedrock

- **Haiku 5.5** (`anthropic.claude-haiku-5-5`; profiles `global.`, `us.`, `eu.`, `au.`, `jp.`). On `bedrock-runtime` it is available only through cross-region profiles, with no in-region invocation. It has no structured outputs and no count-tokens; EOL is no earlier than 2027-10-07. [Model card](https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-haiku-5-5.html)
  - **(b) Done:** discovery now skips foundation models whose `inferenceTypesSupported` lacks `ON_DEMAND`, so the model watcher no longer offers a bare ID that always fails.
  - **(c)** providers.md documents this.
  - `count_tokens` is already estimated for non-Anthropic providers.
- India geo profile for Kimi K3, and Cost Explorer attribution for Bedrock. **(d)**
- Bedrock Mantle is unchanged. It is still waiting on a decision (see 2026-10-05).

### Google Vertex AI / Gemini

- Haiku 5.5 is in Model Garden (2026-10-07). **(a)** The catalog has `vertex_ai/claude-haiku-5-5`.
- **Gemini API:** `gemini-3.7-flash` and `gemini-3.5-flash` are deprecated and **auto-routed** to newer Flash models (2026-10-08). `deep-research-pro-preview-12-2025` shuts down 2026-10-23. Preview audio/TTS/Veo models shut down on 2026-10-22 and 2026-11-17. **(a)** These are catalog and model-watch concerns. Deployments still pointing at the deprecated IDs should be repointed. [Changelog](https://ai.google.dev/gemini-api/docs/changelog)
- The OpenAI-compatible endpoint is unchanged. **(d)**

### Azure

- Haiku 5.5 in Foundry (`claude-haiku-5-5`, Global Standard and Data Zone US), with catalog deprecation date 2027-09-29. **(a)**
- No new Azure OpenAI api-version and no retirements. **(d)**

### OpenAI

- `service_tier: "ultrafast"` for `gpt-6.1-sol`, a `chat-latest` refresh, and usage-tier changes. **(a)/(d)**
- Decisions API beta (`v1/decisions`). **(d)** It is a new surface.
- **Codex CLI 0.161–0.162:**
  - It honours `Retry-After` on Responses errors and sends base instructions as `developer`/`system` input messages. **(a)** The bridge already folds those into `system`.
  - The new assistant message `phase` ("partial answer") is ignored by the bridge. **(a)**
  - "Responses Lite" incremental tool-catalog updates are behind the `incremental_tools` flag. **Watch:** if this becomes the default, the bridge will need to handle tool-catalog update items.
- Agents SDK v0.23.1: no wire changes. **(d)**

### Competitors

- **LiteLLM v1.105.0** (2026-10-11):
  - Entra delegated auth for agents.
  - Fail-closed rate limiting when Redis is unreachable.
  - Per-member budget email alerts.
  - MCP tool-description pinning.
  - Credentials stripped from spend logs.
  - Nothing adopted. Two are worth a roadmap look: per-member budget alerts, and pinning MCP tool descriptions (rug-pull protection) for the MCP registry.
- **Portkey** and **Bifrost:** no confirmed release in the window.

### Security

- **Go 1.27.2 / 1.26.9** (2026-10-08): fixes in net/http (HTTP/2 HPACK, SETTINGS, flow control, CONNECT smuggling, Range), crypto/tls (ECH), net/textproto, html/template, os and cmd/go. **(b) Done:** #42 moves the toolchain to go1.27.2.
- **golang.org/x/text** GO-2026-6629 (fixed in v0.41.0). **(a)** The repo is on v0.42.0.
- **golang.org/x/net** advisory (2026-10-08). **(d)** x/net is not a dependency.
- **pgx:** no advisories.
- **npm:** no new advisories for vite 8.3, React 19.3 or TypeScript. Vite shipped backport releases on 2026-10-06/08 (8.3.3/8.3.4) with no advisory published. Dependabot (#21) covers the console group.

## 2026-10-26

Window: 2026-10-16 to 2026-10-26. The 2026-10-19 entry is in #53, which was still open when this run started.

### Anthropic

- **API version `2026-10-21`** (2026-10-21), the first new version since `2023-06-01`, which is not deprecated. [Release notes](https://platform.claude.com/docs/en/release-notes/overview), [migration guide](https://platform.claude.com/docs/en/api/migration-2026-10-21)
  - Messages, count_tokens and Batches return 400 for `temperature`, `top_p` and `top_k` (even at defaults), for `document` blocks in assistant messages, and for non-empty `citations` on user text blocks. Response shapes are unchanged.
  - The new SDKs (Python 2.0, TypeScript 1.0, Go 2.0, and the rest) send `anthropic-version: 2026-10-21` and `Authorization: Bearer` instead of `X-Api-Key`.
  - On Bedrock the new version is only for Claude 4.7 and later.
  - **(a)** The gateway accepts keys from `Authorization: Bearer`, forwards the client's `anthropic-version` on `/v1/messages` to Anthropic and Foundry, and keeps the platform body versions on Bedrock and Vertex. **(c) Done:** api.md documents this, and warns against pinning `anthropic_version=2026-10-21` on a provider that OpenAI-format clients reach with sampling parameters.
  - **(b) Done (#55):** the guardrail/complexity/rollout judge always sent `temperature: 0`. Claude 4.7 and later reject non-default sampling values, so any judge on Haiku 5.5, Sonnet 5.5 or Opus 5.5 failed every call. The judge now retries once without `temperature` when the upstream rejects it.
  - Admin API field renames (`role_ids`, `rbac_group_id`, `scope` on API keys, and others). **(d)** The gateway does not call the Admin API.
- **Claude Models on Google Cloud (Anthropic managed)** (2026-10-22): the Claude API operated by Anthropic, at `https://claude.googleapis.com/v1/projects/{project}/locations/global/workspaces/{workspace}/invoke`, authenticated with a Google OAuth access token (no API keys) and billed through Google Cloud Marketplace. Same body, headers, streaming, model IDs and Models API as the Claude API. [Docs](https://platform.claude.com/docs/en/build-with-claude/claude-models-on-google-cloud/anthropic-managed)
  - **(b) Done (draft #56):** `anthropic://` gains Google Cloud auth (`auth=metadata|workload_identity|service_account|google`, plus `project` and `workspace`), reusing the Vertex credential code. Requests are priced at Claude API list prices; Marketplace billing in Claude Consumption Units is assumed to match, which is not confirmed.
- **Fast mode for Opus 5.5** on the Google Cloud offering (2026-10-22). **(a)** Covered by the fast-mode pricing in #53.
- Models API `capabilities.thinking.types.between_tools` (2026-10-15). **(a)** Discovery reads only `id` and `display_name`.
- User profiles, enforcement fields, organization webhooks beta (`webhooks-2026-10-07`), Claude Tag API beta, inference hooks GA, web fetch org toggle (a request with the web fetch tool now returns 400 when an org turns it off), Managed Agents web restrictions, OpenTelemetry export beta. **(d)** Not gateway surfaces. Per-end-customer user profiles could later map to Theophylax principals; not adopted.
- No new models, content block types, streaming events or deprecations in the window.

### AWS Bedrock

- **Structured outputs for Claude** on `bedrock-runtime` and `bedrock-mantle` (2026-10-18). **(a)** `output_config`/`output_format` pass through in the InvokeModel body. [Document history](https://docs.aws.amazon.com/bedrock/latest/userguide/doc-history.html)
- Advanced Prompt Optimization, agentic retrieval instructions, Guardrails grounding tiers. **(d)**
- `2026-10-21` on Bedrock is limited to Claude 4.7+ (see Anthropic). Mantle is unchanged and still waiting on a decision.

### Google Vertex AI / Gemini

- Gemini API changelog: no entries after 2026-10-13 (Gemini 4 Argon, logged last week). [Changelog](https://ai.google.dev/gemini-api/docs/changelog)
- No changes for Claude on Vertex. The Anthropic-managed Google Cloud offering is separate from Vertex (see Anthropic).

### Azure

- No new Azure OpenAI api-version or retirement notice found in the window. The Foundry retirements logged on 2026-10-19 still apply: `claude-haiku-4-5` on **2026-11-15**, `claude-opus-4-5` on 2026-11-24, `claude-sonnet-4-5` on 2026-11-30.

### OpenAI

- **12-hour prompt caching** on `v1/responses` (2026-10-20), with cache writes billed at 2.5× input. **Watch:** the gateway prices OpenAI usage from `input_tokens_details.cached_tokens` and has no OpenAI cache-write rate; check the response usage fields and catalog keys once the parameter is documented. [Changelog](https://developers.openai.com/api/docs/changelog)
- Decisions API GA, project service-tier restrictions, prompt-to-voice, GPT-Live voices, Agents API ultrafast. **(d)**
- No new deprecations, Responses events or item types.
- **Codex CLI 0.164.0** (2026-10-23): proxy Negotiate/NTLM auth on Windows. No wire changes. `incremental_tools` is still not the default. **(d)**

### Competitors

- **LiteLLM 1.107.0 supply-chain compromise** (2026-10-21). Malicious wheels and images on PyPI, Docker Hub and GHCR carried a credential stealer. The attacker held an org-owner GitHub token from 2026-10-10 and overrode branch protection. [Datadog](https://securitylabs.datadoghq.com/articles/litellm-compromised-pypi-docker/), [The Hacker News](https://thehackernews.com/2026/10/leaked-litellm-master-key-let-attackers.html)
  - Theophylax runs no LiteLLM code, but **its default price catalog is `BerriAI/litellm` `main` fetched live**, so a tampered catalog could zero or inflate prices and with them budgets. Reports name no change to the catalog file. **Needs a decision:** pin `THEOPHYLAX_PRICING_URL` to a reviewed commit or mirror, or add a sanity check that rejects a refresh that drops or zeroes prices for models already priced.
  - The `theophylax import litellm` migration reads LiteLLM's config and database only; no LiteLLM code runs.
- **Bifrost v2.3.2** (2026-10-20): Go 1.27.3 for CVE-2026-111943. Matched by this run.
- **Portkey:** no release found.

### Security

- **Go 1.27.3 / 1.26.10** (2026-10-19): CVE-2026-111943, `crypto/x509` stops trusting a set of intermediates from one CA. It affects the gateway's outbound TLS to providers, IdPs and MCP servers. **(b) Done:** toolchain go1.27.3 (#54). The Docker build image `golang:1.27-alpine` follows the patch release. [Announcement](https://groups.google.com/g/golang-announce/c/66pojueaQek/m/tmWX2Pq9AgAJ)
- **pgx, x/text:** no new advisories. Dependabot #51 (x/text v0.43.0) is still open.
- **npm (vite, React, TypeScript):** no new advisories in the window. Dependabot #49 and #52 are still open.
