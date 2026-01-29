-- Drop indexes
DROP INDEX IF EXISTS idx_sessions_sgc_id;
DROP INDEX IF EXISTS idx_log_refs_start_time;
DROP INDEX IF EXISTS idx_log_refs_session_id;
DROP INDEX IF EXISTS idx_server_capabilities_server_recorded;

-- Drop tables
DROP TABLE IF EXISTS log_references;
DROP TABLE IF EXISTS server_capabilities;
