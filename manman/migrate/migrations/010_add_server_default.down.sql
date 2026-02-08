-- Remove default server flag

DROP INDEX IF EXISTS servers_single_default;
ALTER TABLE servers DROP COLUMN IF EXISTS is_default;
