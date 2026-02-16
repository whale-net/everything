-- Change port allocation from SGC to session-based
-- This allows each session to independently allocate/deallocate ports

-- Drop existing foreign key and add new one to sessions
ALTER TABLE server_ports DROP CONSTRAINT IF EXISTS server_ports_sgc_id_fkey;
ALTER TABLE server_ports DROP COLUMN IF EXISTS sgc_id;
ALTER TABLE server_ports ADD COLUMN session_id BIGINT REFERENCES sessions(session_id) ON DELETE CASCADE;

-- Update index
DROP INDEX IF EXISTS idx_server_ports_sgc_id;
CREATE INDEX idx_server_ports_session_id ON server_ports(session_id) WHERE session_id IS NOT NULL;

COMMENT ON COLUMN server_ports.session_id IS 'Session that allocated this port. Ports are freed when session ends.';
