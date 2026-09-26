-- Add default server flag
-- Only one server can be marked as default at a time

ALTER TABLE servers ADD COLUMN is_default BOOLEAN NOT NULL DEFAULT FALSE;

-- Enforce single default server via partial unique index
CREATE UNIQUE INDEX servers_single_default ON servers (is_default) WHERE is_default;

-- Backfill: Set earliest server as default if no default exists
UPDATE servers
SET is_default = TRUE
WHERE server_id = (SELECT MIN(server_id) FROM servers)
AND NOT EXISTS (SELECT 1 FROM servers WHERE is_default);
