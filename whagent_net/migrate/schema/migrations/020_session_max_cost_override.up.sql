-- Adds sessions.max_cost_usd_override: a per-session cost cap pinned at
-- StartSession time. NULL means no override, so the definition's
-- max_cost_usd (or the platform default) applies. Written once and never
-- updated, so the worker reads the same value on replay.
ALTER TABLE sessions
    ADD COLUMN max_cost_usd_override NUMERIC(12, 6) NULL
        CONSTRAINT sessions_max_cost_usd_override_check CHECK (max_cost_usd_override > 0);
