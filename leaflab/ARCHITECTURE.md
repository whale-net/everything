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
| `sensorboard` | `elegoo_dynamic_config.cc` | Single unified image — any Elegoo ESP32 wiring; sensors provisioned via `DeviceConfig` push |

Build: `bazel build //leaflab/sensorboard:sensorboard --config=esp32`
Flash: `bazel run   //leaflab/sensorboard:flash -- /dev/ttyUSB0`

### Hardware vs. Logical Configuration

**Runtime (via MQTT `DeviceConfig` push):**
- Which IC driver to instantiate (`chip_type`) and its I2C address + mux path
- Sensor logical name in MQTT topics and manifest
- Enabled/disabled per sensor
- Poll interval per sensor
- Region assignment (stored server-side; firmware forwards `region_id` to the processor)

Config is persisted to NVS and loaded on boot — no reflash needed to add sensors or change names. See [`MQTT.md`](MQTT.md) for the full config flow.

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


## Cross-process broadcast signalling (FR73 Phase 3, FR47/NFR15 Phase 4)

### The Problem

The processor's `SensorCache` holds a current-value view of each sensor's region, name, and hardware key. When these properties change (via `ApplyConfigRegions`, API assignment, rename, or rewire), the cache becomes stale and continues to stamp readings with outdated values until the board reboots or the cache entry expires. This violates FR73: "no cached view of a sensor may outlive the fact it caches, in any process."

### The Solution: Broadcast Cache Invalidation

**Mechanism: AMQP fanout exchange + in-process event handlers**

