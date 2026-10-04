-- Prices come from the upstream provider's published list prices (fetched
-- catalog), keyed by upstream model ID, instead of admin-entered values.

CREATE TABLE pricing_catalog (
  id          integer PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  source      text NOT NULL,
  fetched_at  timestamptz NOT NULL,
  entries     jsonb NOT NULL
);

-- The catalog entry each request was priced with ('' = no list price found).
ALTER TABLE request_logs ADD COLUMN price_key text NOT NULL DEFAULT '';

ALTER TABLE models
  DROP COLUMN input_price,
  DROP COLUMN output_price,
  DROP COLUMN cache_read_price,
  DROP COLUMN cache_write_price;
