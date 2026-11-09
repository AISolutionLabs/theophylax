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

## 2026-10-19

Window: 2026-10-09 to 2026-10-19.

### Anthropic

- **Fast mode is out of research preview for Opus 5.5** (2026-10-15). Requests to `claude-opus-5-5` no longer need the `fast-mode-2026-02-01` beta header, which Opus 5 and Opus 4.8 still require. Foundry supports it for Azure-hosted Opus 5.5 deployments. It is billed at $8/$40 per MTok, twice the standard Opus 5.5 rate, and responses report `usage.speed` as `"fast"` or `"standard"`. [Release notes](https://platform.claude.com/docs/en/release-notes/overview), [fast mode](https://platform.claude.com/docs/en/build-with-claude/fast-mode)
  - **(a)** `speed` and the beta header pass through on `/v1/messages`.
  - **(b) Done:** fast-mode requests were priced at standard rates, which undercounted spend and budgets by half. The gateway now reads `usage.speed` and applies the catalog's `provider_specific_entry.fast` multiplier to every rate. Foundry deployments use Anthropic's multiplier.
  - Fast mode has its own rate limit, separate from standard Opus. Exceeding it returns 429 or 529 with `anthropic-fast-*` rate-limit headers, and failover treats those like any other 429.
- OpenTelemetry export beta (10-14), Compliance API user preferences (10-15), Managed Agents dynamic workflows (10-09, `workflow_run.*` events on the session stream). **(d)**
- **No new deprecations.** `claude-haiku-4-5` passed its "not sooner than" date (2026-10-15) with no notice, so the earliest it can retire on the Claude API is mid-December under the 60-day policy. No new SSE events, content block types or stop reasons for the Messages API.
- **Fable 5.1 / Mythos 5.1** (September, not logged before; `claude-fable-5-1`, Bedrock `anthropic.claude-fable-5-1`; Mythos is limited to verified organizations). **(a)** Discovered and priced through the catalog, and their betas pass through on `/v1/messages`:
  - `thinking-binding-controls-2026-08-01`: the top-level `input_transformations` response field.
  - `mid-conversation-output-config-2026-07-01` and `mid-conversation-system-clear-at-2026-08-21`: `role:"system"` messages.
  - `thinking-display-updates-2026-08-18`.
  - The Responses bridge does not emit mid-conversation system messages.

### AWS Bedrock

- No doc-history or What's New entries in the window. Mantle is unchanged.
- **Input for the pending Mantle decision:** since August 2026, AWS recommends `bedrock-runtime` for new applications. That includes an Anthropic Messages route at `POST https://bedrock-runtime.{region}.amazonaws.com/anthropic/v1/messages`, which accepts SigV4 or a Bedrock API key in `x-api-key`. That route would let the gateway reuse its Anthropic transport rather than add a Mantle one. [Endpoints](https://docs.aws.amazon.com/bedrock/latest/userguide/endpoints.html)

### Google Vertex AI / Gemini

- **Gemini 4 Argon GA** (`gemini-4-argon`, 2026-10-13) on the Gemini API and Agent Platform. [Changelog](https://ai.google.dev/gemini-api/docs/changelog)
  - It silently ignores `temperature`/`top_p`/`top_k` and has no explicit caching.
  - **(a)** Priced in the catalog, and `reasoning_effort` maps to `thinking_level` on the OpenAI-compatible endpoint.
  - **(c) Noted here only:** for long decodes Google recommends HTTP timeouts of at least 15 minutes. `THEOPHYLAX_UPSTREAM_TIMEOUT` defaults to 10 minutes, so raise it if deployments route long Argon generations.
- **Vertex `gemini-2.5-pro` / `-flash` / `-flash-lite`** were due to retire "not before 2026-10-16", and the catalog no longer lists `vertex_ai/gemini-2.5-pro`. No shutdown notice was found. **(a)** The model watcher flags unpriced deployments. Repoint any that remain.
- Gemini API shutdowns on 2026-10-22 (Veo 3.1 previews, `gemini-omni-flash-preview`) and 2026-11-17 (TTS and live previews). **(d)** These are not on the gateway's surfaces.
- No changes for Claude on Vertex. **(a)**

### Azure

- No new Azure OpenAI api-version and no api-version retirement. **(d)**
- **Foundry retires Claude 4.5 models before Anthropic does:** `claude-haiku-4-5` on **2026-11-15**, `claude-opus-4-5` on 2026-11-24 and `claude-sonnet-4-5` on 2026-11-30. Later: `claude-opus-4-6` on 2027-02-02 and `claude-sonnet-4-6` on 2027-02-10. [Retirement schedule](https://learn.microsoft.com/en-us/azure/foundry/openai/concepts/model-retirement-schedule)
  - **(a)** The catalog carries `azure_ai/claude-haiku-4-5` `deprecation_date` 2026-11-15, so the model watcher flags it. Deployments must be repointed.
- Fast mode for Opus 5.5 on Foundry: see Anthropic above.

### OpenAI

- **Deprecation (2026-10-14):** `gpt-4o-2024-08-06`, `gpt-4o-2024-11-20`, `gpt-4o-mini-2024-07-18`, `gpt-4.1-2025-04-14` and `gpt-4.1-mini-2025-04-14` shut down on **2027-01-14** (replacement `gpt-6-luna`). Earlier notices (10-01): `gpt-5.3-codex`, `gpt-5.1` and `gpt-5.4-nano` shut down on 2027-04-01, and `tts-1`/`tts-1-hd` on 2027-01-06. **(a)** These are catalog and model-watch concerns. [Deprecations](https://developers.openai.com/api/docs/deprecations)
- GPT-Live → Decisions API, and prompt-to-voice (`v1/audio/voices`). **(d)**
- No new Responses streaming events, item types or parameters.
- **Codex CLI 0.163.0** (2026-10-15):
  - Incomplete responses are now terminal errors, and their usage is counted. **(a)** The bridge emits `response.incomplete` with usage when `stop_reason` is `max_tokens`.
  - Opt-in output-token replay, retention annotations on tool outputs, and V2 remote compaction. **(a)** Unknown fields are ignored by the bridge and passed through by the relay.
  - **Watch:** `incremental_tools` is still not the default.
- Agents SDK v0.24.0: no wire changes. **(d)**

### Competitors

- **LiteLLM** v1.105.1, v1.106.0 and v1.107.0 dev/rc builds:
  - It now checks that the caller owns a `previous_response_id` (on `/responses/compact`).
  - Disables stdio MCP servers by default.
  - Scopes `/spend/tags` to the caller.
  - Adds default-deny agent access.
- **Bifrost Enterprise v2.3.0:** business-unit ownership of keys, detect-only guardrails, bounded request decompression, Responses `shell_call`/`apply_patch_call`.
- **Portkey:** no release.
- **Needs a decision: `previous_response_id` ownership.** Theophylax relays `previous_response_id` (and `conversation`) to OpenAI-format providers unchanged. When several tenants share one upstream credential, a client that learns another tenant's response ID could continue that conversation. Response IDs are unguessable and returned only to their creator, so this is low risk. A fix needs a table mapping response IDs to the principal that created them, which is a security-model change and should go through an issue first.
- Nothing else adopted. Per-member budget alerts and MCP description pinning are still on the list from 2026-10-12.

### Security

- **Go:** no release after 1.27.2/1.26.9.
- **golang.org/x/net** HTTP/2 advisories (2026-10-08). **(d)** x/net is not a dependency.
- **pgx:** no new advisories.
- **x/text:** Dependabot #51 bumps it to v0.43.0. No advisory was found for it.
- **React** GHSA-xqc2-xf4c-x3h3 (critical RCE) and GHSA-h3gv-c5rc-p453 (DoS), 2026-10-14. **(d)** They affect only `react-server-dom-*` (Server Components). The console is a client-only Vite SPA and uses only `react`/`react-dom`. Dependabot #52 moves React within the console group.
- **vite, @vitejs/plugin-react, typescript:** nothing new.

### Follow-ups not implemented

- **Service-tier pricing:** OpenAI `service_tier` priority/flex/ultrafast and Gemini priority/flex are listed in the catalog as `*_priority`, `*_flex` and `*_ultrafast` keys, but requests are billed at default rates. Supporting them means reading the response's `service_tier` and adding rate keys: the same mechanism as fast mode, but a separate change.
- **Data-residency multipliers:** Anthropic `provider_specific_entry.us` (1.1×) is not applied.

## 2026-10-26

Window: 2026-10-16 to 2026-10-26.

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

## 2026-11-02

Window: 2026-10-23 to 2026-11-02. The 2026-10-19 and 2026-10-26 entries were moved here from #53 and #54, because each open upstream-watch PR appended to this file and the second to merge would conflict. From now on the log goes in a single docs-only PR each week.

### Anthropic

- **Request size limit raised from 32 MB to 128 MB** for the Messages and Token Counting APIs on the first-party Claude API (2026-10-28). [Release notes](https://platform.claude.com/docs/en/release-notes/overview), [request size limits](https://platform.claude.com/docs/en/api/overview#request-size-limits)
  - **(c) Done:** `THEOPHYLAX_MAX_BODY_MB` still defaults to 32 and applies to every provider, so a request between 32 and 128 MB gets a 413 from the gateway. operations.md now says so and explains the trade-off. The default stays at 32: the gateway reads each body into memory, and Bedrock, Vertex and the OpenAI-format providers have their own, mostly smaller, limits.
- **Fast mode deprecated for Claude Opus 4.8** (2026-10-30). Removal is on **2026-11-30**. After that, requests to `claude-opus-4-8` with `speed: "fast"` return an error. The replacement is Opus 5.5. [Fast mode](https://platform.claude.com/docs/en/build-with-claude/fast-mode#supported-models)
  - **(a)** The gateway never sets `speed`; clients do, and it passes through. Clients that still send `speed: "fast"` to an Opus 4.8 route must move to Opus 5.5. The fast-mode billing in #53 is unaffected.
  - On Foundry, the `claude-opus-5-5-fast` deployment retires on 2027-01-15.
- Usage and Cost Analytics API beta (`analytics-usage-cost-2026-08-24`, 2026-10-29). The older Usage and Cost API is now legacy. **(d)** The gateway does not call either API.
- Claude Tag API beta (`experimental-claude-tag-2026-10-21`, 2026-10-23, Claude Enterprise only). **(d)**
- No new models, content block types, streaming events, stop reasons or model deprecations. The model-deprecations page is unchanged since 2026-09-30. `claude-haiku-4-5` is still active with no notice.
- **Unconfirmed:** a [FourWeekMBA article](https://fourweekmba.com/ai-anthropic-to-watermark-4-older-claude-models-for-eu-ai-act/) (2026-11-01) says Anthropic will apply its text watermark to Mythos 5, Opus 4.7, Opus 4.6 and Sonnet 4.6 from 2026-11-03. Its only source is a screenshot of a customer email. Anthropic has published nothing. **(d)** It would not change the API. Revisit if it appears in the release notes.

### AWS Bedrock

- No Claude, InvokeModel, inference-profile or auth entries in the [document history](https://docs.aws.amazon.com/bedrock/latest/userguide/doc-history.html) after the structured-outputs entry (2026-10-18), which was logged on 2026-10-26.
- Batch inference now gives on-demand traffic priority and does not guarantee completion; the job timeout defaults to 24 hours, with a maximum of 168 (2026-10-26). **(d)** Gateway batches run as on-demand requests.
- Mantle is unchanged and still waiting on a decision.

### Google Vertex AI / Gemini

- **Gemini API: request storage on by default for new paid-tier projects** (2026-10-26). New projects on the paid tier store `generateContent`, `streamGenerateContent` and `batchGenerateContent` requests unless the request sets `store=false` or the project turns off logging. Existing projects keep their setting. [Changelog](https://ai.google.dev/gemini-api/docs/changelog)
  - **(c), needs a check:** the entry names only the native methods. It does not say whether the OpenAI-compatible endpoint the gateway uses is affected. Operators who create a new Gemini API project should turn off Generate Content API logging in its settings if they do not want prompts stored. Not adopted in code: there is no documented way to send `store=false` through the OpenAI-compatible endpoint.
- Gemini Computer Use tool GA, `gemini-api` CLI beta. **(d)** These are native-API features.
- Agent Platform (Vertex), 2026-10-26 to 2026-10-30:
  - Inference request metadata logging GA, which covers Claude `rawPredict`/`streamRawPredict`, with optional `x-goog-agent-platform-observability-label-*` headers.
  - Semantic governance policies now also evaluate Claude and the OpenAI-compatible endpoint.
  - Grok 4.7 GA.
  - **(d)** No change for Claude on Vertex. Attribution labels could later carry Theophylax principals; not adopted. [Release notes](https://docs.cloud.google.com/gemini-enterprise-agent-platform/release-notes)

### Azure

- No new Azure OpenAI api-version and no new retirement notice. The [retirement schedule](https://learn.microsoft.com/en-us/azure/foundry/openai/concepts/model-retirement-schedule) was last updated 2026-09-23.
- Reminder: **`claude-haiku-4-5` on Foundry retires on 2026-11-15**, `claude-opus-4-5` on 2026-11-24 and `claude-sonnet-4-5` on 2026-11-30. Azure OpenAI `o1`, `o3`, `o3-pro`, `o4-mini` and `o3-mini` retire on 2026-11-19, and `codex-mini` on 2026-11-15.

### OpenAI

- New models: `gpt-6.1-terra` (2026-10-26, Responses and Chat Completions) and `gpt-6-rosalind` (2026-10-30, trusted-access only). **(a)** Discovered and priced through the catalog. [Changelog](https://developers.openai.com/api/docs/changelog)
- **Shutdown on 2026-10-23** (from the 2026-04-22 notice): `gpt-3.5-turbo*`, `gpt-4`, `gpt-4-0613`, `gpt-4-turbo`, `gpt-4o-2024-05-13`, `o1`, `o1-pro`, `o3-mini`, `o4-mini`, `gpt-4.1-nano` and `gpt-image-1`. **(a)** These are catalog and model-watch concerns. Any deployment still pointing at them now fails and should be repointed. [Deprecations](https://developers.openai.com/api/docs/deprecations)
- Decisions API GA, service-tier controls per project, and API credits for subscribers. **(d)**
- 12-hour prompt caching (2026-10-20, logged last week): the changelog still names no request parameter or usage field. **Watch** continues.
- Codex CLI: no release after 0.164.0 found on GitHub. `incremental_tools` is still not the default.

### Competitors

- **LiteLLM** stable patch releases v1.103.8 through v1.106.6 (2026-10-21 to 2026-10-28). They rotated the image-signing key on 2026-10-24 after the supply-chain compromise and removed the eMudhra root CA. Pre-releases v1.108.0-rc.1 and v1.109.0-dev.1 add mid-stream fallback continuation, `LITELLM_OFFLINE_MODE` and `model_info.pricing_provider`. [Releases](https://github.com/BerriAI/litellm/releases)
  - No statement about the price catalog file. The decision from 2026-10-26 (pin `THEOPHYLAX_PRICING_URL` or add a sanity check on refresh) is still open.
  - Mid-stream fallback continuation is worth a roadmap look. Theophylax fails over only before the first byte is streamed.
- **Bifrost:** nothing after v2.3.2 (2026-10-20, logged). **Portkey:** no release.

### Security

- **Go:** no release after go1.27.3/go1.26.10.
- **golang.org/x/net** GO-2026-6860 and GO-2026-6866 (2026-10-27) affect `x/net/html`. **(d)** x/net is not a dependency.
- **pgx:** no new advisories.
- **npm:** GitHub reports 9 open Dependabot alerts on the default branch (1 high, 8 moderate). The console on `main` is still on vite 5.4. Merging the vite 8 bump (#57, now fixed to build, or #49) clears the vite/esbuild alerts. #52 (the console group, including React 19 and TypeScript 7) also passes CI.

## 2026-11-09

Window: 2026-10-30 to 2026-11-09.

**Coverage gaps this run.** The fetch tool could not read these pages, so they are **not reviewed** and next week's run should cover them for this window too:

- the Claude release notes
- the Bedrock document history
- the Gemini API changelog
- the Agent Platform release notes
- the Azure retirement schedule

The findings below come from the pages it could read. The Go toolchain could not be downloaded either (go1.27.2 from proxy.golang.org was blocked, and the local Go is 1.24), so no code change could be built or tested. This run therefore opened no code PRs.

### Anthropic

- **Model deprecations page** ([deprecations](https://platform.claude.com/docs/en/about-claude/model-deprecations)):
  - No new notices. The latest entry is still 2026-09-30: `claude-sonnet-4-5-20250929` retires on **2026-11-30**.
  - `claude-haiku-4-5-20251001` is still Active with no notice. Under the 60-day policy it cannot retire before early January 2027.
  - `claude-opus-4-5-20251101` is Active ("not sooner than 2026-11-24"), with no notice.
- **Models overview:** no new model IDs. The current models are `claude-fable-5-1`, `claude-opus-5-5`, `claude-sonnet-5-5` and `claude-haiku-5-5`, plus `claude-mythos-5-1`. **(a)**
- **Beta headers page:** nothing new. The newest is still `agent-memory-2026-07-22`. **(a)**
- **Watermark rumour (2026-11-01 entry): not confirmed.** The [text watermark docs](https://platform.claude.com/docs/en/build-with-claude/text-watermark) give no dates and no API fields, and say requests need no changes. **(d)**
- **Not reviewed:** release-note entries in the window (new SSE events, content blocks, tool types, stop reasons, usage fields).

### AWS Bedrock

- [What's New](https://aws.amazon.com/about-aws/whats-new/recent/feed/) has no Claude, InvokeModel, inference-profile, auth or Mantle items in the window. **(d)** for the window's entries:
  - Kimi K3.1 Pro (11-06)
  - AgentCore Web Answer (11-05)
  - OpenAI models in GovCloud (10-30)
- The document history was not reviewed.
- Mantle is still waiting on a decision.

### Google Vertex AI / Gemini

- The [Vertex AI release notes](https://docs.cloud.google.com/vertex-ai/docs/release-notes) have only Workbench maintenance entries in the window. **(d)**
- The [Gemini deprecations page](https://ai.google.dev/gemini-api/docs/deprecations) is unchanged since 2026-10-22. TTS and live previews shut down on 2026-11-17. **(d)**
- Not reviewed: the Gemini API changelog and the Agent Platform release notes.

### Azure

- The [Claude in Foundry page](https://learn.microsoft.com/en-us/azure/foundry/foundry-models/concepts/claude-models) is unchanged since 2026-09-22.
- The main retirement schedule was not reviewed. The dates logged on 2026-10-19 are assumed to still hold:
  - `claude-haiku-4-5` on **2026-11-15**
  - `claude-opus-4-5` on 2026-11-24
  - `claude-sonnet-4-5` on 2026-11-30
  - Azure OpenAI `o1`/`o3`/`o3-pro`/`o4-mini`/`o3-mini` on 2026-11-19
  - `codex-mini` on 2026-11-15
- The Azure Government schedule also still lists `o3-mini` retiring on 2026-11-19.

### OpenAI

- **12-hour prompt caching is now documented** in the [prompt caching guide](https://developers.openai.com/api/docs/guides/prompt-caching), which is undated. This resolves the **Watch** item from 2026-10-26.
  - The request parameter is `prompt_cache_options.ttl` (`"30m"`, the default, or `"12h"`). It applies to the Responses API on GPT-5.6 and later, and a breakpoint can override it with `prompt_cache_breakpoint.ttl`.
  - Usage reports `input_tokens_details.cache_write_tokens` next to `cached_tokens`.
  - Cache writes are billed at 1.25× input for 30m and 2.5× for 12h; reads at 0.1×, or 0.05× for GPT-6.1 Sol/Terra.
  - **(b), deferred:** `parseAnyUsage` (internal/gateway/relay.go) and the OpenAI translators read only `cached_tokens`, so cache writes are billed as plain input. That undercounts spend and budgets by 25% to 150% of the written tokens.
  - The fix is to map `cache_write_tokens` to `Usage.CacheWrite`, subtract it from `Input`, and price it at the catalog's cache-creation rate (or 1.25×/2.5× input when the catalog has no key for it).
  - It was not implemented because no Go toolchain was available to test it, and it is unconfirmed whether the LiteLLM catalog carries an OpenAI cache-write key. Next run.
- New models:
  - `gpt-6-cyber` (11-05)
  - `gpt-image-2.6-sunburst` and `gpt-image-2.6-flare` (11-04, images endpoints; SVG/GIF output)
  - **(a)** Discovered and priced through the catalog. [Changelog](https://developers.openai.com/api/docs/changelog)
- Agents API (10-30): background compaction is now the default (`compaction.async`); also new are `experimental.time_awareness` and `review: "auto_review"`. Outbound SIP on the Live API (11-02). **(d)** These are not gateway surfaces.
- **Deprecation:** `gpt-5.5-cyber-preview` shuts down on 2027-02-01 (announced 11-05). **(a)** [Deprecations](https://developers.openai.com/api/docs/deprecations)
- No new Responses streaming events or item types.
- **Codex:**
  - 0.165.0 (Oct 27) adds GPT-6.1 Terra to the model catalog and has no wire change. **(d)**
  - [openai/codex#59832](https://github.com/openai/codex/pull/59832) (merged 11-06) adds a separate `incremental_tools_legacy` flag, off by default. It carries tool declarations and updates in conversation history as `additional_tools` items.
  - **Watch:** if it becomes the default, the Responses→Messages bridge must fold `additional_tools` items into `tools`.
  - `incremental_tools` still applies only to Responses Lite.
- Agents SDK: 0.25.0 is not released yet. Its open release PR has no Responses wire changes. **(d)**

### Competitors

- **LiteLLM** ([release notes](https://docs.litellm.ai/release_notes/)):
  - v1.108.0 stable (11-04): shared Azure PTU capacity across teams, `/v1/live/sessions`, and count routes return the provider's error instead of a local estimate.
  - v1.108.1 (11-05): Bedrock guardrail traces counted in spend.
  - v1.108.2 (11-06): an empty `api_key` no longer falls back to the admin key, and Bedrock service tiers are enforced.
  - Nothing adopted. Count routes are relevant: Theophylax estimates `count_tokens` for non-Anthropic providers on purpose, so that stays.
  - Still no statement on the integrity of the price catalog file. **The decision from 2026-10-26 (pin `THEOPHYLAX_PRICING_URL` or sanity-check refreshes) is still open.**
- **Bifrost**, **Portkey:** no release found in the window. This is unverified, because the GitHub release pages may have been stale.

### Security

- **Go 1.27.4 / 1.26.11 are pre-announced for 2026-11-10**, with private security fixes for 12 CVEs (CVE-2026-97033 to -97043 and -97054) in the standard library and toolchain. The affected packages are not yet named. [oss-security](https://www.openwall.com/lists/oss-security/2026/11/05/13) **(b), next run:** bump the `toolchain` line to go1.27.4 once it is released.
- **pgx:** no advisories. Dependabot #59 bumps it to v5.12.0, a minor release with no advisory.
- **npm:** no new advisories in the window for vite, React, TypeScript, @vitejs/plugin-react or esbuild. Downstream repos cite two vite advisories, GHSA-8g5g-mxqh-grgj (fixed in 6.4.5) and GHSA-587h-7wmj-h5pm, which could not be opened. If they apply to vite 5.4, the console on `main` is affected; #57 (vite 8.3.5) would clear them.

### Backlog

- These upstream-watch PRs are still open, unmerged:
  - #53 fast-mode billing
  - #54 Go 1.27.3
  - #55 judge temperature retry
  - #56 Claude on Google Cloud (draft)
  - #58 log
- This entry is stacked on #58 for the same reason as before: each log PR appends to this file.
- **Timing:** #54's toolchain bump will be superseded by go1.27.4. Merging #54 now still closes CVE-2026-111943.
