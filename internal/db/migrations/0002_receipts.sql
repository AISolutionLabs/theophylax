-- Proof-of-Inference receipts and their Merkle checkpoints.

CREATE TABLE receipt_checkpoints (
  id          text PRIMARY KEY,
  ts          timestamptz NOT NULL DEFAULT now(),
  first_ts    timestamptz NOT NULL,
  last_ts     timestamptz NOT NULL,
  count       integer NOT NULL,
  merkle_root bytea NOT NULL,
  audit_id    text
);

CREATE TABLE receipts (
  request_id     text PRIMARY KEY,
  ts             timestamptz NOT NULL,
  org_id         text,
  key_id         text,
  kid            text NOT NULL,
  jws            text NOT NULL,
  hash           bytea NOT NULL,
  checkpoint_id  text REFERENCES receipt_checkpoints(id)
);
CREATE INDEX receipts_ts_brin ON receipts USING brin (ts);
CREATE INDEX receipts_pending ON receipts (ts, request_id) WHERE checkpoint_id IS NULL;
CREATE INDEX receipts_checkpoint ON receipts (checkpoint_id);
