-- Per-person budgets. An org or team budget with per_user=true is a default
-- limit for each person, not a pooled cap. A person's effective limit for a
-- period is, in order: their own budget (scope_type='user'), their team's
-- per-person default, the org's per-person default. Pooled budgets still apply.

ALTER TABLE budgets ADD COLUMN per_user boolean NOT NULL DEFAULT false;
ALTER TABLE budgets ADD CONSTRAINT budgets_per_user_scope CHECK (NOT per_user OR scope_type IN ('org','team'));
ALTER TABLE budgets DROP CONSTRAINT budgets_scope_type_scope_id_period_key;
ALTER TABLE budgets ADD CONSTRAINT budgets_scope_period_key UNIQUE (scope_type, scope_id, period, per_user);

-- Personal spend is counted per org ('org_user', '<org>/<user>') and per team
-- ('team_user', '<team>/<user>'). Seed the counters from retained logs.
INSERT INTO usage_counters (scope_type, scope_id, period, period_start, cost_usd, tokens, requests)
SELECT c.scope_type, c.scope_id, p.period,
       CASE p.period WHEN 'daily' THEN (l.ts AT TIME ZONE 'UTC')::date
                     WHEN 'monthly' THEN date_trunc('month', l.ts AT TIME ZONE 'UTC')::date
                     ELSE DATE '1970-01-01' END,
       sum(l.cost_usd), sum(l.input_tokens + l.output_tokens + l.cache_read_tokens + l.cache_write_tokens), count(*)
FROM request_logs l
CROSS JOIN LATERAL (VALUES ('org_user', l.org_id || '/' || l.user_id), ('team_user', l.team_id || '/' || l.user_id)) c(scope_type, scope_id)
CROSS JOIN (VALUES ('daily'), ('monthly'), ('total')) p(period)
WHERE l.user_id IS NOT NULL AND c.scope_id IS NOT NULL
  AND l.input_tokens + l.output_tokens + l.cache_read_tokens + l.cache_write_tokens > 0
  AND (p.period = 'total'
       OR (p.period = 'monthly' AND l.ts >= date_trunc('month', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
       OR (p.period = 'daily' AND l.ts >= date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'))
GROUP BY 1, 2, 3, 4
ON CONFLICT (scope_type, scope_id, period, period_start) DO NOTHING;
