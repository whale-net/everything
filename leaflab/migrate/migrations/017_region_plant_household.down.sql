-- Down migration 017: Remove household_id from region and plant

ALTER TABLE plant DROP COLUMN IF EXISTS household_id;
ALTER TABLE region DROP COLUMN IF EXISTS household_id;
