# LeafLab — Architecture

## System Overview

```
┌─────────────────────────────────────────────────────────┐
│                    LeafLab Device (ESP32)                │
│                                                         │
│  sensorboard_dynamic_main.cc  ← boot: load NVS config  │
│    ↓                                                    │
│  *_dynamic_config.cc  ← hardware wiring (compile-time)  │
│    ↓ GetSensors() / GetBus()                            │
│  FirmwarePublisher                                      │
│    ↓ subscribes leaflab/<id>/config                     │
│    ↓ publishes manifest, readings, config/ack           │
└───────────────────────┬─────────────────────────────────┘
                        │ MQTT / TLS / Wi-Fi
                   MQTT Broker (RabbitMQ + MQTT plugin)
                        │ amq.topic exchange (leaflab.#)
               ┌────────┴────────┐
               │  leaflab/processor (Go)              │
               │  consumes AMQP, writes TimescaleDB   │
               └────────┬────────┘
                        │
               TimescaleDB (PostgreSQL)
                        │
               Dashboards / future API
```

---

## Firmware Architecture

### Link-Seam Board Configuration

`sensorboard_main.cc` / `sensorboard_dynamic_main.cc` is a `cc_library` that calls
functions with no implementation in the library itself:

```cpp
firmware::II2CBus& GetBus();
pw::span<firmware::ISensor* const> GetSensors();
firmware::FirmwarePublisher& GetPublisher();
// dynamic targets also:
firmware::ConfigStore& GetConfigStore();
firmware::ConfigApplier& GetConfigApplier();
```

These are provided by a board-specific config file (`*_config.cc`) linked at build time.
Bazel selects the right config — no `#ifdef`, no runtime config, no YAML.

**Board config targets:**

| Target | Config file | Description |
|--------|-------------|-------------|
| `sensorboard` | `elegoo_config.cc` | Single BH1750, static names |
| `sensorboard_multiplex` | `elegoo_multiplex_config.cc` | 4 sensors via TCA9548A, static names |
| `sensorboard_simple_dynamic` | `elegoo_simple_dynamic_config.cc` | Single BH1750, runtime name overrides |
| `sensorboard_dynamic` | `elegoo_dynamic_config.cc` | 4 sensors via TCA9548A, runtime name overrides |

### Hardware vs. Logical Configuration

**Compile-time (hardware facts):**
- Which IC driver to instantiate (BH1750, SHT3x, CCS811)
- I2C address of each sensor
- Mux bus and channel for each sensor
- These cannot change without a reflash — they describe physical wiring

**Runtime (logical config, via MQTT `DeviceConfig`):**
- Sensor name used in MQTT topics and the manifest
- Enabled/disabled per sensor
- Poll interval per sensor
- Region assignment (stored server-side; firmware uses `region_id` field for DB tagging only)

Config is persisted to NVS and loaded on boot. See [`MQTT.md`](MQTT.md) for the full config flow.

### Non-Blocking Sensor Reads

Sensors with hardware measurement delays (BH1750 needs 180 ms) use a state machine:

```
Init() → send power-on + trigger
Read() → if elapsed >= 180ms: read result, re-arm
         else:                return cached value
```

The clock function (`millis` on device, a stub in tests) is injected at construction. `loop()` never blocks.

### Host Testability

Every layer is testable without hardware:

| Layer | Test double |
|-------|------------|
| I2C bus | `FakeI2CBus` — records transactions, preset responses, error injection |
| Sensors | `FakeSensor`, `RecordingSensor` — configurable values + call counts |
| MQTT publisher | `FakePublisher` — captures published messages |
| Wi-Fi / MQTT hooks | Inline stubs in test files |

All `//firmware/...` tests run with `bazel test //firmware/...` on the host.

---

## Data Pipeline

### Message Flow

```
FirmwarePublisher.OnConnect():
  1. Subscribe to leaflab/<device_id>/config
  2. Publish "online" to leaflab/<device_id>/status
  3. Publish DeviceManifest to leaflab/<device_id>/manifest (retained)

FirmwarePublisher.PublishReadings():
  for each enabled sensor:
    if Read() returns valid: publish SensorReading to leaflab/<device_id>/sensor/<name>

FirmwarePublisher.HandleConfigMessage():
  1. Decode DeviceConfig; reject if version ≤ current
  2. Match each SensorConfig entry to a sensor by (mux_path, i2c_address)
  3. Apply name, enabled, poll_interval overrides
  4. Save config to NVS
  5. Re-publish DeviceManifest with updated names
  6. Publish DeviceConfigAck with accepted=true
```

