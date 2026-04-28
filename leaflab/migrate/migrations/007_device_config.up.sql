-- Device configuration history.
--
-- Stores every DeviceConfig pushed to a device (accepted or rejected).
-- Config is stored as protojson (JSONB) for human readability and SQL
-- queryability — the device stores the same config as compact nanopb binary.
--
-- The active config for a board is the row with the highest version where
-- accepted = TRUE.

CREATE TABLE device_config (
    config_id    BIGSERIAL   PRIMARY KEY,
    board_id     BIGINT      NOT NULL REFERENCES board(board_id) ON DELETE RESTRICT,
    version      BIGINT      NOT NULL,
    config_json  JSONB       NOT NULL,   -- protojson-encoded DeviceConfig
    accepted         BOOLEAN     NOT NULL DEFAULT FALSE,
    pushed_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    acked_at         TIMESTAMPTZ,            -- NULL until ack received from device
    rejection_reason TEXT,                   -- set on rejected acks; NULL when accepted or pending
    UNIQUE (board_id, version)
);

-- Fast lookup: "what config is currently active for board X?"
CREATE INDEX idx_device_config_board_active
    ON device_config (board_id, version DESC)
    WHERE accepted = TRUE;
