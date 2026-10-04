-- Fleet-wide rate limit counters, one row per limit key per minute.
CREATE TABLE rate_windows (
  key     text   NOT NULL,
  minute  bigint NOT NULL,
  reqs    bigint NOT NULL DEFAULT 0,
  toks    bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (key, minute)
);
