# LeafLab — Data Model & Flows

## Entity Relationships

```mermaid
erDiagram
    board {
        bigserial board_id PK
        varchar   device_id UK
        timestamptz registered_at
        timestamptz last_seen_at
    }

    sensor_type {
        bigserial sensor_type_id PK
        varchar   name UK
        varchar   default_unit
    }

    sensor {
        bigserial   sensor_id PK
        bigint      board_id FK
        bigint      sensor_type_id FK
        bigint      region_id FK
        varchar     name
        varchar     unit
        int         i2c_address
        jsonb       mux_path
        timestamptz registered_at
        timestamptz last_seen_at
    }

    sensor_name_history {
        bigserial   sensor_name_history_id PK
        bigint      sensor_id FK
        varchar     name
        timestamptz valid_from
        timestamptz valid_to
    }

    sensor_hw_history {
        bigserial   history_id PK
        bigint      sensor_id FK
        smallint    i2c_address
        jsonb       mux_path
        timestamptz valid_from
        timestamptz valid_to
    }

    region {
        bigserial   region_id PK
        bigint      parent_region_id FK
        varchar     name
        text        description
        timestamptz created_at
    }

    sensor_region_history {
        bigserial   history_id PK
        bigint      sensor_id FK
        bigint      region_id FK
        timestamptz valid_from
        timestamptz valid_to
    }

    device_config {
        bigserial   config_id PK
        bigint      board_id FK
        bigint      version
        jsonb       config_json
        boolean     accepted
        timestamptz pushed_at
        timestamptz acked_at
    }

    device_config_entry {
        bigserial config_id FK
        smallint  i2c_address
        jsonb     mux_path
        bigint    sensor_type_id FK
        text      provenance
    }

    sensor_reading {
        bigserial   reading_id PK
        bigint      sensor_id FK
        bigint      region_id FK
        bigint      config_version
        double      value
        boolean     valid
        int         uptime_s
        timestamptz recorded_at
    }

    sensor_chip {
        bigserial sensor_chip_id PK
        varchar   name UK
        text      description
    }

    sensor_chip_address {
        bigserial sensor_chip_address_id PK
        bigint    sensor_chip_id FK
        int       i2c_address
        boolean   is_default
        varchar   addr_config
    }

    sensor_chip_type {
        bigint sensor_chip_id FK
        bigint sensor_type_id FK
    }

    board            ||--o{ sensor               : "hosts"
    sensor_type      ||--o{ sensor               : "classifies"
    region           |o--o{ sensor               : "current placement"
    region           |o--o{ region               : "parent of"
    sensor           ||--o{ sensor_name_history   : "name history"
    sensor           ||--o{ sensor_hw_history    : "wiring history"
    sensor           ||--o{ sensor_region_history: "location history"
    region           ||--o{ sensor_region_history: "hosts"
    sensor           ||--o{ sensor_reading       : "produces"
    board            ||--o{ device_config        : "configured by"
    device_config    ||--o{ device_config_entry  : "per-entry provenance"
    sensor_type      ||--o{ device_config_entry  : "classifies"
    sensor_chip      ||--o{ sensor_chip_address  : "known addresses"
    sensor_chip      ||--o{ sensor_chip_type     : "produces"
    sensor_type      ||--o{ sensor_chip_type     : "produced by"
```

---

## Sensor Identity Through Time

`sensor` is a stable anchor — its `sensor_id` never changes even when the
sensor is renamed, moved, or temporarily removed from a config.

```mermaid
flowchart TD
    A[board connects\nmanifest published] --> B[UpsertSensor\nby hw address or name]
    B --> C{same hw address\nalready in DB?}
    C -- yes --> D[UPDATE name/unit\nreturn existing sensor_id]
    C -- no  --> E[INSERT new sensor row]
    D --> F[UpsertSensorLabel\nclose old row in sensor_name_history\nopen new row]
    E --> F
    F --> G[UpsertSensorHWHistory\nrecord physical address snapshot]
```

---

## Config Push & Region Assignment

