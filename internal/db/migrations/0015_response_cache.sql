-- Response cache: complete upstream responses keyed by a hash of the
-- organization, project, deployment, upstream model and the exact upstream
-- request. Bodies are encrypted with the master key. Off unless a scope sets
-- cache_ttl_seconds.
CREATE TABLE IF NOT EXISTS response_cache (
  key           bytea PRIMARY KEY,
  org_id        text NOT NULL,
  project_id    text NOT NULL DEFAULT '',
  created_at    timestamptz NOT NULL DEFAULT now(),
  expires_at    timestamptz NOT NULL,
  content_type  text NOT NULL DEFAULT '',
  body_enc      bytea NOT NULL,
  size          integer NOT NULL
);
CREATE INDEX IF NOT EXISTS response_cache_expires ON response_cache (expires_at);
CREATE INDEX IF NOT EXISTS response_cache_org ON response_cache (org_id, project_id);
