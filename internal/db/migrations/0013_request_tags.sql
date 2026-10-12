-- Request tags: key/value labels a client attaches to a request (header
-- X-Theophylax-Tags) or a gateway key carries by default, for chargeback and
-- analytics. Queried by containment (tags @> '{"k":"v"}').
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS tags jsonb NOT NULL DEFAULT '{}';
CREATE INDEX IF NOT EXISTS request_logs_tags ON request_logs USING gin (tags jsonb_path_ops);
