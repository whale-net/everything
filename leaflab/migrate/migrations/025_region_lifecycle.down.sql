-- Down migration 025: Remove region lifecycle additions

DROP TRIGGER IF EXISTS region_freeze_parent_once_attributed ON region;
DROP FUNCTION IF EXISTS region_freeze_parent_once_attributed();

DROP INDEX IF EXISTS idx_region_active;

ALTER TABLE region DROP COLUMN IF EXISTS successor_region_id;
ALTER TABLE region DROP COLUMN IF EXISTS retired_principal;
ALTER TABLE region DROP COLUMN IF EXISTS retired_operation;
ALTER TABLE region DROP COLUMN IF EXISTS retired_at;
