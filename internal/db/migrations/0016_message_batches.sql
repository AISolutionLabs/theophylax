-- Message batches: asynchronous requests run by the gateway through the
-- normal inference pipeline. Request parameters and results are encrypted.
CREATE TABLE IF NOT EXISTS message_batches (
  id text PRIMARY KEY,
  org_id text NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  team_id text,
  project_id text NOT NULL,
  key_id text,
  user_id text,
  via text NOT NULL DEFAULT '',
  client_ip text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'in_progress',
  total int NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  cancel_initiated_at timestamptz,
  ended_at timestamptz
);
CREATE INDEX IF NOT EXISTS message_batches_project ON message_batches (project_id, created_at DESC);
CREATE INDEX IF NOT EXISTS message_batches_open ON message_batches (status) WHERE status <> 'ended';

CREATE TABLE IF NOT EXISTS message_batch_items (
  batch_id text NOT NULL REFERENCES message_batches(id) ON DELETE CASCADE,
  idx int NOT NULL,
  custom_id text NOT NULL,
  status text NOT NULL DEFAULT 'pending',
  params_enc bytea,
  result_enc bytea,
  request_id text,
  attempts int NOT NULL DEFAULT 0,
  next_at timestamptz NOT NULL DEFAULT now(),
  claimed_at timestamptz,
  PRIMARY KEY (batch_id, idx)
);
CREATE INDEX IF NOT EXISTS message_batch_items_due ON message_batch_items (next_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS message_batch_items_running ON message_batch_items (claimed_at) WHERE status = 'running';
