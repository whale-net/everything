# leaflab-emulator

A local dev tool that simulates one or more leaflab boards: it speaks the
real MQTT wire contract ([`../MQTT.md`](../MQTT.md)) — manifest, readings,
config apply/ack, LWT — using synthetic sensor values instead of real
hardware.

**What it is not:**
- **Not deployed anywhere.** `release_app`'s `deploy_unit = "none"`
  (`BUILD.bazel`) keeps it out of `leaflab_chart` and every K8s environment.
  It is built and published like any other leaflab app, but only ever runs
  under `tilt up`.
- **Not on the capability roadmap.** It exists purely so a developer can
  exercise the MQTT write path and the rest of the pipeline without a
  physical board.
- **Not a physical simulation.** Values are randomized within a plausible
  range and random-walked tick to tick (see "Synthetic values" below) — no
  day/night cycles, no cross-sensor correlation, no environmental modeling.

## Quickstart

**As a `tilt up` resource (the default).** `cd leaflab && tilt up` brings up
`leaflab-emulator` alongside everything else — no manual step. It loads the
scenario fixtures baked into its image and starts one simulated board per
scenario file.

**Standalone, against a running Tilt broker:**

```bash
MQTT_BROKER_URL=tcp://localhost:1883 \
MQTT_USERNAME=rabbit \
MQTT_PASSWORD=password \
SCENARIO_DIR=leaflab/scripts/scenarios \
bazel run //leaflab/emulator:emulator
```

`MQTT_BROKER_URL=tcp://localhost:1883` targets Tilt's host port-forward for
the RabbitMQ MQTT plugin — the same broker `mosquitto_sub` and physical
boards use, so a standalone run and a `tilt up`-managed run are
indistinguishable on the wire. See [`ENV.md`](ENV.md) for every variable and
its default.

## Selecting boards — `EMULATOR_BOARDS`

Comma-separated list of `<scenario>[:<count>][@<device_id>]` entries, e.g.:

```
EMULATOR_BOARDS="mux-light-temp:3,single-light@leaflab-deadbeef0001"
```

- `<scenario>` — a scenario file's basename (no `.json`); see
  [`../scripts/scenarios/`](../scripts/scenarios/) for the full, current
  list rather than duplicating it here — `ls leaflab/scripts/scenarios/` or
  `./push-config.sh <device_id> --list`.
- `[:<count>]` — how many boards to spawn from that scenario; default `1`.
- `[@<device_id>]` — pin the device_id instead of deriving it; only legal
  with `count` 1 (or omitted).

Leave `EMULATOR_BOARDS` unset (the Tilt default) to get one board per
scenario file in `SCENARIO_DIR`.

## Device IDs are deterministic across restarts

Each simulated board's `device_id` is derived from its scenario name and
index (`leaflab-<12 hex>`, matching the real eFuse-MAC-derived format —
[`../MQTT.md`](../MQTT.md)) — **not** randomized per process start. That
means a board's claim, rename, and ownership state in the database survives
`tilt down` / `tilt up`: the same simulated board comes back as the same
board, not a fresh unclaimed one. This is the property that makes the
emulator useful for anything beyond a single session — override it with
`@<device_id>` only when you specifically need a fixed ID (e.g. matching a
value already claimed in your dev DB).

## Observing it

```bash
# Raw MQTT traffic from every simulated (and real) board
mosquitto_sub -h localhost -p 1883 -u rabbit -P password -t 'leaflab/#' -v

# Which device_ids the emulator assigned on this run
kubectl logs -n leaflab-local-dev deploy/leaflab-emulator | grep -i device

# leaflab-ui, to browse/claim/rename simulated boards like real ones
open http://localhost:8080
```

## Exercising the config path

Push a config either via `grpcurl` directly against `leaflab-api`:

```bash
grpcurl -plaintext -d '{"device_id":"<id>","sensors":[{"i2c_address":35,"name":"my_light"}]}' \
    localhost:50051 leaflab.api.v1.LeafLabAPI/PushDeviceConfig
```

or via the helper script, which pushes one of the same scenario files the
emulator itself can load:

```bash
leaflab/scripts/push-config.sh <device_id> <scenario>
```

Then watch for the ack:

```bash
mosquitto_sub -h localhost -p 1883 -u rabbit -P password -t 'leaflab/<device_id>/config/ack' -v
```

**Version rule:** a pushed config's `version` must be strictly greater than
the board's currently-applied version or the emulator rejects it
(`accepted=false`, unchanged state) — same as real firmware. A simulated
board's applied version resets to `0` whenever the emulator process
restarts (it is in-memory only; there is no NVS to persist it), so a config
that was previously accepted must be re-pushed after `tilt down` / `tilt
up` even though the board's `device_id` itself stayed the same.

## Synthetic value ranges

Each `SensorType` is randomized uniformly within a fixed range on first
publish, then random-walks (a small per-tick delta, clamped back into
range) on every later publish — see `valuegen.go`.

| Sensor type | Range | Unit |
|-------------|-------|------|
| Illuminance | 0 – 2000 | `lx` |
| Temperature | 15 – 35 | `°C` |
| Humidity | 20 – 90 | `%RH` |
| eCO2 | 400 – 2000 | `ppm` |
| TVOC | 0 – 1000 | `ppb` |

If you see a reading of 1,700 lx, it is fabricated, not a real light level.

## Known limits (deliberate)

- **Single-hop mux only** — mirrors the real firmware (LB7); a scenario
  with a multi-hop `muxPath` fails to load rather than being silently
  flattened.
- **No physical modeling** — no day/night cycles, no correlation between
  sensors on the same board, no drift beyond the per-tick random walk.
- **No UI for managing emulator instances** — boards are chosen entirely
  via `EMULATOR_BOARDS` at process start; there is no way to add/remove a
  simulated board without restarting the emulator.
- **Config state is in-process only** — a board's applied config version
  and sensor overrides live in the `Runner`'s memory, not on disk; they are
  lost on restart (see "Version rule" above).
