-- Add missing sensor types for CCS811.
INSERT INTO sensor_type (name, default_unit) VALUES
    ('eco2', 'ppm'),
    ('tvoc', 'ppb')
ON CONFLICT (name) DO NOTHING;

-- sensor_chip_type — which measurement types a chip can produce.
-- Many-to-many: SHT3x → {temperature, humidity}, CCS811 → {eco2, tvoc}, etc.
-- Populated by the catalog.Seeder on every migrate run (chips.yaml source of truth).
CREATE TABLE sensor_chip_type (
    sensor_chip_id   BIGINT NOT NULL REFERENCES sensor_chip(sensor_chip_id) ON DELETE CASCADE,
    sensor_type_id   BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id) ON DELETE CASCADE,
    PRIMARY KEY (sensor_chip_id, sensor_type_id)
);
