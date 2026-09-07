-- Sensor chip catalog.
--
-- Describes physical IC models and their possible I2C address variants.
-- Seed data is NOT in this file — it is loaded idempotently on every migrate
-- run by the catalog.Seeder plugin (firmware/sensor/catalog/chips.yaml).
-- Adding a chip: update chips.yaml and redeploy; no new migration needed.

CREATE TABLE sensor_chip (
    sensor_chip_id BIGSERIAL   PRIMARY KEY,
    name           VARCHAR(64) NOT NULL UNIQUE,  -- e.g. 'BH1750', 'SHT3x'
    description    TEXT
);

-- One row per I2C address variant for a chip.
-- is_default = TRUE for the unmodified (ADDR=GND) wiring.
-- Exactly one default per chip is enforced by the partial unique index.
CREATE TABLE sensor_chip_address (
    sensor_chip_address_id BIGSERIAL PRIMARY KEY,
    sensor_chip_id         BIGINT   NOT NULL REFERENCES sensor_chip(sensor_chip_id) ON DELETE CASCADE,
    i2c_address            SMALLINT NOT NULL,
    is_default             BOOLEAN  NOT NULL DEFAULT FALSE,
    addr_config            VARCHAR(64),          -- 'ADDR=GND', 'ADDR=VCC', etc.
    UNIQUE (sensor_chip_id, i2c_address)
);

CREATE UNIQUE INDEX idx_sensor_chip_address_one_default
    ON sensor_chip_address(sensor_chip_id)
    WHERE is_default = TRUE;

-- Link sensors to their chip model.
-- Populated by the processor when it handles a DeviceManifest that includes
-- chip_model. NULL for sensors registered before this migration or without
-- chip_model in their manifest.
ALTER TABLE sensor
    ADD COLUMN sensor_chip_id BIGINT REFERENCES sensor_chip(sensor_chip_id);

CREATE INDEX idx_sensor_sensor_chip_id ON sensor(sensor_chip_id);
