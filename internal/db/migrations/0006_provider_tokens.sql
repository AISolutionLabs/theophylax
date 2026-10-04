-- Access/refresh tokens obtained by custom credential providers, shared by all
-- replicas so rotated refresh tokens survive restarts. Values are encrypted
-- with the master key.
CREATE TABLE provider_tokens (
  provider_id  text PRIMARY KEY REFERENCES providers(id) ON DELETE CASCADE,
  seed         text NOT NULL,
  access_enc   bytea NOT NULL,
  refresh_enc  bytea,
  expires_at   timestamptz,
  updated_at   timestamptz NOT NULL DEFAULT now()
);
