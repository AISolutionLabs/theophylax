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
