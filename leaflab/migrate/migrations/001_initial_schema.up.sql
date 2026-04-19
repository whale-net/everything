-- LeafLab initial schema
-- TimescaleDB extension must be created before hypertable targets.

CREATE EXTENSION IF NOT EXISTS timescaledb;

-- ── Region ───────────────────────────────────────────────────────────────────
-- Represents a physical location. Self-referential for hierarchy (Room > Shelf > Pot).

CREATE TABLE region (
    region_id       BIGSERIAL PRIMARY KEY,
    parent_region_id BIGINT REFERENCES region(region_id) ON DELETE SET NULL,
    name            VARCHAR(255) NOT NULL,
    description     TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_region_parent_region_id ON region(parent_region_id);

-- ── Board ────────────────────────────────────────────────────────────────────
-- Represents a physical device (ESP32). Self-registers via DeviceManifest on connect.

CREATE TABLE board (
    board_id      BIGSERIAL PRIMARY KEY,
    device_id     VARCHAR(64) NOT NULL UNIQUE,  -- eFuse MAC e.g. leaflab-ccdba79f5fac
    registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── SensorType ───────────────────────────────────────────────────────────────
-- Lookup table for sensor categories. Seeded from proto SensorType enum.

CREATE TABLE sensor_type (
    sensor_type_id BIGSERIAL PRIMARY KEY,
    name           VARCHAR(64) NOT NULL UNIQUE,
    default_unit   VARCHAR(16) NOT NULL
);

INSERT INTO sensor_type (name, default_unit) VALUES
    ('illuminance', 'lx'),
    ('temperature', 'degC'),
    ('humidity',    'pct');

-- ── Sensor ───────────────────────────────────────────────────────────────────
-- Represents one sensor on a board. Self-registers via DeviceManifest.
-- region_id is nullable — a sensor can be registered before being placed anywhere.
-- UNIQUE(board_id, name) makes upsert on re-flash idempotent.

CREATE TABLE sensor (
    sensor_id      BIGSERIAL PRIMARY KEY,
    board_id       BIGINT NOT NULL REFERENCES board(board_id) ON DELETE CASCADE,
    sensor_type_id BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id),
    region_id      BIGINT REFERENCES region(region_id) ON DELETE SET NULL,
    name           VARCHAR(128) NOT NULL,
    unit           VARCHAR(16) NOT NULL,  -- as reported in manifest
    registered_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (board_id, name)
);

CREATE INDEX idx_sensor_board_id       ON sensor(board_id);
CREATE INDEX idx_sensor_sensor_type_id ON sensor(sensor_type_id);
CREATE INDEX idx_sensor_region_id      ON sensor(region_id);

-- ── SensorRegionHistory ──────────────────────────────────────────────────────
-- Audit trail of sensor placement changes.
-- unassigned_at = NULL means this is the current assignment.

CREATE TABLE sensor_region_history (
    history_id    BIGSERIAL PRIMARY KEY,
    sensor_id     BIGINT NOT NULL REFERENCES sensor(sensor_id) ON DELETE CASCADE,
    region_id     BIGINT NOT NULL REFERENCES region(region_id) ON DELETE CASCADE,
    assigned_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    unassigned_at TIMESTAMPTZ
);

CREATE INDEX idx_sensor_region_history_sensor_id     ON sensor_region_history(sensor_id);
CREATE INDEX idx_sensor_region_history_region_id     ON sensor_region_history(region_id);
-- Partial index for fast "current assignment" lookups.
CREATE INDEX idx_sensor_region_history_current       ON sensor_region_history(sensor_id) WHERE unassigned_at IS NULL;

-- ── SensorReading ────────────────────────────────────────────────────────────
-- Time-series readings. TimescaleDB hypertable partitioned by recorded_at.
-- Composite PK required: TimescaleDB demands the partition key be part of the PK.
-- region_id is denormalized from Sensor at write time — preserves location history
-- when a sensor is moved between regions.

CREATE TABLE sensor_reading (
    reading_id  BIGSERIAL,
    sensor_id   BIGINT NOT NULL REFERENCES sensor(sensor_id) ON DELETE CASCADE,
    region_id   BIGINT REFERENCES region(region_id) ON DELETE SET NULL,
    value       DOUBLE PRECISION NOT NULL,
    valid       BOOLEAN NOT NULL DEFAULT TRUE,
    uptime_ms   INTEGER NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (reading_id, recorded_at)
);

SELECT create_hypertable('sensor_reading', 'recorded_at');

-- Leading on recorded_at DESC for time-range queries (most common access pattern).
CREATE INDEX idx_sensor_reading_sensor_id ON sensor_reading(sensor_id, recorded_at DESC);
CREATE INDEX idx_sensor_reading_region_id ON sensor_reading(region_id,  recorded_at DESC);
-- Partial index — invalid readings are rare; this keeps anomaly queries fast.
CREATE INDEX idx_sensor_reading_invalid   ON sensor_reading(sensor_id, recorded_at DESC) WHERE valid = FALSE;

-- ── PlantType ────────────────────────────────────────────────────────────────

CREATE TABLE plant_type (
    plant_type_id BIGSERIAL PRIMARY KEY,
    common_name   VARCHAR(128) NOT NULL,
    species       VARCHAR(128)
);

-- ── Plant ────────────────────────────────────────────────────────────────────
-- removed_at = NULL means still present. RESTRICT on region_id prevents
-- silently deleting plant history by deleting a region.

CREATE TABLE plant (
    plant_id      BIGSERIAL PRIMARY KEY,
    region_id     BIGINT NOT NULL REFERENCES region(region_id) ON DELETE RESTRICT,
    plant_type_id BIGINT NOT NULL REFERENCES plant_type(plant_type_id),
    name          VARCHAR(128) NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    removed_at    TIMESTAMPTZ
);

CREATE INDEX idx_plant_region_id     ON plant(region_id);
CREATE INDEX idx_plant_plant_type_id ON plant(plant_type_id);
-- Partial index for active plants.
CREATE INDEX idx_plant_active        ON plant(region_id) WHERE removed_at IS NULL;
