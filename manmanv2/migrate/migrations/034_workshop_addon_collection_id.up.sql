-- Correlate a workshop addon that was created as a child of a Steam Workshop
-- collection back to the collection's own addon row (issue #445).
ALTER TABLE workshop_addons ADD COLUMN IF NOT EXISTS collection_id BIGINT REFERENCES workshop_addons(addon_id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_workshop_addons_collection_id ON workshop_addons(collection_id);