1. **Publisher:** Every writer of sensor properties publishes a `CacheInvalidationSignal` to a dedicated fanout exchange (`leaflab.cache-invalidations`) when a change commits:
   - `ApplyConfigRegions` (processor, Phase 3)
   - API region assignment (Phase 5, FR51)
   - Rename (Phase 5, FR52)
   - Rewire (Phase 2, #1202, when identity changes)

2. **Subscriber:** The processor (and any future multi-replica components in Phase 4) subscribes to the fanout exchange and applies changes to the in-memory cache in real time.

3. **Guarantee:** Fanout delivery ensures all subscribers receive the signal (broadcast, not queue). A competing-consumer queue would pass CI with N=1 but fail when Phase 4 introduces multiple API replicas pinned to a single reader (FR47).

### Signal Flow

```
Writer (Processor, API, etc.)
  ↓ commits region/identity change to DB
  ↓ publishes CacheInvalidationSignal to fanout exchange
  ↓
AMQP Fanout Exchange (leaflab.cache-invalidations)
  ↓ (broadcast to all subscribed queues)
  ├→ Processor's queue → ApplyInvalidation → SensorCache updated
  ├→ API's queue (Phase 4) → ApplyInvalidation → in-memory cache updated
  └→ (future replicas all receive)
```

### Invalidation Types

- **"region":** RegionID changed; update the cached value in place.
- **"rename":** Sensor name changed; delete the old name key, insert the new name key.
- **"rewire":** Hardware key (canonical key: i2c_address, mux_path, sensor_type) changed; invalidate all entries for the device (full reload on next access required).
- **"identity":** Catch-all for other structural changes; invalidate device entries.

### Phase 4 Reuse

Phase 4 (NFR15 ack observability, FR47) reuses this same broadcast mechanism to publish
acknowledgement signals — **one signalling path, not two.** The shared `leaflab/broadcast`
Go package is the single place both signal types (and any future one) are declared: it owns
`Exchange` (`leaflab.cache-invalidations`), the routing key for each signal type, the
`Publisher` wrapper, and `NewListener` (the ephemeral, per-instance subscriber used for
components — like API replicas — that need every instance to receive every signal). See
"Config Acknowledgement Observability (FR47 — Bounded Wait)" below for how the API side uses
this.

### Design Constraints

- **Single-replica processor:** The processor is declared as single-replica (see `leaflab/processor/BUILD.bazel` line ~57). This is a correctness constraint: the processor is the sole writer of the read path, and fanout ensures consistency without race conditions.
- **5-second response bound:** A reading recorded more than 5 seconds after a region assignment commits must reflect the new region (FR73 acceptance criterion 3). Achieved by synchronous signal publication + immediate in-process handling.
- **No poll-based sync:** The old approach (cache expiry + periodic DB checks) was replaced by event-driven invalidation to meet the 5-second bound.

---
---

## Database Schema

```
board                       — one row per physical device (device_id = eFuse MAC)
  └── sensor                — one row per physical sensor; stable across renames
        ├── sensor_name_history   — SCD-2 name history (valid_from / valid_to)
        ├── sensor_hw_history     — physical address history (valid_from / valid_to)
        ├── sensor_region_history — region assignment history (valid_from / valid_to)
        └── sensor_reading        — time-series fact table (TimescaleDB hypertable)

sensor_type               — illuminance / temperature / humidity / etc.
region                    — hierarchical location tree (Room → Shelf → Pot)
plant / plant_type        — plant instances and their taxonomy (soft-delete via removed_at)
device_config             — pushed DeviceConfig blobs as JSONB, with accepted flag
sensor_chip               — known chip models (BH1750, SHT3x, ...)
sensor_chip_address       — known valid I2C addresses per chip (for manifest validation)
sensor_chip_type          — many-to-many: which measurement types each chip produces
```

All three `*_history` tables are SCD-2 using the uniform `valid_from` / `valid_to` column convention. `valid_to IS NULL` is the current open row; a partial index makes that lookup O(1).

### Key Design Decisions

- **`sensor` is a stable dimension anchor.** A rename via `DeviceConfig` closes the old `sensor_name_history` row and opens a new one — the `sensor_id` (and all reading history) is unchanged. Continuity of data across renames is the primary reason the sensor table exists as a separate entity rather than denormalizing into readings.

- **`sensor.region_id` is a current-value cache.** `sensor_region_history` records every assignment with open/closed intervals (`valid_to IS NULL` means current). Historical readings carry a snapshotted `region_id` at insert time, so location is preserved even when the sensor moves.

- **`sensor.mux_path` is JSONB.** Supports arbitrary-depth mux cascades (`[]` = direct on root bus, `[{muxAddress, muxChannel}, ...]` ordered outer→inner). A functional unique index on `(board_id, i2c_address, sensor_type_id, mux_path::text)` prevents duplicates.

- **JSONB in DB, binary nanopb on device.** `protojson.Marshal` produces human-readable JSONB in `device_config.config_json`, enabling SQL queries on sensor configs without a proto client. The device uses nanopb binary encoding in NVS — smaller and faster for a constrained environment.

- **Config version stamped on readings.** `sensor_reading.config_version` records which `DeviceConfig` was active when the reading was written, enabling queries like "show me readings taken under this config version."

- **`sensor_reading.valid` is always `true` today** but reserved for future anomaly marking (e.g. I2C failure rows, out-of-range flags). Rows are always inserted so gaps in the time series are explicit rather than invisible.

- **`device_config` is the board-state history.** Each accepted config version represents a "validity window" for the board's running configuration. The view `v_board_state_history` flattens this into a SCD-2-shaped representation (`valid_from` / `valid_to`) using a window function.

---

## Query Layer — Analytical Views

Seven `v_` views (defined in migration 012) are the contract between the processor's write path and downstream consumers (Grafana panels, ad-hoc SQL). **All join logic lives in these views; consumers should not replicate it.**

```
v_region_path                  — recursive region hierarchy (path_ids[], path_name)
v_sensor_current               — current sensor state (name, type, chip, board, region)
v_board_state_history          — SCD-2 shaped device config history
v_board_state_current          — latest accepted config per board
v_sensor_reading_enriched      — workhorse: reading + all dimensions (no fanout)
v_sensor_reading_with_plant    — reading × active plants at recorded_at (may fanout)
v_sensor_reading_with_config_debug — reading + full config_json (debug)
```

The enriched view uses `sensor_reading.region_id` (the insert-time snapshot), not the sensor's current region — reads are historically accurate for region even when sensors move.

See [DATA.md](DATA.md#analytical-views) for the full view reference and example queries.

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


## Config Acknowledgement Observability (FR47 — Bounded Wait)

**Phase 4 reuses the Phase 3 broadcast signalling path exactly** — the same
`leaflab.cache-invalidations` fanout exchange (`leaflab/broadcast.Exchange`), not a second,
independently-declared exchange. Ack signals are distinguished from cache-invalidation signals
purely by routing key (`leaflab/broadcast.RoutingKeyConfigAck` vs.
`RoutingKeyCacheInvalidation`); both are published from the processor through the same
`*rmq.Publisher`/channel (see `leaflab/processor/main.go`).

### Signalling Path: Processor → All API Replicas

When the processor receives a device acknowledgement:

1. **Processor receives ack** over MQTT (`leaflab.<id>/config.ack` topic)
2. **Processor publishes `broadcast.ConfigAckSignal`** (`leaflab/processor/ack_signal.go`'s
   `RabbitMQAckPublisher`) to the shared `leaflab.cache-invalidations` fanout exchange, routing
   key `leaflab.config-ack`
   - The fanout exchange ensures every API replica receives the signal — not a
     competing-consumer queue
   - Signal carries device_id, config_version, accepted/rejected flag, verbatim rejection reason
3. **Each API replica** runs its own `AckListener` (`leaflab/api/ack_listener.go`), built on
   `broadcast.NewListener` — a private, ephemeral, broker-assigned queue exclusive to that
   replica's connection, bound to the shared exchange. This is the API-side counterpart to
   `leaflab/processor/cache.go`'s invalidation subscriber: same exchange, same package
   (`leaflab/broadcast`), different routing key.
   - Delivers signals to any waiting callers via `ConfigAckWaiter.NotifyAck`
4. **Waiting client** receives the result within 2 s at p95 (bounded by NFR15)

### Bounded Wait Constraint

The API's `WaitForConfigAck` RPC:
- Takes board_id, version, and a deadline (seconds since epoch)
- Pins the caller to one replica for the duration of the wait (required by NFR15)
- Clamps the deadline to 30 seconds maximum
- Returns accepted/rejected/still-pending-at-deadline plus the verbatim rejection reason
- Enforces concurrent open waits per principal via the "concurrent-wait" rate-limit bucket

### Why Not a Work Queue?

A competing-consumer queue (e.g., a durable queue with a fixed name shared by every replica)
would fail the N-replica test:
- With N API replicas sharing one named queue, only one replica receives any given ack
- A caller pinned to a different replica would time out
- This violates NFR15's "every replica" broadcast constraint

`broadcast.NewListener`'s ephemeral, broker-assigned queue per call is what makes every replica
independent: each replica's queue is exclusive to it, so the fanout exchange delivers a copy of
every message to every replica, not to one arbitrary consumer.
`leaflab/broadcast/broadcast_integration_test.go` and
`leaflab/api/ack_fanout_integration_test.go` prove this against a real RabbitMQ broker with two
genuinely independent listener instances — the failure mode above is exactly what those tests
would catch.
