ALTER TABLE agent_definition
    DROP COLUMN idle_timeout_seconds;
ALTER TABLE sessions
    DROP COLUMN idle_timeout_seconds;
-- Fails if any session row already holds status 'expired'.
ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS sessions_status_check,
    ADD CONSTRAINT sessions_status_check CHECK (status IN ('running', 'awaiting_input', 'done', 'stopped', 'failed', 'capped'));
