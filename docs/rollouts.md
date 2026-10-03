# Eval-gated model rollouts

A rollout migrates one public model (what clients call) to a candidate deployment — a new model version, a different region or a different provider — without changing any client.

1. **Shadow** — `shadow_pct` of successful live requests are replayed (non-streaming, after guardrails and redaction) to the candidate. A judge model compares the incumbent's and candidate's answers to the same task with the A/B order randomised per sample to cancel position bias, and records win / tie / loss with a reason, plus latency and cost of both.
2. **Canary** — `canary_pct` of real traffic is served by the candidate first (with normal failover to the incumbents), watching error rate.
3. **Promote** — the candidate becomes the primary deployment; existing deployments drop 10 priority levels and remain as failover.

Automatic decisions (each one is written to the audit chain):

| Stage | Advance when | Roll back when |
|---|---|---|
| Shadow | ≥ `min_samples` judged, win-or-tie ≥ `win_threshold`, candidate errors ≤ `max_error_rate`, latency ≤ `max_latency_ratio` × incumbent | win-or-tie < threshold − 15 pts, or errors > 2 × limit |
| Canary | ≥ `min_samples` requests and errors ≤ `max_error_rate` | ≥ 5 errors and rate > 2 × limit (early), or > limit at the end |

Pause, resume, advance, promote or roll back manually from the console or `POST /api/rollouts/{id}/{action}`.

Shadow traffic costs money and sends (redacted) prompts to the candidate and judge providers — make sure both are approved for the data class. Shadow spend is tracked on the rollout, not on user budgets.
