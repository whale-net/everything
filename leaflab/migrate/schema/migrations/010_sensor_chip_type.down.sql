DROP TABLE IF EXISTS sensor_chip_type;
DELETE FROM sensor_type WHERE name IN ('eco2', 'tvoc');
