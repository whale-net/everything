# LeafLab

Plant and environment monitoring firmware and data pipeline.

LeafLab devices read sensors (light, temperature, soil moisture, etc.), publish readings to MQTT, and feed a cloud processing pipeline. Each device is a small ESP32 board running firmware built in this monorepo.

---

## Projects

| Directory | Description |
|-----------|-------------|
| `sensorboard/` | ESP32 firmware that reads sensors via I2C and publishes via MQTT |
| `processor/` | Go service that consumes MQTT messages from RabbitMQ and writes to the database |
| `migrate/` | Database migration runner (TimescaleDB) |

---

## Quick Start

```bash
# Build sensorboard firmware (simple dynamic — single BH1750)
bazel build //leaflab/sensorboard:sensorboard_simple_dynamic --config=esp32

# Flash to a connected ESP32 over USB
bazel run //leaflab/sensorboard:flash_simple_dynamic -- /dev/ttyUSB0

# Monitor serial output
bazel run //leaflab/sensorboard:serial

# Provision Wi-Fi + MQTT credentials (first time)
bazel run //leaflab/sensorboard:provision -- /dev/ttyUSB0 \
  wifi_ssid=MySSID wifi_pass=MyPass \
  mqtt_host=192.168.1.42 mqtt_port=1883
```

See [`sensorboard/README.md`](sensorboard/README.md) for full build, flash, and extension instructions.

---

## Architecture Overview

```
Physical sensor (BH1750, etc.)
    ↓ I2C
ESP32 (leaflab/sensorboard firmware)
    ↓ MQTT over Wi-Fi
RabbitMQ (MQTT plugin, amq.topic exchange)
    ↓ AMQP
leaflab/processor (Go)
    ↓
TimescaleDB (PostgreSQL + timescaledb extension)
    ↓
Dashboards / analytics
```

The sensor firmware layer is fully unit-tested on the host — no hardware required for most development work. See [`ARCHITECTURE.md`](ARCHITECTURE.md) for the full design.

---

## Database Schema

```mermaid
erDiagram
    Board {
        bigserial board_id PK
        varchar device_id UK "eFuse MAC e.g. leaflab-ccdba79f5fac"
        timestamp registered_at
        timestamp last_seen_at
    }

    SensorType {
        bigserial sensor_type_id PK
        varchar name UK "illuminance | temperature | humidity"
        varchar default_unit "lx | degC | pct"
    }

    Sensor {
        bigserial sensor_id PK
        bigint board_id FK
        bigint sensor_type_id FK
        bigint region_id FK "nullable — current region"
        varchar name "current logical name"
        varchar unit
        int i2c_address "nullable"
        jsonb mux_path "[] = direct; [{muxAddress,muxChannel},...] outer→inner"
        timestamp registered_at
        timestamp last_seen_at
    }

    SensorLabel {
        bigserial sensor_label_id PK
        bigint sensor_id FK
        varchar name
        timestamp valid_from
        timestamp valid_to "null if current"
    }

    SensorHWHistory {
        bigserial history_id PK
        bigint sensor_id FK
        int i2c_address
        jsonb mux_path
        timestamp recorded_at
    }

    Region {
        bigserial region_id PK
        bigint parent_region_id FK "nullable"
        varchar name
        text description
        timestamp created_at
    }

    SensorRegionHistory {
        bigserial history_id PK
        bigint sensor_id FK
        bigint region_id FK
        timestamp assigned_at
        timestamp unassigned_at "null if current"
    }

    DeviceConfig {
        bigserial config_id PK
        bigint board_id FK
        bigint version
        jsonb config_json "protojson DeviceConfig"
        boolean accepted
        timestamp pushed_at
        timestamp acked_at
    }

    SensorReading {
        bigserial reading_id PK
        bigint sensor_id FK
        bigint region_id FK "snapshot at insert"
        bigint config_version "nullable — active config version at insert"
        double value
        boolean valid
        int uptime_s
        timestamp recorded_at "hypertable partition key"
    }

    Board ||--o{ Sensor : "hosts"
    SensorType ||--o{ Sensor : "types"
    Region |o--o{ Sensor : "currently at"
    Region |o--o{ Region : "parent of"
    Sensor ||--o{ SensorLabel : "name history"
    Sensor ||--o{ SensorHWHistory : "hw address history"
    Sensor ||--o{ SensorRegionHistory : "region history"
    Region ||--o{ SensorRegionHistory : "recorded in"
    Sensor ||--o{ SensorReading : "produces"
    Board ||--o{ DeviceConfig : "configs"
```

Key design decisions:
- `sensor` is a stable dimension anchor — rename via config closes old `sensor_label` row, opens new; `sensor_id` and reading history are unchanged
- `sensor.region_id` is a current-value cache; `sensor_region_history` records every assignment (SCD-2)
- `sensor_reading.region_id` is snapshotted at insert so historical location is preserved when sensors move
- `sensor_reading.config_version` records which `DeviceConfig` was active at write time
- `sensor.mux_path` is JSONB supporting arbitrary mux cascade depth
- `device_config.config_json` stores protojson for human-readable SQL queries; device NVS uses binary nanopb

---

## Relationship to `//firmware`

LeafLab firmware is built on top of the board-agnostic libraries in [`firmware/`](../firmware/README.md):

- `firmware/sensor` — `ISensor` interface, `SensorReading`, `BH1750Sensor`, thermistor
- `firmware/i2c` — `II2CBus`, `ArduinoI2CBus`, `FakeI2CBus`
- `firmware/mqtt` — `MQTTWriter` sensor aggregator
- `firmware/network` — Wi-Fi + MQTT state machine

LeafLab board configs (`elegoo_config.cc`) wire together these libraries with concrete hardware addresses and pin assignments. The libraries themselves have no LeafLab-specific knowledge.
