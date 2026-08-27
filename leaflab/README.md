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
| `api/` | gRPC API (`leaflab-api`) — device config push, board listing, health (port `50051`) |
| `ui/` | HTMX browser BFF (`leaflab-ui`) — the second deployable (A8); forwards the logged-in user's own token to `api/` (port `8000`) |

---

## Quick Start

```bash
# Build sensorboard firmware
bazel build //leaflab/sensorboard:sensorboard --config=esp32

# Flash to a connected ESP32 over USB
bazel run //leaflab/sensorboard:flash -- /dev/ttyUSB0

# Monitor serial output
bazel run //leaflab/sensorboard:serial

# Provision Wi-Fi + MQTT credentials (first time)
bazel run //leaflab/sensorboard:provision -- /dev/ttyUSB0 \
  wifi_ssid=MySSID wifi_pass=MyPass \
  mqtt_host=192.168.1.42 mqtt_port=1883
```

See [`sensorboard/README.md`](sensorboard/README.md) for full build, flash, and extension instructions.

---

## The two deployables (A8)

`leaflab-api` (gRPC, programmatic surface) and `leaflab-ui` (HTMX BFF, browser surface) are
separate deployables that share one Postgres database and one Keycloak realm. See
[`ARCHITECTURE.md`](ARCHITECTURE.md#auth-boundary-and-the-second-deployable-a8-phase-1) for the
auth boundary between them.

| Deployable | Port | Protocol | Started by Tilt? |
|------------|------|----------|-------------------|
| `leaflab-api` | `50051` | gRPC | Yes — `tilt up` (see `Tiltfile`) |
| `leaflab-ui` | `8000` | HTTP | Not yet — run directly (below); Helm/Tilt wiring is a later task on this plan |

```bash
# Start RabbitMQ, Postgres, leaflab-migrate, leaflab-processor, leaflab-api
cd leaflab && tilt up

# Run leaflab-api standalone (without Tilt), unauthenticated dev mode:
LEAFLAB_API_AUTH_MODE=none LEAFLAB_API_DEV_MODE=true \
PG_DATABASE_URL=postgres://postgres:password@localhost:5432/leaflab?sslmode=disable \
bazel run //leaflab/api:api

# Run leaflab-ui — points at a Tilt-provisioned (or standalone) leaflab-api:
PG_DATABASE_URL=postgres://postgres:password@localhost:5432/leaflab?sslmode=disable \
LEAFLAB_API_URL=localhost:50051 \
bazel run //leaflab/ui:ui
# → http://localhost:8000
```

See [`api/ENV.md`](api/ENV.md) and [`ui/ENV.md`](ui/ENV.md) for every environment variable,
including OIDC settings for authenticated (non-dev) runs.

---

## Pushing device config (`push-config.sh`)

`leaflab/scripts/push-config.sh` pushes a named scenario config (JSON files
in `leaflab/scripts/scenarios/`) to a device via `PushDeviceConfig`. It is
authenticated and no longer depends on server reflection (FR81, FR11.1):

- **Credential** — an OIDC device authorization grant (RFC 8628), obtained
  via `leaflab/scripts/authtoken`, a thin wrapper around
  [`libs/go/grpcauth`'s `DeviceFlowAccessToken`](../libs/go/grpcauth/README.md#deviceflowaccesstoken--for-non-go-callers-shell-scripts-grpcurl).
  This resolves to **your own principal** — the same subject and realm roles
  as a browser login — never a service account (A25).
- **Service contract** — resolved from the published descriptor set Bazel
  artifact, `//leaflab/api:leaflab_api_descriptor_set`, via `grpcurl
  -protoset`, not server reflection. `leaflab-api` turns reflection off
  outside `LEAFLAB_API_DEV_MODE=true` (FR11.1), so a caller that still
  assumed reflection would break the moment it hit anything but a dev
  server.

**One-time setup**, per realm/client, before the first push:

```bash
export LEAFLAB_API_OIDC_ISSUER=https://auth.example.com/realms/whale
export LEAFLAB_DEVICE_FLOW_CLIENT_ID=leaflab-cli   # public client — see
                                                     # libs/go/grpcauth/KEYCLOAK.md
                                                     # "Device authorization
                                                     # grant (FR81)"

bazel run //leaflab/scripts/authtoken:authtoken -- login
```

This prints a verification URL and code, then polls until you approve it in
a browser. The resulting refresh token is cached under your user config dir
(mode `0600`); every later invocation of `push-config.sh` refreshes it
silently — `LEAFLAB_API_OIDC_ISSUER` and `LEAFLAB_DEVICE_FLOW_CLIENT_ID` must
stay set in your shell (or exported in your profile) for that refresh to
find the right realm/client.

**Everyday use** — same as before, `push-config.sh <device_id> <scenario>`:

```bash
./leaflab/scripts/push-config.sh leaflab-ccdba79f5fac single-light
```

The script builds (or reuses, if already built) the descriptor set and the
`authtoken` binary via `bazel build`, obtains a token non-interactively, and
fails with an actionable message — pointing at the `authtoken login` command
above — instead of hanging if no credential is cached yet.

`LEAFLAB_API_HOST` still selects the target (`localhost:50051` by default).
`LEAFLAB_DESCRIPTOR_SET` / `LEAFLAB_AUTHTOKEN_BIN` let you point at
pre-built artifacts (mainly for tests) instead of invoking `bazel build`.

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

    SensorNameHistory {
        bigserial sensor_name_history_id PK
        bigint sensor_id FK
        varchar name
        timestamptz valid_from
        timestamptz valid_to "null if current"
    }

    SensorHWHistory {
        bigserial history_id PK
        bigint sensor_id FK
        jsonb mux_path
        timestamptz valid_from
        timestamptz valid_to "null if current"
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
        timestamptz valid_from
        timestamptz valid_to "null if current"
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
    Sensor ||--o{ SensorNameHistory : "name history"
    Sensor ||--o{ SensorHWHistory : "hw address history"
    Sensor ||--o{ SensorRegionHistory : "region history"
    Region ||--o{ SensorRegionHistory : "recorded in"
    Sensor ||--o{ SensorReading : "produces"
    Board ||--o{ DeviceConfig : "configs"
```

Key design decisions:
- `sensor` is a stable dimension anchor — rename via config closes the old `sensor_name_history` row, opens new; `sensor_id` and reading history are unchanged
- `sensor.region_id` is a current-value cache; `sensor_region_history` records every assignment (SCD-2, `valid_from`/`valid_to`)
- `sensor_reading.region_id` is snapshotted at insert so historical location is preserved when sensors move
- `sensor_reading.config_version` records which `DeviceConfig` was active at write time
- `sensor.mux_path` is JSONB supporting arbitrary mux cascade depth
- `device_config.config_json` stores protojson for human-readable SQL queries; device NVS uses binary nanopb
- Seven `v_` views expose all join logic for Grafana panels — see [DATA.md](DATA.md#analytical-views)

---

## Relationship to `//firmware`

LeafLab firmware is built on top of the board-agnostic libraries in [`firmware/`](../firmware/README.md):

- `firmware/sensor` — `ISensor` interface, `SensorReading`, `BH1750Sensor`, thermistor
- `firmware/i2c` — `II2CBus`, `ArduinoI2CBus`, `FakeI2CBus`
- `firmware/mqtt` — `MQTTWriter` sensor aggregator
- `firmware/network` — Wi-Fi + MQTT state machine

LeafLab board configs (`elegoo_config.cc`) wire together these libraries with concrete hardware addresses and pin assignments. The libraries themselves have no LeafLab-specific knowledge.
