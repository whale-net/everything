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

## Cross-Process Cache Invalidation — FR73 / NFR15

Every process that keeps an in-memory view of a sensor (today: `leaflab/processor`'s
`SensorCache`; from Phase 4, the API's own bounded-wait observability, NFR15) must learn about a
region, identity or name change **within 5 s of the write committing** (FR73) — and **every
replica** must learn it, not just one of a competing-consumer set (NFR15's broadcast constraint:
a caller's bounded wait can be pinned to any one API replica, so a work queue that delivers to
only one replica does not satisfy it). FR73's 5 s bound and NFR15's every-replica broadcast
constraint are one observability requirement satisfied by a single signalling path — Phase 4 does
not introduce a second.

**Chosen mechanism: a RabbitMQ fanout exchange** (`leaflab.invalidation`, package
`leaflab/invalidation`) — deliberately not the `amq.topic` exchange the device-facing MQTT traffic
uses, and not a competing-consumer work queue. Every writer (the API's direct region/identity/name
assignments and the processor's own `ApplyConfigRegions`) publishes a small typed `Event`
(`{Kind: region|identity|name, DeviceID, SensorID, SensorName, ObservedAt}`) **after its write
commits, never before**. Every subscribing process (`invalidation.Subscriber`) binds its own
exclusive, auto-delete queue to the exchange, so a fanout delivers one copy of every event to
every currently connected replica — satisfying NFR15's every-replica constraint by construction,
and guaranteeing a bounded-wait caller's own replica never misses an event another replica
received instead. **This is why Phase 4 reuses this exact package** for the API's NFR15
observability rather than introducing a second signalling path.

- **Publish-after-commit + idempotent, re-reading handlers.** A subscriber's handler never trusts
  the event payload for the new value; it evicts the stale cache entry (or, for the API's bounded
  wait, wakes a waiter), and the next reader re-reads the database. A duplicate, reordered, or
  replayed event converges to the same result.
- **Rename orphan.** `SensorCache` is keyed `device_id → sensor_name`; a rename's `Event` carries
  the prior sensor name so the subscriber evicts the *old* key explicitly — the new key alone
  never touches it.
- **Bounded staleness backstop.** A periodic full reload (`SensorCache`'s replace, not an
  additive merge, so it also clears a rename's orphaned prior-name entry) self-heals a *dropped*
  event within a bound. **The backstop does not satisfy FR73's 5 s bound; the signal does.** The
  backstop only bounds how long a dropped event can leave the cache wrong.
- **No device-facing change.** The invalidation path is entirely server-side (API ↔ processor); it
  does not touch the MQTT topics, payloads or QoS documented in `MQTT.md`.

---

## Read Path — Bounded Queries Over Tiers (FR25.1, FR71, FR23, FR20)

The bounded read-path RPCs (`GetReadingSeries`, `GetCurrentValues`, `GetPeriodSummary`,
`CompareSeries`; `leaflab/api/proto/api.proto`) are served entirely by `leaflab/api/readings`,
composing three packages that already exist rather than reimplementing any of them — this is the
**one** place FR25.1's join, enrichment and attribution logic lives, so no RPC handler
reimplements it:

- **`leaflab/api/tiers.Select`** picks which granularity tier (`raw`, `sensor_reading_5m` or
  `sensor_reading_1h`) answers a bounded window, coarsening away from the caller's requested tier
  when its retention or NFR3.2's 48-hour raw cap cannot serve it, and always reporting which tier
  answered (FR71 — coarsening is disclosed, never silent).
- **`leaflab/api/attribution.Resolver`** applies FR23's nearest-ancestor plant attribution *above*
  the aggregate, at read time — **the read path deliberately never queries
  `v_sensor_reading_with_plant`**; that view's LATERAL walk (see "Query Layer — Analytical Views"
  below) exists for Grafana's direct-database-read path only, not for the API. Entity-kind
  semantics differ by what is being asked about:
  - **sensor / board** — the physical sensor or board's sensor set (`sensor.board_id`); never
    attribution.
  - **region** — the tier tables' own `region_id` column, i.e. the *physical* region a reading's
    sensor was in at write time (denormalized on `sensor_reading`, migrations 001/022) — "readings
    recorded in this region," not "readings attributed to a plant living in it."
  - **plant** — FR23 attribution proper. Each of the plant's own `plant_region_history` intervals
    is walked separately; for each, `attribution.Resolver.AttributedSensors` resolves the
    interval's attributed sensor set, and FR20's `boundary_partial` rows are substituted for any
    bucket a placement boundary split during that interval. Attribution is evaluated once per
    plant-owned interval, at that interval's own boundaries — a documented limitation: a
    *different* plant entering or leaving elsewhere on the sensor's ancestor path strictly inside
    one of this plant's own intervals is not separately detected.
- **`leaflab/api/authz`** (`EntityRef`/`Scope`) gates every entity before `readings.Reader` is
  called — `Reader` itself performs no authorization (NFR2's one-resolve-one-check shape).

FR26.3's suspect checks (`leaflab/api/suspect`, see `DATA.md`'s "Suspect Checks" section) are
computed on top of the same query result inside `leaflab/api/readings`, never as a second pass
over `sensor_reading`.

---

## Two-Phase Boundary Capture (FR20, A14, A17)

A bucket a plant moved through must resolve exactly for the life of the tier that holds it,
including after raw rows have aged out (FR20.2), permanently and with no horizon (A14).
`leaflab/api/capture` (`boundary_capture` / `boundary_partial`, migration 033) implements this in
two phases:

- **Phase one — `Recorder`**, run at the instant a placement boundary is recorded (from
  `leaflab/api/placement`'s writer, and Phase 5's FR51/FR74), inserts one `boundary_capture` row
  per affected sensor and tier, **in the same transaction as the placement write**.
- **Phase two — `Completer`**, run at bucket close: for each pending capture whose bucket has
  closed, computes that bucket's partials from raw, **both sides, independently — never by
  subtraction from the full bucket** (A17: min and max are not invertible). Results are written
  as `boundary_partial` rows before the capture is marked `completed`.

Both tables are keyed by `sensor_id` and instant, **never by `plant_id`** (FR20.2) — attribution
is resolved above the aggregate at read time (the "Read Path" section above), never baked into the
capture itself. `Completer` runs inside `leaflab/processor` (a single-replica worker,
`app_type = "worker"`) on its own ticker rather than as a separate scheduled job, since the
processor already holds the long-lived pool this package needs and is always up — keeping
migration 022's refresh/retention ordering easy to reason about without a second scheduling
interval. Differential retention on `boundary_partial` follows the coarsest tier a partial splits:
`five_minute`-tier partials are dropped once their bucket ages past 90 days (mirroring
`sensor_reading_5m`'s own retention), `hourly`-tier partials are never dropped — see `DATA.md`'s
retention table for the full tier picture.

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
