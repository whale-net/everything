-- Drop the content-addressed Workshop cache tables and their indexes.

DROP TABLE IF EXISTS workshop_cache_host_presence;
DROP INDEX IF EXISTS idx_workshop_cache_entries_workshop_id;
DROP TABLE IF EXISTS workshop_cache_entries;
