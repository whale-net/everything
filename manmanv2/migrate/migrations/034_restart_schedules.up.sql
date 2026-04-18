CREATE TABLE restart_schedules (
    restart_schedule_id BIGSERIAL PRIMARY KEY,
    sgc_id              BIGINT    NOT NULL REFERENCES server_game_configs(sgc_id) ON DELETE CASCADE,
    cadence_minutes     INT       NOT NULL CHECK (cadence_minutes > 0),
    enabled             BOOLEAN   NOT NULL DEFAULT true,
    last_restart_at     TIMESTAMP WITHOUT TIME ZONE,
    created_at          TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT NOW(),
    deleted_at          TIMESTAMP WITHOUT TIME ZONE
);

CREATE INDEX idx_restart_schedules_sgc_id ON restart_schedules(sgc_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_restart_schedules_due ON restart_schedules(enabled, last_restart_at) WHERE deleted_at IS NULL;
