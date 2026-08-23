DROP INDEX IF EXISTS idx_workshop_addons_collection_id;
ALTER TABLE workshop_addons DROP COLUMN IF EXISTS collection_id;
