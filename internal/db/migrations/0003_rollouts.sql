-- Eval-gated model rollouts: shadow → canary → promoted (or rolled back).

CREATE TABLE model_rollouts (
  id                     text PRIMARY KEY,
  model_id               text NOT NULL REFERENCES models(id) ON DELETE CASCADE,
  name                   text NOT NULL DEFAULT '',
  candidate_provider_id  text NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  candidate_upstream     text NOT NULL,
  judge_model            text NOT NULL DEFAULT '',
  stage                  text NOT NULL DEFAULT 'shadow' CHECK (stage IN ('shadow','canary','paused','promoted','rolled_back')),
  paused_stage           text NOT NULL DEFAULT '',
  shadow_pct             integer NOT NULL DEFAULT 10 CHECK (shadow_pct BETWEEN 0 AND 100),
  canary_pct             integer NOT NULL DEFAULT 10 CHECK (canary_pct BETWEEN 0 AND 100),
  min_samples            integer NOT NULL DEFAULT 50,
  win_threshold          double precision NOT NULL DEFAULT 0.5,
  max_error_rate         double precision NOT NULL DEFAULT 0.05,
  max_latency_ratio      double precision NOT NULL DEFAULT 1.5,
  auto_promote           boolean NOT NULL DEFAULT true,
  auto_rollback          boolean NOT NULL DEFAULT true,
  samples                integer NOT NULL DEFAULT 0,
  wins                   integer NOT NULL DEFAULT 0,
  ties                   integer NOT NULL DEFAULT 0,
  losses                 integer NOT NULL DEFAULT 0,
  judge_errors           integer NOT NULL DEFAULT 0,
  cand_requests          integer NOT NULL DEFAULT 0,
  cand_errors            integer NOT NULL DEFAULT 0,
  cand_latency_ms        bigint NOT NULL DEFAULT 0,
  inc_latency_ms         bigint NOT NULL DEFAULT 0,
  cand_cost              double precision NOT NULL DEFAULT 0,
  inc_cost               double precision NOT NULL DEFAULT 0,
  canary_requests        integer NOT NULL DEFAULT 0,
  canary_errors          integer NOT NULL DEFAULT 0,
  canary_latency_ms      bigint NOT NULL DEFAULT 0,
  decision_note          text NOT NULL DEFAULT '',
  decided_at             timestamptz,
  created_by             text,
  created_at             timestamptz NOT NULL DEFAULT now(),
  updated_at             timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX model_rollouts_one_active ON model_rollouts (model_id) WHERE stage IN ('shadow','canary','paused');

CREATE TABLE rollout_samples (
  id               text PRIMARY KEY,
  rollout_id       text NOT NULL REFERENCES model_rollouts(id) ON DELETE CASCADE,
  ts               timestamptz NOT NULL DEFAULT now(),
  request_id       text NOT NULL,
  verdict          text NOT NULL CHECK (verdict IN ('candidate','incumbent','tie','error','judge_error')),
  reason           text NOT NULL DEFAULT '',
  cand_latency_ms  integer NOT NULL DEFAULT 0,
  inc_latency_ms   integer NOT NULL DEFAULT 0,
  cand_tokens      integer NOT NULL DEFAULT 0,
  inc_tokens       integer NOT NULL DEFAULT 0,
  cand_cost        double precision NOT NULL DEFAULT 0,
  inc_cost         double precision NOT NULL DEFAULT 0
);
CREATE INDEX rollout_samples_rollout ON rollout_samples (rollout_id, ts DESC);
