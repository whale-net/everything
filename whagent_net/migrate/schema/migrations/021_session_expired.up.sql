-- Adds the `expired` terminal status: a session parked in awaiting_input
-- whose idle clock reaches its effective idle timeout. Terminal and distinct
-- from done/stopped/failed/capped.
ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS sessions_status_check,
    ADD CONSTRAINT sessions_status_check CHECK (status IN ('running', 'awaiting_input', 'done', 'stopped', 'failed', 'capped', 'expired'));

-- Per-session idle timeout pinned at StartSession time. NULL means no
-- override, so the definition's idle_timeout_seconds (or the platform
-- default) applies. Written once, so replay reads the same value.
ALTER TABLE sessions
    ADD COLUMN idle_timeout_seconds INT NULL
        CONSTRAINT sessions_idle_timeout_seconds_check CHECK (idle_timeout_seconds >= 1);

-- Per-definition idle timeout. NULL means the platform default applies.
-- agent_definition is SCD2 (017), so a change lands as a new version row.
ALTER TABLE agent_definition
    ADD COLUMN idle_timeout_seconds INT NULL
        CONSTRAINT agent_definition_idle_timeout_seconds_check CHECK (idle_timeout_seconds >= 1);
