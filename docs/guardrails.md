# Guardrails

Policies attach to the organization, a team or a project and are evaluated in that order. Each rule chooses where it runs: **prompts (pre-call)**, **responses (post-call)** or both. Policies can run in **monitor** mode, which records what would have happened without acting. Every action appears in the request log and in the request's signed receipt (rule, action and counts — never the matched content).

| Rule | Pre-call | Post-call | Actions |
|---|---|---|---|
| `secrets` (Secret Shield) | ✓ | ✓ | redact (+restore), block, log |
| `pii` | ✓ | ✓ | redact, block, log |
| `prompt_injection` | ✓ | canary / exfiltration / tool-call guards | block, log |
| `keywords`, `regex` | ✓ | ✓ (streams terminated mid-flight) | block, redact, log |
| `model_allowlist` | ✓ | | block |
| `max_tokens` | ✓ | | cap, block |
| `max_input_chars` | ✓ | | block |
| `webhook` | ✓ | ✓ | your service answers allow / block / redact |
| `bedrock_guardrail` | ✓ | ✓ | AWS Bedrock ApplyGuardrail |

## Secret Shield

Detects cloud and SaaS credentials (AWS access and secret keys, Azure storage keys, SAS signatures and Entra client secrets, Google API keys, GitHub/GitLab/Slack/Stripe/npm/PyPI tokens, Anthropic and OpenAI keys), private keys, JWTs, credentialed connection strings, bearer headers and `password=`/`api_key:` style assignments (with entropy checks and exclusions for `${VARS}`, placeholders and identifiers). `high_entropy` is available opt-in for unknown token formats.

Each secret is replaced with `[REDACTED:<type>:<8 hex>]`, an HMAC of the value, so the same secret always maps to the same placeholder: multi-turn conversations stay coherent and prompt caches stay warm. Scanning covers system prompts, messages, tool results (file contents, command output) and arguments of earlier tool calls.

With **restore on return**, placeholders the model emits are replaced by the original values before the response leaves the gateway — in plain responses, streamed text deltas and streamed tool-call JSON (correctly JSON-escaped), with buffering across chunk boundaries. Only values detected in the same request can be restored. Captured payloads are stored *before* restoration, so logs never contain the secrets.

## Prompt injection

The native engine scores each new user message and tool result (or the whole conversation with `scope: all`):

1. **De-obfuscation** — NFKC normalisation (full-width and mathematical alphabets), Cyrillic/Greek homoglyph folding, leetspeak, spaced-out letters, zero-width and bidi stripping, decoding of Unicode *tag characters* (ASCII smuggling) and of base64, hex and rot13 payloads, which are re-scanned.
2. **Detector families** — instruction override, jailbreak personas, role hijack, system-prompt extraction, chat-template/delimiter injection, tool/agent hijack (`curl … | sh`, `rm -rf`, encoded shells), data exfiltration (including markdown-image beacons), secrecy toward the user, and multilingual overrides (ES, FR, DE, PT, IT, ZH, JA, RU, KO).
3. **Indirect injection** — tool results are untrusted: weights are raised and extra detectors look for text addressed to the AI ("Note to the assistant: …").
4. **Scores** combine with noisy-OR into 0–1 risk with named categories. Default threshold 0.5.
5. **Judge** — `detector: hybrid` sends only grey-zone texts to a model you choose (routed through the gateway); `judge` classifies every scan. A confident "safe" verdict clears grey-zone false positives.

Post-call defences on the same rule:

- `canary` — a marker stable per project per UTC day is added to the system prompt (so prompt caching keeps working); responses or streams that reveal it are blocked.
- `exfil_guard` — blocks links/images that carry data in query strings to non-allow-listed domains.
- `tool_guard` — if untrusted input showed injection signals, risky tool calls in the response (pipe-to-shell, destructive deletes, posting env/secret files, force pushes) are blocked, in streams too.

Start in monitor mode, review the requests marked by guardrails, then enforce.

## Hosted classifiers

| Rule type | Calls | Configure |
|---|---|---|
| `lakera` | Lakera Guard v2 | `api_key: ${env:LAKERA_API_KEY}`, optional `project_id` |
| `azure_prompt_shield` | Azure AI Content Safety Prompt Shields (user prompt and up to five tool results as documents) | `url` = your Content Safety endpoint, `api_key` |
| `azure_content_safety` | Content Safety text analysis | `url`, `api_key`, `max` = blocking severity (2, 4 or 6; default 4) |
| `openai_moderation` | OpenAI omni-moderation or a compatible endpoint | `api_key`, optional `url` and `model` |
| `bedrock_guardrail` | AWS Bedrock ApplyGuardrail through an existing Bedrock connection | `provider_id`, `guardrail_id`, `version` |
| `webhook` | your own service (contract below); `headers` may use `${env:}` | `url`, `headers` |

API keys must be `${env:NAME}` or `${file:/path}` references so policies never contain secrets. All are fail-open by default (`fail_open: false` to block when the service is down) with `timeout_ms` (default 2000). They run on input and on non-streamed output; streamed output is checked by the native rules.

## How well it works

Detection is measured on a labelled corpus (`internal/guardrails/testdata/injection_corpus.jsonl`, generated by `gen_corpus.py`): attacks across override, extraction, jailbreak, template, payload-splitting, indirect (README, web page, tool JSON, code comment, email, issue), exfiltration and tool-hijack intents, ten obfuscations and fourteen languages, plus hard negatives from coding-agent traffic (code, logs, install docs, security writing, and users legitimately telling the assistant to ignore something). The detector is tuned on the dev split only; the test split is held out.

| Split | Recall | False positives |
|---|---|---|
| dev (tuned on) | 100% (66/66) | 0% (0/32) |
| **test (held out)** | **91% (64/70)** | **0% (0/28)** |

On 308 real third-party READMEs and package descriptions scanned as untrusted tool output, 2.3% were flagged, and every hit was a document containing prompt templates or jailbreak examples. CI fails if either split drops below 88% recall or rises above 4% false positives. Treat the native engine as a strong first layer: pair it with `tool_guard`, `canary`, a judge or a hosted classifier for high-risk agents.

## Webhook contract

```http
POST <url>
{"phase":"input","model":"…","texts":["…"],"metadata":{"org_id":"…","project_id":"…","request_id":"…"}}

200 {"action":"allow"}  |  {"action":"block","message":"…"}  |  {"action":"redact","texts":["…"]}
```

`fail_open` (default true) decides what happens when the webhook is down or slow (`timeout_ms`, default 2000).
