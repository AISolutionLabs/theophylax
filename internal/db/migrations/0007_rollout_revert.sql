-- Promoted rollouts can be rolled back. Promotion records the deployment it
-- added and the model's routing just before, so a rollback restores it exactly.
ALTER TABLE model_rollouts
  ADD COLUMN promoted_deployment_id text,
  ADD COLUMN routing_before jsonb,
  ADD COLUMN reverted_at timestamptz,
  ADD COLUMN reverted_by text;
