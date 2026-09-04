-- pending_restarts: control-plane-local durable record of a Start pending
-- on a gating session's Stop reaching a terminal status.
--
-- Deliberately not SCD2 (see AGENTS.md § SCD2): valid_from/valid_to are for
-- dimension history, not a short-lived work intent with its own terminal
-- state machine. Lifecycle is expressed via status + resolved_at instead.

CREATE TABLE pending_restarts (
    pending_restart_id BIGSERIAL PRIMARY KEY,
    server_game_config_id BIGINT NOT NULL REFERENCES server_game_configs(sgc_id),
    gating_session_id BIGINT NOT NULL REFERENCES sessions(session_id),
    status TEXT NOT NULL,              -- 'pending' | 'started' | 'failed' | 'expired'
    stall_deadline TIMESTAMPTZ NOT NULL,
    started_session_id BIGINT,         -- set when status='started'
    failure_reason TEXT,               -- set when status IN ('failed','expired')
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at TIMESTAMPTZ            -- set on any terminal status
);

-- FR10 / NFR10: at most one pending restart per deployment, enforced by the
-- DB, not by application-level checking. This index is the idempotency
-- mechanism.
CREATE UNIQUE INDEX pending_restarts_one_pending_per_sgc
    ON pending_restarts(server_game_config_id) WHERE status = 'pending';

-- Consumer lookup path: terminal status observed for gating_session_id.
CREATE INDEX pending_restarts_gating_session_pending
    ON pending_restarts(gating_session_id) WHERE status = 'pending';

-- Reaper lookup path (NFR12).
CREATE INDEX pending_restarts_stall_deadline
    ON pending_restarts(stall_deadline) WHERE status = 'pending';