### Go Processor Handlers

| Routing key | Action |
|-------------|--------|
| `leaflab.<dev>.manifest` | Upsert board, upsert sensors (hw-address or name keyed), populate sensor cache |
| `leaflab.<dev>.sensor.<name>` | Cache lookup → insert `sensor_reading` row with config_version stamp |
| `leaflab.<dev>.config` | Decode `DeviceConfig`, persist as JSONB to `device_config` table |
| `leaflab.<dev>.config.ack` | On accept: apply region assignments, update config version cache |

---

## Database Schema

```
board                     — one row per physical device (device_id = eFuse MAC)
  └── sensor              — one row per physical sensor; stable across renames
        ├── sensor_label  — SCD-2 name history (valid_from / valid_to)
        ├── sensor_hw_history — physical address history (i2c + mux_path)
        ├── sensor_region_history — region assignment history (assigned_at / unassigned_at)
        └── sensor_reading — time-series fact table (TimescaleDB hypertable)

sensor_type               — illuminance / temperature / humidity / etc.
region                    — hierarchical location tree (Room → Shelf → Pot)
device_config             — pushed DeviceConfig blobs as JSONB, with accepted flag
sensor_chip               — known chip models (BH1750, SHT3x, ...)
sensor_chip_address       — known valid I2C addresses per chip (for manifest validation)
```

### Key Design Decisions

- **`sensor` is a stable dimension anchor.** A rename via `DeviceConfig` closes the old `sensor_label` row and opens a new one — the `sensor_id` (and all reading history) is unchanged. Continuity of data across renames is the primary reason the sensor table exists as a separate entity rather than denormalizing into readings.

- **`sensor.region_id` is a current-value cache.** `sensor_region_history` records every assignment with open/closed intervals (`unassigned_at = NULL` means current). Historical readings carry a snapshotted `region_id` at insert time, so location is preserved even when the sensor moves.

- **`sensor.mux_path` is JSONB.** Supports arbitrary-depth mux cascades (`[]` = direct on root bus, `[{muxAddress, muxChannel}, ...]` ordered outer→inner). A functional unique index on `(board_id, i2c_address, sensor_type_id, mux_path::text)` prevents duplicates.

- **JSONB in DB, binary nanopb on device.** `protojson.Marshal` produces human-readable JSONB in `device_config.config_json`, enabling SQL queries on sensor configs without a proto client. The device uses nanopb binary encoding in NVS — smaller and faster for a constrained environment.

- **Config version stamped on readings.** `sensor_reading.config_version` records which `DeviceConfig` was active when the reading was written, enabling queries like "show me readings taken under this config version."

- **`sensor_reading.valid` is always `true` today** but reserved for future anomaly marking (e.g. I2C failure rows, out-of-range flags). Rows are always inserted so gaps in the time series are explicit rather than invisible.

---

## Relationship to `//firmware`

LeafLab firmware is built on top of the board-agnostic libraries in [`firmware/`](../firmware/README.md):

- `firmware/sensor` — `ISensor`, `BH1750Sensor`, `SHT3xDevice`, `CCS811Device`
- `firmware/i2c` — `II2CBus`, `ArduinoI2CBus`, `TCA9548ABus`, `FakeI2CBus`
- `firmware/mqtt` — `FirmwarePublisher` (manifest, readings, config sub/ack)
- `firmware/network` — Wi-Fi + MQTT state machine, TLS via `WiFiClientSecure`
- `firmware/config` — `ConfigStore` (NVS), `ConfigApplier` (sensor name/enabled overrides)
- `firmware/credentials` — NVS provisioning for Wi-Fi and MQTT credentials
- `firmware/device_id` — stable eFuse MAC-based device ID

LeafLab board configs (`*_config.cc`) wire these libraries to concrete hardware addresses and pin assignments. The libraries themselves have no LeafLab-specific knowledge.