```mermaid
sequenceDiagram
    participant API
    participant MQTT
    participant Device
    participant Processor
    participant DB

    API->>MQTT: publish DeviceConfig (proto)
    MQTT->>Device: leaflab/<id>/config
    Device->>Device: ConfigApplier.Apply()\ninstantiate/destroy sensors\nsave to NVS
    Device->>MQTT: publish DeviceManifest (updated names)
    Device->>MQTT: publish DeviceConfigAck (accepted=true)
    MQTT->>Processor: leaflab.<id>.config
    Processor->>DB: UpsertDeviceConfig (JSONB)
    MQTT->>Processor: leaflab.<id>.manifest
    Processor->>DB: UpsertSensor per descriptor
    MQTT->>Processor: leaflab.<id>.config.ack
    Processor->>DB: AckDeviceConfig (accepted=true)
    Processor->>DB: ApplyConfigRegions\n  UPDATE sensor.region_id\n  close + open sensor_region_history rows
    Processor->>Processor: cache.SetConfigVersion(device, version)
```

---

## Reading Write Path

```mermaid
flowchart LR
    Device -->|SensorReading proto| MQTT
    MQTT -->|leaflab.id.sensor.name| Processor
    Processor --> Cache{sensor in\ncache?}
    Cache -- hit --> Insert
    Cache -- miss --> DB_lookup[GetSensor from DB]
    DB_lookup --> Insert
    Insert[InsertReading\nsensor_id, region_id snapshot\nconfig_version stamp\nrecorded_at = NOW] --> TimescaleDB
```

---

## mux_path JSONB Format

`sensor.mux_path` and `sensor_hw_history.mux_path` store the full I2C mux
chain ordered outer → inner.  Empty array means the sensor is directly on
the root I2C bus.

`sensor_hw_history.i2c_address` (added in migration 013) is the interval's
half of the FR18 canonical hardware key `(i2c_address, mux_path,
sensor_type)` — the sensor_type component is carried by the `sensor` row
the interval belongs to (`sensor_id`), not duplicated here. It is `NULL`
for intervals closed before migration 013 ("not recorded, pre-migration");
never `0` unless the sensor's address was genuinely `0x00` on an open
interval — `0` is a real, present I2C address (see `//leaflab/hwkey`'s
`AddressOpt`), distinct from absent.

```jsonc
// direct on root bus
[]

// single TCA9548A at 0x70, channel 5
[{"muxAddress": 112, "muxChannel": 5}]

// cascaded muxes: outer 0x70 ch3 → inner 0x71 ch1
[{"muxAddress": 112, "muxChannel": 3},
 {"muxAddress": 113, "muxChannel": 1}]
```

Unique constraint on `sensor`: `(board_id, i2c_address, sensor_type_id, mux_path::text)`.

---

## Config Version Stamping

Every `sensor_reading` row carries `config_version` (nullable).  This is the
`device_config.version` that was active when the reading was written, taken
from an in-memory cache pre-warmed at processor startup and updated on each
accepted `DeviceConfigAck`.

This enables queries like:

```sql
-- readings taken under a specific config
SELECT * FROM sensor_reading
WHERE sensor_id = $1 AND config_version = $2
ORDER BY recorded_at DESC;

-- latest reading per config version
SELECT config_version, MAX(recorded_at)
FROM sensor_reading WHERE sensor_id = $1
GROUP BY config_version ORDER BY 2 DESC;
```

---

## Config Push Scope Semantics (FR82)

`PushDeviceConfigRequest.scope` states what a push's `sensors` payload
means as a whole -- there is no default, and it is never inferred from
payload shape:

- **`COMPLETE`** -- the payload is the board's entire desired sensor set.
  An entry absent from it is removed from the desired state. This is what
  a `leaflab/scripts/scenarios/*.json` file has always meant, and matches
  `ConfigApplier::ApplyFactory()` (`firmware/config/config_applier.cc`),
  the shipping board's applier (wired in via
  `leaflab/sensorboard/elegoo_dynamic_config.cc`): it resets every sensor,
  device and mux-bus pool before rebuilding from `cfg.sensors` on every
  push.
- **`EDIT`** -- the payload names only entries being added or changed,
  plus `removes`. Everything else is materialised server-side from the
  board's **current accepted config version alone** -- never the reported
  device manifest (FR49). Refused, before anything is written, against a
  board with no accepted config yet.

