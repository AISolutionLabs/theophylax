-- Registry rollout control: remember which versions were approved, and let an
-- installation pin one of them instead of following the approved version.
ALTER TABLE registry_versions ADD COLUMN IF NOT EXISTS approved_at timestamptz;
ALTER TABLE registry_versions ADD COLUMN IF NOT EXISTS approved_by text;
UPDATE registry_versions v SET approved_at = i.updated_at, approved_by = i.reviewed_by
  FROM registry_items i WHERE v.item_id = i.id AND v.version = i.approved_version AND v.approved_at IS NULL;

-- Existing installations keep following the approved version, as before.
ALTER TABLE installations ADD COLUMN IF NOT EXISTS pinned boolean NOT NULL DEFAULT false;
