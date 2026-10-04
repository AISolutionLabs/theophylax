-- Model watch: what each provider offers, and the history of scans.
CREATE TABLE IF NOT EXISTS model_discoveries (
  id              text PRIMARY KEY,
  provider_id     text NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  upstream_model  text NOT NULL,
  display_name    text NOT NULL DEFAULT '',
  kind            text NOT NULL DEFAULT '',
  status          text NOT NULL DEFAULT 'new' CHECK (status IN ('new','baseline','added','dismissed')),
  first_seen      timestamptz NOT NULL DEFAULT now(),
  last_seen       timestamptz NOT NULL DEFAULT now(),
  gone_at         timestamptz,
  UNIQUE (provider_id, upstream_model)
);
CREATE INDEX IF NOT EXISTS model_discoveries_status ON model_discoveries (status) WHERE status = 'new';

CREATE TABLE IF NOT EXISTS model_watch_runs (
  id              text PRIMARY KEY,
  ran_at          timestamptz NOT NULL DEFAULT now(),
  providers       integer NOT NULL DEFAULT 0,
  new_models      integer NOT NULL DEFAULT 0,
  gone_models     integer NOT NULL DEFAULT 0,
  catalog_models  integer NOT NULL DEFAULT 0,
  errors          jsonb NOT NULL DEFAULT '[]'
);
