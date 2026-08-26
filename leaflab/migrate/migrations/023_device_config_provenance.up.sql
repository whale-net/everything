-- Add provenance tracking to device_config entries (FR82).
--
-- Provenance tracks whether a stored entry was explicitly authored in a request
-- (named by the caller) or materialised from a base version during an EDIT.
--
-- For COMPLETE pushes: all entries are AUTHORED.
-- For EDIT pushes: entries named in the request are AUTHORED; entries carried forward
-- from the base version are MATERIALISED. This governs FR1.3's skip visibility:
-- skip signal means "something you asked for did not happen" (only AUTHORED entries).

-- Add column to device_config
ALTER TABLE device_config ADD COLUMN provenance_json JSONB;

-- Set default for existing rows (materialized since they existed before FR82)
UPDATE device_config SET provenance_json = '{}' WHERE provenance_json IS NULL;

-- Make NOT NULL
ALTER TABLE device_config ALTER COLUMN provenance_json SET NOT NULL;
