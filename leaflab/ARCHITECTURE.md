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

household                 — Phase 2 ownership root; see "Ownership & Authorization" below
  and its supporting tables (household_membership, board_ownership, household_grant,
  admin_elevation, claim_challenge*, support_reference, departure_record, audit_log) —
  full column-level detail lives in DATA.md, not duplicated here.
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

## Ownership & Authorization (Phase 2)

Full column-level schema for every table named below lives in
[DATA.md](DATA.md#ownership-model) ("Ownership Model" and "A23 Staleness Threshold"). This
section covers the request-time *behavior* the ownership schema exists to support.

### The ownership boundary

Every RPC handler that touches a board, region, plant, sensor, or reading resolves the
entity first (`leaflab/api/authz.Resolver`) and checks the resolution against the caller's
`Scope` (`leaflab/api/authz.Scope`) — **never** a bare household-id comparison. `Scope` is
deliberately not defined as "a household id": the one production implementation
(`HouseholdScope`) happens to hold one household today, but nothing in the interface
constrains a scope to be one household, or its holder to be a household member — a
narrower-than-household scope (e.g. "this one board", held by a non-member) is
representable without changing the interface. This is what lets `household_grant` (below)
and the admin lanes reuse the same authorization plumbing instead of forking a second
code path.

An entity that does not exist and an entity that exists but falls outside the caller's scope
are **indistinguishable on the wire** (NFR2) — both collapse to the same not-found failure.
The distinction is preserved only in server-side logs, for operators.

### Admin standing lane vs. elevation

Holding the `leaflab-admin` realm role (FR12) is **eligibility only** — `requireAdminEligible`
is the gate every admin RPC applies first, and it confers nothing past that gate by itself:

- **Standing lane (FR10.2, `ResolveToHousehold`)** is resolution-only and reachable with
  eligibility alone: given a person, a support reference, or a partial device identifier, it
  resolves to the owning household(s) and FR79's health fields — and nothing else. It does
  not route through `authz.Scope` at all (there is no wider entity to check against, only
  this one dedicated projection), and it is audited once per call regardless of how many
  boards match.
- **Elevation (FR10.1)** is a deliberately entered, time-boxed episode against one target
  household — never standing, always carrying a stated reason, recorded in `admin_elevation`
  with `started_at`/`expires_at`/`ended_at`. Default duration is 60 minutes
  (`DefaultElevationDuration`, configurable — see [api/ENV.md](api/ENV.md)). Only an
  elevation (via `ElevatedScope`, which otherwise behaves exactly like `HouseholdScope`)
  grants reach into a specific household's boards/regions/plants/sensors/readings; eligibility
  alone never does. Elevation does **not** confer FR75's membership-change capability — no
  admin RPC writes `household_membership`.

`isAdminEligible`/eligibility checks are called only from the admin RPC section
(`leaflab/api/server.go`) and never re-derived by an entity-access handler deciding whether to
widen a `Scope` — the elevated-access path (`elevatedBoardScope`) checks the `admin_elevation`
row directly instead.

### The grant model and its three exclusions (FR7)

A household member can grant a named principal (`household_grant`: `grantee_subject`,
`granted_by_subject`, `expires_at`, `revoked_at`) time-boxed write access to their household,
without making them a member. `MemberOrGrantee` (`leaflab/api/authz/capability.go`) is the
one place this is resolved: it builds the same household-scoped `Scope` for a grantee as for
a member, **except** for three named exclusions a grantee may never exercise regardless of
an otherwise-active grant:

1. Grant further access (`CapabilityGrantAccess`) — a grantee cannot re-grant.
2. Change membership (`CapabilityChangeMembership`, FR75) — a grantee cannot add/remove
   household members.
3. Claim, transfer, or release a board (`CapabilityBoardOwnership`, FR76/FR77) — board
   ownership moves are member-only.

Every other "member capability" call site passes `CapabilityOrdinary` and a grantee's write
capability is identical to a member's. Declaring the three exclusions in exactly one place
(`grantExcludedCapabilities`) is what keeps a future requirement from having to re-decide
which operations a grantee is allowed to reach. A grant disappears from the active list on
expiry (evaluated against `NOW()` at request time, no background sweep) or on explicit
one-action revocation (`revoked_at`).

### Where authorization is decided (NFR18.1)

Every authorization and presentation-shaping decision — Scope construction, the three grant
exclusions, admin eligibility/elevation, rounding, coarsening, suppression, and label
selection — is made in `leaflab/api` (the service), never in `leaflab/ui` (the HTMX BFF).
The UI layer holds no service-account credentials or re-mintable tokens of its own and applies
none of those four shaping operations; it forwards the caller's own credentials and renders
exactly what the service returns. `leaflab/ui/nfr18_conformance_test.go` is a same-package
grep-based placeholder that fails the moment shaping logic (`math.Round`, `coarsen`,
`suppress(`, label-selection helpers) appears in `leaflab/ui`'s own checked-in source — a full
cross-package conformance suite (mirroring `tools/app_registry/conformance`) is tracked as a
later NFR1.a task.

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

## Auth Boundary and the Second Deployable (A8, Phase 1)

```
Browser                gRPC client (grpcurl, push-config.sh)
   │                            │
   ▼                            ▼
leaflab-ui (HTMX BFF)      leaflab-api (gRPC)  ◄── libs/go/grpcauth validates the
   │  htmxauth: session,        ▲                   presented token (JWKS, OIDC)
   │  login/callback/logout     │
   └──────── forwards the ──────┘
             logged-in user's own
             access token (never a
             service account)
```

**Two deployables, one auth realm.** `leaflab-api` (gRPC) is the programmatic surface;
`leaflab-ui` is a separate Go HTMX BFF built on `libs/go/htmxauth` + `libs/go/htmxui` — the
`tools/app_registry/ui` / `manmanv2/ui` pattern, the only wiring precedent in this repo (A8).
**grpc-gateway and connect-go were rejected**: A8 records that an HTTP/JSON transport
(grpc-gateway or connect-go) would be friendlier for a browser client, but adopting one would
make this plan the first payer for a repo-wide transport dependency with no other consumer yet
— an HTTP/JSON transport stays a documented follow-up (see root plan #1166 "Deferred" table),
not Phase 1 scope.

**Who validates tokens, where authorization is decided:**

- **Token validation is per-service, not centralized.** `leaflab-api` validates every RPC's
  bearer token itself via `libs/go/grpcauth`'s server interceptor (JWKS against
  `LEAFLAB_API_OIDC_ISSUER`; see `api/ENV.md`). `leaflab-ui` validates the browser's session
  token itself via `libs/go/htmxauth` (its own OIDC login/callback flow, DB-backed session
  store; see `ui/ENV.md`). Neither service trusts a token the other has already validated —
  each dials its own JWKS check.
- **The BFF is transport, not policy (NFR18.1).** `leaflab-ui` forwards the logged-in user's
  own access token to `leaflab-api` on every call (`grpcauth.NewUserTokenDialOption`). It holds
  no service-account credentials of its own and mints no tokens — it cannot act as any
  principal other than the browser session in front of it.
- **Authorization (what a validated principal may do) is decided at `leaflab-api`, not at the
  BFF.** `leaflab-api`'s enforcement interceptor (`api/auth.go`) rejects any RPC that reaches
  it with no `Claims` in context except the one allowlisted anonymous method (`GetHealth`,
  FR63.2). `leaflab-ui` performs no authorization decisions of its own beyond "is there a
  session" — a caller hitting `leaflab-api` directly (grpcurl, `push-config.sh`) gets the same
  authorization outcome as one going through the BFF, because the decision lives at
  `leaflab-api` either way.
- **Phase 1 has no household scoping (A30).** Every authenticated principal can see every
  board today — see `api/ENV.md`'s "Exposure gate (A30)" section for how non-exposure to
  production users is enforced structurally in the meantime (no public Ingress on
  `leaflab-api`, `leaflab-ui` not yet wired into any Helm chart).

**Ports:** `leaflab-api` listens on `50051` (gRPC); `leaflab-ui` listens on `8000` (HTTP). See
[`README.md`](README.md) for `bazel run` commands for both.

---

## Single-Writer Constraint (NFR9)

The device-facing contract is frozen for this plan: the MQTT/AMQP wire contract, the firmware
image, and USB provisioning are unchanged, and devices do not gain an identity provider. **The
processor is in scope for change** — its data-writing logic evolves across this plan's phases —
but its deployment shape does not: `leaflab/processor/BUILD.bazel`'s `release_app` pins
`replicas = 1`, and that pin is a **correctness constraint, not a tuning knob**.

The processor is single-writer of the reading-stamping path by construction. Concurrently
running replicas would race on:

- **`UpsertSensor` (FR16)** — `repository.go`'s sensor upsert reads-then-writes the sensor
  dimension row; two replicas processing overlapping manifests could each decide a sensor is
  new and double-insert it.
- **`UpsertSensorHWHistory` (FR16.1)** — closes the currently-open SCD-2 hardware-address row
  and opens a new one; two replicas racing this close/open pair against the same sensor could
  leave two open rows or lose an address change.
- **`ApplyConfigRegions` (FR1.3)** — applies a config version's region assignments; interleaved
  application from two replicas could apply an older config's regions after a newer one.
- **`SensorCache` invalidation (FR73)** — `cache.go`'s `SensorCache` is an in-process,
  in-memory cache (device→sensor lookups, config version watermarks) with no cross-process
  invalidation. A second replica's cache would silently diverge from the first's as soon as
  either processed a manifest or config ack the other didn't see.

This enumeration is an inventory this plan maintains as sites are added, not a completeness
claim — any future write path added to the processor inherits the same single-writer
requirement until `SensorCache` gets cross-replica coordination (a shared store) and each site
above is revisited for concurrent-write safety. Raising `replicas` without doing that work is a
correctness bug, not a scaling change.

**A device offline for the entire rollout returns and works unchanged** — the single-writer
constraint governs the processor's internal write ordering, not the wire contract a device
observes.

---

## NFR16 — Existing RPCs, Phase 1 vs. Phase 2

The three RPCs `leaflab-api` already had before this plan — `PushDeviceConfig`,
`GetDeviceConfig`, and `ListBoards` (see `api/proto/api.proto`; `GetHealth` is new in Phase 1,
FR63, and is not one of the three) — keep their behaviour for existing callers other than
becoming authenticated. **Phase 1's only change to their result set is
authentication** — a caller that could reach them before now needs a valid token, but an
authenticated caller sees exactly what an unauthenticated one saw before (no household
scoping exists yet; see A30 above). **Household scoping is Phase 2's job**, once FR5 lands: at
that point an authenticated caller's result set narrows to their own household. That
transition is explicit and versioned when it happens — no existing caller silently gets a
wider or narrower result set without a version change.

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
