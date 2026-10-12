-- Alert notification channels (Slack, Teams, signed webhooks, email) per
-- organization. Targets and webhook secrets are encrypted with the master key.
CREATE TABLE IF NOT EXISTS notification_channels (
  id          text PRIMARY KEY,
  org_id      text NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  name        text NOT NULL,
  type        text NOT NULL,              -- slack | teams | webhook | email
  target_enc  bytea NOT NULL,             -- URL, or comma-separated addresses for email
  secret_enc  bytea,                      -- optional HMAC secret for webhooks
  events      text[] NOT NULL DEFAULT '{}', -- empty means every event
  enabled     boolean NOT NULL DEFAULT true,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS notification_channels_org ON notification_channels (org_id);

-- One row per alert sent: replicas claim an alert by inserting its key, so each
-- alert is delivered once across the fleet.
CREATE TABLE IF NOT EXISTS alert_log (
  key      text PRIMARY KEY,
  org_id   text NOT NULL,
  sent_at  timestamptz NOT NULL DEFAULT now()
);

-- Guardrail blocks are summarised per organization, policy and 5-minute window.
CREATE TABLE IF NOT EXISTS guardrail_block_counts (
  org_id        text NOT NULL,
  policy        text NOT NULL,
  window_start  timestamptz NOT NULL,
  count         bigint NOT NULL DEFAULT 0,
  last_rule     text NOT NULL DEFAULT '',
  last_detail   text NOT NULL DEFAULT '',
  PRIMARY KEY (org_id, policy, window_start)
);
