# Complexity routing

A **complexity router** is a catalog model that has no deployments of its own. Each request to it is scored from 0 (trivial) to 1 (hardest) and sent on to one of its tier models. Simple questions go to a small, fast, cheap model, and hard reasoning or coding goes to the strongest one. Clients just ask for the router's name, for example `model: "auto"`.

## Setting one up

In the console, open a model (or add one named, say, `auto`), choose **Complexity router** as its routing strategy, and pick a model for each tier. Through the API:

```sh
curl -X POST "$GW/api/models" -H "Authorization: Bearer $TPXP" -d '{
  "name": "auto", "strategy": "complexity",
  "routing": {"tiers": [
    {"max_score": 0.2, "model": "claude-haiku"},
    {"max_score": 0.5, "model": "claude-sonnet"},
    {"model": "claude-opus"}
  ]}
}'
```

Rules for the tiers:

- A router has 2–5 tiers. Each tier except the last sets a `max_score`, and those values increase strictly between 0 and 1. The last tier takes everything harder.
- Tier models must already be in the catalog, and a router cannot route to another router.
- A model that a router depends on can't be deleted until the router stops using it.

The thresholds above (0.2 / 0.5) are a reasonable start. Watch `X-Theophylax-Complexity` and `theophylax_complexity_routed_total{router,model}` for a few days, then adjust them.

## How requests are scored

The default `heuristic` classifier looks only at the request, so routing adds no latency and no cost, and the same request always goes to the same model. It adds up these signals:

| Signal | Effect |
|---|---|
| Length of the latest user message (log scale) | up to +0.30 |
| Size of the whole conversation (log scale) | up to +0.15 |
| Code in the ask, or a coding task ("write a function…", "fix the test…") | +0.15 |
| Reasoning cues (analyze, design, trade-offs, debug, root cause, race condition, step by step…) | +0.09 each, up to +0.36 |
| Maths (LaTeX, integrals, theorem…) | +0.10 |
| A short request that starts like a simple one (greeting, translate, summarize, fix typo, what is…) | −0.15 |
| Tools offered (more than 5 counts double) | +0.05 / +0.10 |
| Three or more tool results in the conversation (an agent loop) | +0.10 |
| Images, documents or audio attached | +0.05 |
| Extended thinking or `reasoning_effort` medium/high requested | +0.25 |
| `max_tokens` of 8,000 or more | +0.10 |

The score is clamped to 0–1. As a rough guide, "hi, what is the capital of France?" scores 0.00, "Explain how TCP congestion control works" 0.13, "Write a Python function that parses ISO 8601 durations, with tests" 0.32, and "Design a multi-region active-active architecture…, analyze failure modes, compare trade-offs and plan the migration step by step" 0.55.

With `"classifier": "hybrid"` and a `judge_model`, a request whose heuristic score lands within 0.1 of a tier boundary is also rated by the judge model, and the judge's rating decides. A small, fast model is enough for this. If the judge fails or takes more than 4 seconds, the heuristic score is used.

## What you see

- **Response headers.** `X-Theophylax-Complexity: 0.32; classifier=heuristic; model=claude-sonnet`, and `X-Theophylax-Model` names the model that served the request.
- **Request logs and usage.** These record the model that served the request, so spend and chargeback stay accurate per model.
- **Receipts.** The `route.router` field holds `{model, score, classifier, tier}`, so the routing decision is signed along with the rest.
- **Everything else is unchanged.** The chosen model's own deployments, strategy, failover, guardrails, budgets, response cache and rollouts all apply as if the client had asked for it directly.

## Allow-lists

- Model allow-lists on keys, organizations, teams and projects must admit the router's name.
- A list that admits the router but names none of its tier models leaves the choice to the router.
- A list that names some tier models caps the router at those. A request scored for a tier the caller may not use goes to the nearest allowed tier below it, or else above it. For example, a project allowed `auto` and `claude-sonnet` never reaches `claude-opus` through the router.

## Configuration as code

Routers export and apply like any other model with [`theophylax config`](config-as-code.md). Their `routing` block is kept, and routers are always created after the models they route to.
