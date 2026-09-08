-- 001_initial_schema down: drop in reverse dependency order so
-- transcript_event (FK to sessions) and sessions (self-referencing FK via
-- parent_session_id) go before anything they depend on.
DROP TABLE IF EXISTS tool_call_idempotency;
DROP TABLE IF EXISTS session_agent;
DROP TABLE IF EXISTS turn_usage;
DROP TABLE IF EXISTS turn_context;
DROP TABLE IF EXISTS transcript_event;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS agent_definition;
