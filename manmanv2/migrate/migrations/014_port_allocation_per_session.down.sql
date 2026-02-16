-- Revert port allocation back to SGC-based
ALTER TABLE server_ports DROP CONSTRAINT IF EXISTS server_ports_session_id_fkey;
DROP INDEX IF EXISTS idx_server_ports_session_id;
ALTER TABLE server_ports DROP COLUMN IF EXISTS session_id;
ALTER TABLE server_ports ADD COLUMN sgc_id BIGINT REFERENCES server_game_configs(sgc_id) ON DELETE SET NULL;
CREATE INDEX idx_server_ports_sgc_id ON server_ports(sgc_id) WHERE sgc_id IS NOT NULL;
