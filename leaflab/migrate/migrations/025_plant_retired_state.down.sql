-- Down migration 025: Remove plant retired-state columns and the
-- relocation-induced marker on plant_region_history.

ALTER TABLE plant_region_history DROP COLUMN IF EXISTS relocation_induced;

ALTER TABLE plant DROP COLUMN IF EXISTS retired_principal;
ALTER TABLE plant DROP COLUMN IF EXISTS retired_operation;
