-- Remove environment field from servers table

DROP INDEX IF EXISTS idx_servers_environment;
ALTER TABLE servers DROP COLUMN IF EXISTS environment;
