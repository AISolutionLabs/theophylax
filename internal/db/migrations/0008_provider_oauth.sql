-- Who connected an OAuth (authorization code) provider, and when.
ALTER TABLE provider_tokens
  ADD COLUMN connected_by text,
  ADD COLUMN connected_at timestamptz;
