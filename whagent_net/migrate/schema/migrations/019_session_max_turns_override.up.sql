-- Adds sessions.max_turns_override: a per-session max-turns cap pinned at
-- StartSession time. NULL means no override, so the definition's max_turns
-- (or the platform default) applies. Written once and never updated, so the
-- worker reads the same value on replay.
ALTER TABLE sessions
    ADD COLUMN max_turns_override INT NULL
        CONSTRAINT sessions_max_turns_override_check CHECK (max_turns_override >= 1);
