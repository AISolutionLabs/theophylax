-- Router models (strategy 'complexity') keep their tier configuration here.
ALTER TABLE models ADD COLUMN IF NOT EXISTS routing jsonb;
ALTER TABLE models DROP CONSTRAINT IF EXISTS models_strategy_check;
ALTER TABLE models ADD CONSTRAINT models_strategy_check CHECK (strategy IN ('priority','weighted','latency','complexity'));
