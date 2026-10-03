-- Theophylax core schema

CREATE TABLE orgs (
  id          text PRIMARY KEY,
  slug        text NOT NULL UNIQUE,
  name        text NOT NULL,
  settings    jsonb NOT NULL DEFAULT '{}',
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
  id                 text PRIMARY KEY,
  email              text NOT NULL,
  name               text NOT NULL DEFAULT '',
  password_hash      text,
  is_platform_admin  boolean NOT NULL DEFAULT false,
  status             text NOT NULL DEFAULT 'active' CHECK (status IN ('active','suspended')),
  idp_id             text,
  external_id        text,
  last_login_at      timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_email_uq ON users (lower(email));

CREATE TABLE teams (
  id          text PRIMARY KEY,
  org_id      text NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  slug        text NOT NULL,
  name        text NOT NULL,
  description text NOT NULL DEFAULT '',
  settings    jsonb NOT NULL DEFAULT '{}',
  external_id text,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, slug)
);

CREATE TABLE projects (
  id          text PRIMARY KEY,
  org_id      text NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  team_id     text NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  slug        text NOT NULL,
  name        text NOT NULL,
  description text NOT NULL DEFAULT '',
  settings    jsonb NOT NULL DEFAULT '{}',
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (team_id, slug)
);
CREATE INDEX projects_org_idx ON projects (org_id);

-- Role bindings. scope_type org|team|project; roles: owner|admin|member|viewer
CREATE TABLE memberships (
  id          text PRIMARY KEY,
  user_id     text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  org_id      text NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  scope_type  text NOT NULL CHECK (scope_type IN ('org','team','project')),
  scope_id    text NOT NULL,
  role        text NOT NULL CHECK (role IN ('owner','admin','member','viewer')),
  source      text NOT NULL DEFAULT 'manual',
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (user_id, scope_type, scope_id)
);
CREATE INDEX memberships_scope_idx ON memberships (scope_type, scope_id);
CREATE INDEX memberships_org_idx ON memberships (org_id);

CREATE TABLE agents (
  id             text PRIMARY KEY,
  org_id         text NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  team_id        text NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  project_id     text REFERENCES projects(id) ON DELETE SET NULL,
  slug           text NOT NULL,
  name           text NOT NULL,
  description    text NOT NULL DEFAULT '',
  model          text NOT NULL,
  system_prompt  text NOT NULL DEFAULT '',
  skills         text[] NOT NULL DEFAULT '{}',
  mcp_servers    text[] NOT NULL DEFAULT '{}',
  tools          jsonb NOT NULL DEFAULT '[]',
  params         jsonb NOT NULL DEFAULT '{}',
  allow_client_system boolean NOT NULL DEFAULT true,
  owner_user_id  text REFERENCES users(id) ON DELETE SET NULL,
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, slug)
);

