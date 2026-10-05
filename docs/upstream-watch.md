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