`removes` entries are removal keys (`RemoveKey`, api.proto): a full
canonical key (`sensor_type` set) drops exactly the one entry at that
`(i2c_address, mux_path, sensor_type)`; a chip key (`sensor_type` unset)
drops every entry sharing `(i2c_address, mux_path)` regardless of
`sensor_type` -- the form matching the physical action of unsoldering a
part (e.g. one CCS811 chip key drops both its eCO2 and TVOC entries). An
entry whose `i2c_address` is the legacy "unknown address" sentinel (`0`,
see `mux_path JSONB Format` above) has no removal path via `EDIT` at all
-- only a `COMPLETE` push that omits it can drop it.

`device_config_entry` (migration 028) stores one row per entry in a
stored `device_config` version, keyed on that same canonical hardware key,
carrying `provenance`: `authored` (named by the caller in the request that
stored it, including in `removes`) or `materialised` (carried forward
unchanged from an `EDIT` push's base). This is what FR1.3's skip-visibility
surface and FR35.2/FR37's per-entry reporting key off of.

`//leaflab/api/config` implements this package-pure: `CanonicalKey`
(entry -> `hwkey.Key`), `RemoveKey.Match` (removal-key resolution against
a base entry set) and `Materialise` (EDIT-scope base + adds + removes ->
the new version's complete, provenance-tagged entry set). It has no
database dependency -- `leaflab/api/server.go`'s `PushDeviceConfig`
handler loads the base config and sensor_type IDs and translates this
package's results into `device_config`/`device_config_entry` rows and
`sensor_hw_history` interval closes.

---

## Push Validation and Diff (FR39, FR37)

`//leaflab/api/config`'s `Validate` (`validate.go`) runs FR39's
server-side checks against a push payload -- everything the firmware
would reject anyway, so a caller finds out before publishing rather than
from a rejected `DeviceConfigAck`: I2C address range, a chip/measurement-
type pair the catalog (`sensor_chip`/`sensor_chip_type`/`sensor_type`,
migrations 008/010) does not produce, `poll_interval_ms` bounds
(`leaflab/api/ENV.md`), within-payload hardware-key collisions, a remove
key matching nothing in the base (never a silent no-op), and a remove
naming an entry with no I2C address (FR82.4's named failure class). Every
failure is collected into one `Validation`, each naming its entry index
and field -- never just the first. Like the rest of this package, `Validate`
has no database dependency: the caller resolves a `Catalog` snapshot and
`PollIntervalBounds` once and passes them in.

`Diff` (`diff.go`) computes FR37's server-side, per-entry diff between two
complete entry sets (`ADDED`/`REMOVED`/`CHANGED`/`UNCHANGED`), by
canonical hardware key rather than payload order. Because every stored
payload is complete (FR82), `REMOVED` is reachable whether the prior
version was superseded by a `COMPLETE` push that omitted the entry or an
`EDIT` push whose `removes` dropped it. The `DiffConfigVersions` RPC
(api.proto) diffs two versions, or a version against an unpushed draft --
a draft is materialised through the same FR82 scope semantics as an
actual push, but never stored or published.

Boundary with FR16.4: `Validate`'s within-payload collision check does
not, and cannot, catch a swap (two entries trading addresses) -- a swap
produces two distinct canonical keys, not a collision. Swap handling is
identity resolution's job (`leaflab/api/identity.go`), not validation's.

---

## Reported Inventory and Drift (FR49)

`board_manifest_report` / `board_manifest_report_entry` (migration 035)
store the single most recent `DeviceManifest` a board sent, plus the
instant it was received. This is deliberately **not** derived from
`sensor`/`sensor_hw_history`: those tables accumulate across every
manifest a board has ever sent (an entry omitted from a later manifest is
never retired there), so they cannot answer "what did the *last* manifest
say" on their own. `board.last_seen_at` (migration 001) is no substitute
either -- it updates on every message a board sends, not specifically on
a manifest.

**The report is a lossy echo of desired state, never a source.** A
manifest only ever contains what the board's `ConfigApplier::ApplyFactory()`
(`firmware/config/config_applier.cc`) successfully instantiated from its
last applied config -- an entry with an unknown chip type, an invalid
address, no resolvable bus endpoint, or an exhausted pool is silently
dropped before it ever reaches a manifest. Because of that, the reported
inventory is always a subset of the desired state last pushed, never a
superset, and it is used only to report divergence (`GetConfigDrift`,
api.proto) -- never to materialise a new config version. `leaflab/api/config`
(FR82's EDIT-scope materialisation) must never import or query these
tables: an entry that failed to instantiate would otherwise be silently
deleted from the stored desired state the next time it's carried forward.
`leaflab/conformance` asserts this structurally, not just in this prose.

The write path replaces this board's entries wholesale on every manifest
(delete then reinsert, then stamp `reported_at`) -- not SCD2 (there is no
current/superseded distinction to preserve, only "the latest one", the
same "last value" shape `board.last_seen_at` itself has). Column shape
mirrors `device_config_entry`'s canonical hardware key (FR18) exactly.

`GetConfigDrift` classifies every canonical hardware key seen on either
side of (stored desired state, reported inventory):
`IN_DESIRED_NOT_REPORTED` (the interesting case: the board did not
instantiate this entry), `REPORTED_NOT_IN_DESIRED` (should be impossible
under FR82 replace semantics -- reported loudly, never swallowed), or
`MATCHED`.

---

## SCD2 Convention

All SCD2 (Slowly Changing Dimension Type 2) history tables follow a uniform column convention:

| Column | Type | Meaning |
|---|---|---|
| `valid_from` | `TIMESTAMPTZ NOT NULL` | When this row became the current value |
| `valid_to` | `TIMESTAMPTZ` | When it was superseded; `NULL` = still current |

A partial index on `(sensor_id) WHERE valid_to IS NULL` makes "what is the current value?" queries O(1) on each history table.

SCD2 tables in this schema:

| Table | What changes |
|---|---|
| `sensor_name_history` | Sensor logical name |
| `sensor_region_history` | Sensor region assignment |
| `sensor_hw_history` | Sensor I2C address + mux path |
| `plant_region_history` | Plant region placement (FR19) — indexed both directions (plant→region at T and region→plant at T, NFR6.1); a move closes and opens, never back-dated (FR19) |

`device_config` is NOT SCD2 — it is an append-only event log keyed by `(board_id, version)`. The view `v_board_state_history` derives a SCD2-shaped representation from it using a window function.

---

## Analytical Views

Seven plain views (prefixed `v_`) expose the schema to downstream consumers (Grafana panels, ad-hoc SQL). All join logic is in the views — consumers should not replicate it.

| View | Cardinality | Purpose |
|---|---|---|
| `v_region_path` | 1 row / region | Recursive region hierarchy with `path_ids[]`, `path_names[]`, `path_name` |
| `v_sensor_current` | 1 row / sensor | Current state: name, type, chip, board, region path |
| `v_board_state_history` | 1 row / accepted config | SCD2-shaped board config history (valid_from / valid_to) |
| `v_board_state_current` | 1 row / board | Current accepted device config per board |
| `v_sensor_reading_enriched` | 1 row / reading | **Workhorse**: reading + sensor + region path + config metadata |
| `v_sensor_reading_with_plant` | 1 row / (reading × active plant) | Plant and plant_type slices, plus `household_id`; readings without plants appear with NULL plant fields |
| `v_sensor_reading_with_config_debug` | 1 row / reading | `v_sensor_reading_enriched` + full `device_config.config_json` (debug) |

> **Attribution correction (migration 021, FR72) — Grafana-affecting.** Before migration 021,
> `v_sensor_reading_with_plant` attributed a reading to whichever plant currently held
> `plant.region_id = <reading's region>` — a live, mutable pointer — so moving a plant
> retroactively rewrote every historical reading it had ever produced. Migration 021 repoints the
> join to nearest-ancestor resolution against `plant_region_history` at `recorded_at` (the same
> rule as `leaflab/api/attribution`, NFR1.c), so readings recorded before a plant's move now stay
> attributed to the plant/region that was actually there at the time. **The view's name and every
> existing column are unchanged** (only `household_id` was added); only the numbers a panel
> computes from `plant_name` / `plant_type_id` / `plant_common_name` / `plant_species` can change
> for historical rows — from wrong to right. No parallel `_v2` view was created; there is nothing
> to repoint a dashboard *to*, only numbers to expect to change under an unchanged query. Audited
> every other migration-012 view for the same defect (current-value join instead of an
> interval/SCD2 lookup) and found none affected: `v_sensor_reading_enriched` already keys off
> `sensor_reading.region_id`, a snapshot stamped at insert; `v_sensor_current` /
> `v_board_state_history` / `v_board_state_current` are explicitly current-state views by
> contract; `v_sensor_reading_with_config_debug` keys off `sensor_reading.config_version`, also
> stamped at insert.
>
> **Panels using `v_sensor_reading_with_plant` should expect their historical numbers to change**
> the first time they run after this migration, for any plant that has ever been moved between
> regions. This repo does not check in Grafana dashboard JSON — Grafana queries the database
> directly (`ARCHITECTURE.md` "Query Layer — Analytical Views") — so the affected panel named here is
> the one whose reference query this file documents: the **"Avg Temperature by Plant Type"** panel
> (the `plant_common_name` aggregate below). Any other panel built against this view's `plant_id`,
> `plant_name`, `plant_type_id`, `plant_common_name` or `plant_species` columns is affected the
> same way and should be checked by its operator.
>
> **Cost profile changed materially.** The join is now a per-row `LATERAL` call that walks
> `v_region_path` and queries `plant_region_history` per reading, not a single flat equality join
> — on a view already one row per reading with four joins. The API read path deliberately does
> **not** query this view (series and summaries are served from the granularity tiers instead), so
> `NFR3.3`/`NFR5`'s performance gates do not cover it and it should not be assumed to still be the
> cheap path for a new Grafana panel.

### Temporal accuracy

- **Region** is historically accurate: `sensor_reading.region_id` is snapshotted at insert, so the views join the snapshot — not the sensor's current region.
- **Config version** is historically accurate: `sensor_reading.config_version` is stamped at insert from the in-memory cache.
- **Sensor name** is the *current* name from `sensor_name_history WHERE valid_to IS NULL`. For dashboards showing live or recent data this is almost always correct; for strict point-in-time name lookups query `sensor_name_history` directly.
- **Plant** is historically accurate as of migration 021 (FR72): attribution resolves nearest-ancestor against `plant_region_history` at `recorded_at` — the region (including the reading's own) closest to it on the path with an active plant at that instant, mirroring `leaflab/api/attribution`'s Go resolver exactly (NFR1.c). A plant moved after a reading was recorded does not change that reading's attribution.
- **`household_id`** resolves through the region tree root (`region_path_ids[1]`), since `region.household_id` is populated on tree roots only and descendants inherit it.

### Example queries

```sql
-- Lux readings in "Room A" (or any child region) over the last 24 hours
SELECT recorded_at, value, region_path_name, sensor_name
FROM v_sensor_reading_enriched
WHERE 'Room A' = ANY(region_path_names)
  AND sensor_type_name = 'illuminance'
  AND recorded_at > NOW() - INTERVAL '24 hours'
ORDER BY recorded_at DESC;

-- Average temperature per plant type this week
SELECT plant_common_name, AVG(value) AS avg_temp_c
FROM v_sensor_reading_with_plant
WHERE sensor_type_name = 'temperature'
  AND recorded_at > NOW() - INTERVAL '7 days'
GROUP BY plant_common_name;

-- What config was board X running when a reading spiked?
SELECT recorded_at, value, config_version, device_config_json
FROM v_sensor_reading_with_config_debug
WHERE device_id = 'leaflab-ccdba79f5fac'
  AND value > 90000
ORDER BY recorded_at DESC;

-- Is any board behind on its latest config push?
SELECT b.device_id, b.last_seen_at, bsc.version AS active_version
FROM board b
LEFT JOIN v_board_state_current bsc ON bsc.board_id = b.board_id
WHERE bsc.version IS DISTINCT FROM (
    SELECT MAX(version) FROM device_config
    WHERE board_id = b.board_id AND accepted = TRUE
);
```