-- kind: gateway (inference) | pat (personal access token for the admin API) | scim
CREATE TABLE api_keys (
  id              text PRIMARY KEY,
  kind            text NOT NULL DEFAULT 'gateway' CHECK (kind IN ('gateway','pat')),
  org_id          text NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  team_id         text REFERENCES teams(id) ON DELETE CASCADE,
  project_id      text REFERENCES projects(id) ON DELETE CASCADE,
  user_id         text REFERENCES users(id) ON DELETE CASCADE,
  agent_id        text REFERENCES agents(id) ON DELETE CASCADE,
  name            text NOT NULL,
  prefix          text NOT NULL,
  hash            bytea NOT NULL UNIQUE,
  allowed_models  text[] NOT NULL DEFAULT '{}',
  rpm             integer NOT NULL DEFAULT 0,
  tpm             integer NOT NULL DEFAULT 0,
  expires_at      timestamptz,
  revoked_at      timestamptz,
  last_used_at    timestamptz,
  created_by      text,
  metadata        jsonb NOT NULL DEFAULT '{}',
  created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX api_keys_org_idx ON api_keys (org_id);
CREATE INDEX api_keys_project_idx ON api_keys (project_id);

-- Upstream provider connections. org_id NULL => platform-wide.
CREATE TABLE providers (
  id             text PRIMARY KEY,
  org_id         text REFERENCES orgs(id) ON DELETE CASCADE,
  name           text NOT NULL,
  kind           text NOT NULL,
  conn_enc       bytea,
  conn_redacted  text NOT NULL DEFAULT '',
  config         jsonb NOT NULL DEFAULT '{}',
  enabled        boolean NOT NULL DEFAULT true,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now()
);

-- Public model catalog. name may end in '*' to act as a wildcard route.
CREATE TABLE models (
  id                 text PRIMARY KEY,
  name               text NOT NULL UNIQUE,
  display_name       text NOT NULL DEFAULT '',
  family             text NOT NULL DEFAULT '',
  aliases            text[] NOT NULL DEFAULT '{}',
  input_price        double precision,
  output_price       double precision,
  cache_read_price   double precision,
  cache_write_price  double precision,
  context_window     integer NOT NULL DEFAULT 0,
  max_output         integer NOT NULL DEFAULT 0,
  capabilities       text[] NOT NULL DEFAULT '{}',
  status             text NOT NULL DEFAULT 'active' CHECK (status IN ('active','preview','deprecated','disabled')),
  strategy           text NOT NULL DEFAULT 'priority' CHECK (strategy IN ('priority','weighted','latency')),
  source             text NOT NULL DEFAULT 'manual',
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE deployments (
  id              text PRIMARY KEY,
  model_id        text NOT NULL REFERENCES models(id) ON DELETE CASCADE,
  provider_id     text NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  upstream_model  text NOT NULL,
  weight          integer NOT NULL DEFAULT 1,
  priority        integer NOT NULL DEFAULT 0,
  enabled         boolean NOT NULL DEFAULT true,
  created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX deployments_model_idx ON deployments (model_id);

CREATE TABLE guardrail_policies (
  id           text PRIMARY KEY,
  org_id       text NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  scope_type   text NOT NULL CHECK (scope_type IN ('org','team','project')),
  scope_id     text NOT NULL,
  name         text NOT NULL,
  description  text NOT NULL DEFAULT '',
  enabled      boolean NOT NULL DEFAULT true,
  mode         text NOT NULL DEFAULT 'enforce' CHECK (mode IN ('enforce','monitor')),
  rules        jsonb NOT NULL DEFAULT '[]',
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX guardrail_scope_idx ON guardrail_policies (org_id);

CREATE TABLE budgets (
  id            text PRIMARY KEY,
  org_id        text NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  scope_type    text NOT NULL CHECK (scope_type IN ('org','team','project','key','user')),
  scope_id      text NOT NULL,
  period        text NOT NULL DEFAULT 'monthly' CHECK (period IN ('daily','monthly','total')),
  limit_usd     double precision,
  limit_tokens  bigint,
  alert_pct     integer NOT NULL DEFAULT 80,
  action        text NOT NULL DEFAULT 'block' CHECK (action IN ('block','alert')),
  created_at    timestamptz NOT NULL DEFAULT now(),
  UNIQUE (scope_type, scope_id, period)
);

CREATE TABLE usage_counters (
  scope_type    text NOT NULL,
  scope_id      text NOT NULL,
  period        text NOT NULL,
  period_start  date NOT NULL,
  cost_usd      double precision NOT NULL DEFAULT 0,
  tokens        bigint NOT NULL DEFAULT 0,
  requests      bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (scope_type, scope_id, period, period_start)
);

CREATE TABLE request_logs (
  id                  text NOT NULL,
  ts                  timestamptz NOT NULL,
  org_id              text,
  team_id             text,
  project_id          text,
  user_id             text,
  key_id              text,
  agent_id            text,
  endpoint            text NOT NULL,
  model               text NOT NULL DEFAULT '',
  upstream_model      text NOT NULL DEFAULT '',
  provider_id         text,
  provider_kind       text NOT NULL DEFAULT '',
  status              integer NOT NULL,
  error               text NOT NULL DEFAULT '',
  stream              boolean NOT NULL DEFAULT false,
  latency_ms          integer NOT NULL DEFAULT 0,
  ttfb_ms             integer NOT NULL DEFAULT 0,
  input_tokens        integer NOT NULL DEFAULT 0,
  output_tokens       integer NOT NULL DEFAULT 0,
  cache_read_tokens   integer NOT NULL DEFAULT 0,
  cache_write_tokens  integer NOT NULL DEFAULT 0,
  cost_usd            double precision NOT NULL DEFAULT 0,
  attempts            integer NOT NULL DEFAULT 1,
  guardrails          jsonb NOT NULL DEFAULT '[]',
  client_ip           text NOT NULL DEFAULT '',
  user_agent          text NOT NULL DEFAULT ''
);
CREATE INDEX request_logs_ts_brin ON request_logs USING brin (ts);
CREATE INDEX request_logs_org_ts ON request_logs (org_id, ts DESC);
CREATE INDEX request_logs_id ON request_logs (id);

CREATE TABLE request_payloads (
  request_id  text PRIMARY KEY,
  ts          timestamptz NOT NULL,
  org_id      text,
  request     jsonb,
  response    jsonb
);
CREATE INDEX request_payloads_ts_brin ON request_payloads USING brin (ts);

-- Tamper-evident audit trail: each row hashes the previous row's hash.
CREATE TABLE audit_log (
  seq          bigserial PRIMARY KEY,
  id           text NOT NULL UNIQUE,
  ts           timestamptz NOT NULL DEFAULT now(),
  actor_type   text NOT NULL,
  actor_id     text NOT NULL DEFAULT '',
  actor_email  text NOT NULL DEFAULT '',
  org_id       text,
  action       text NOT NULL,
  target_type  text NOT NULL DEFAULT '',
  target_id    text NOT NULL DEFAULT '',
  details      jsonb NOT NULL DEFAULT '{}',
  ip           text NOT NULL DEFAULT '',
  user_agent   text NOT NULL DEFAULT '',
  prev_hash    bytea,
  hash         bytea NOT NULL
);
CREATE INDEX audit_org_ts ON audit_log (org_id, ts DESC);

CREATE TABLE idp_providers (
  id              text PRIMARY KEY,
  org_id          text NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  name            text NOT NULL,
  type            text NOT NULL DEFAULT 'oidc' CHECK (type IN ('oidc')),
  issuer          text NOT NULL,
  client_id       text NOT NULL,
  secret_enc      bytea,
  scopes          text[] NOT NULL DEFAULT '{openid,email,profile}',
  groups_claim    text NOT NULL DEFAULT 'groups',
  email_domains   text[] NOT NULL DEFAULT '{}',
  jit             boolean NOT NULL DEFAULT true,
  default_role    text NOT NULL DEFAULT 'member',
  role_mappings   jsonb NOT NULL DEFAULT '[]',
  gateway_tokens  boolean NOT NULL DEFAULT false,
  audience        text NOT NULL DEFAULT '',
  scim_token_hash bytea,
  enabled         boolean NOT NULL DEFAULT true,
  created_at      timestamptz NOT NULL DEFAULT now()
);

-- Registry: skills, MCP servers, agent templates, prompts, plugins.
CREATE TABLE registry_items (
  id              text PRIMARY KEY,
  org_id          text NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  kind            text NOT NULL CHECK (kind IN ('skill','mcp_server','agent','prompt','plugin')),
  slug            text NOT NULL,
  name            text NOT NULL,
  description     text NOT NULL DEFAULT '',
  publisher       text NOT NULL DEFAULT '',
  owner_team_id   text REFERENCES teams(id) ON DELETE SET NULL,
  tags            text[] NOT NULL DEFAULT '{}',
  visibility      text NOT NULL DEFAULT 'org' CHECK (visibility IN ('team','org')),
  status          text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','pending_review','approved','rejected','deprecated')),
  latest_version  text NOT NULL DEFAULT '',
  approved_version text NOT NULL DEFAULT '',
  installs        integer NOT NULL DEFAULT 0,
  rating_sum      integer NOT NULL DEFAULT 0,
  rating_count    integer NOT NULL DEFAULT 0,
  created_by      text,
  reviewed_by     text,
  review_note     text NOT NULL DEFAULT '',
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, kind, slug)
);

CREATE TABLE registry_versions (
  id          text PRIMARY KEY,
  item_id     text NOT NULL REFERENCES registry_items(id) ON DELETE CASCADE,
  version     text NOT NULL,
  content     jsonb NOT NULL,
  digest      text NOT NULL,
  changelog   text NOT NULL DEFAULT '',
  created_by  text,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (item_id, version)
);

CREATE TABLE installations (
  id            text PRIMARY KEY,
  item_id       text NOT NULL REFERENCES registry_items(id) ON DELETE CASCADE,
  org_id        text NOT NULL,
  version       text NOT NULL,
  scope_type    text NOT NULL CHECK (scope_type IN ('org','team','project')),
  scope_id      text NOT NULL,
  installed_by  text,
  created_at    timestamptz NOT NULL DEFAULT now(),
  UNIQUE (item_id, scope_type, scope_id)
);

CREATE TABLE registry_ratings (
  item_id    text NOT NULL REFERENCES registry_items(id) ON DELETE CASCADE,
  user_id    text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  rating     integer NOT NULL CHECK (rating BETWEEN 1 AND 5),
  comment    text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (item_id, user_id)
);

CREATE TABLE settings (
  key    text PRIMARY KEY,
  value  jsonb NOT NULL
);
