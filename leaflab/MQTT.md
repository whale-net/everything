# LeafLab MQTT Protocol

## Broker

Configured per-device via NVS provisioning:

```
bazel run //leaflab/sensorboard:provision -- /dev/ttyUSB0 \
  wifi_ssid=MySSID wifi_pass=MyPass mqtt_host=192.168.1.42 mqtt_port=1883
```

## Topic Structure

```
leaflab/<device_id>/status              plain string   "online" / "offline"
leaflab/<device_id>/manifest            proto          DeviceManifest  (retained)
leaflab/<device_id>/sensor/<name>       proto          SensorReading
leaflab/<device_id>/config              proto          DeviceConfig    (server → device)
leaflab/<device_id>/config/ack          proto          DeviceConfigAck (device → server)
```

`<device_id>` is derived from the ESP32 eFuse base MAC at boot, formatted as
`leaflab-<12 hex chars>` (e.g. `leaflab-a4cf12ab34cd`). It is stable across
firmware reflashes and unique per chip.

`<name>` in sensor topics is the sensor's logical name from the board config or
overridden by a pushed `DeviceConfig`. Names must be unique per device.

## Protobuf Schema

Source files: `firmware/proto/firmware.proto`, `firmware/proto/config.proto`

### `DeviceManifest` — published retained on connect and after config apply

Describes all sensors currently active on the board.

```proto
message DeviceManifest {
  string device_id = 1;
  repeated SensorDescriptor sensors = 2;
}

message SensorDescriptor {
  string     name        = 1;
  SensorType type        = 2;
  string     unit        = 3;
  uint32     i2c_address = 4;  // 0 if unknown (legacy)
  uint32     mux_address = 5;  // outermost mux; 0 if direct on root bus
  uint32     mux_channel = 6;
  string     chip_model  = 7;  // e.g. "BH1750", "SHT3x"
}
```

### `SensorReading` — published each loop while connected

```proto
message SensorReading {
  float  value     = 1;
  uint32 uptime_ms = 2;
}
```

### `DeviceConfig` — pushed server → device

Overrides logical configuration for sensors matched by hardware address.

```proto
message DeviceConfig {
  string device_id              = 1;
  uint64 version                = 2;  // monotonic; device rejects version <= current
  repeated SensorConfig sensors = 3;
}

message SensorConfig {
  repeated MuxHop mux_path    = 1;  // empty = sensor directly on root bus
  uint32 i2c_address          = 2;
  string name                 = 3;  // overrides compile-time name in manifest + topics
  bool   enabled              = 4;
  uint32 poll_interval_ms     = 5;  // 0 = use device default
  uint32 region_id            = 7;  // assigned by server; ignored by firmware
}

message MuxHop {
  uint32 mux_address = 1;
  uint32 mux_channel = 2;
}
```

### `DeviceConfigAck` — published device → server after apply

```proto
message DeviceConfigAck {
  string device_id       = 1;
  uint64 applied_version = 2;
  bool   accepted        = 3;
  string reason          = 4;  // rejection reason when accepted=false
}
```

## Config Flow

1. Server pushes `DeviceConfig` to `leaflab/<device_id>/config`
2. Device validates version (rejects if version ≤ current), matches entries to sensors by `(mux_path, i2c_address)`, applies name/enabled/poll overrides, saves to NVS
3. Device re-publishes `DeviceManifest` with updated names
4. Device publishes `DeviceConfigAck` with `accepted=true`

On rejection: ack published with `accepted=false` and a reason string; NVS unchanged.

Config persists across reboots. On boot the device loads stored config before connecting to MQTT.

## LWT (Last Will and Testament)

Set at connect time: if the device disconnects unexpectedly the broker
publishes `"offline"` to `leaflab/<device_id>/status` automatically.
On clean connect the device publishes `"online"` to the same topic.

## RabbitMQ Routing

The MQTT plugin routes `leaflab/#` to the `amq.topic` exchange, replacing
`/` with `.` in routing keys. The Go processor binds `leaflab.#` and switches
on routing key parts:

| Pattern | Handler |
|---------|---------|
| `leaflab.<device>.manifest` | decode `DeviceManifest`, upsert board + sensors |
| `leaflab.<device>.sensor.<name>` | decode `SensorReading`, write to TimescaleDB |
| `leaflab.<device>.config` | decode `DeviceConfig`, persist JSONB to `device_config` |
| `leaflab.<device>.config.ack` | decode `DeviceConfigAck`, mark accepted, apply regions |

## Corrective config push (FR9 / NFR4)

`leaflab-processor` is a **second publisher** on `leaflab.<device>.config`,
alongside `leaflab-api`'s `PushDeviceConfig` — both publish the same
`DeviceConfig` message shape to the same exchange/routing key, and both
assign versions through the same atomic next-version pattern so they can
never collide on one `device_config.version` for a board.

**Trigger.** Handling an incoming `manifest` (`handleManifest`), the
processor compares each sensor's device-reported name against the name the
DB held for that sensor immediately before this manifest's own writes (the
processor's own `sensor`/`sensor_name_history` state — no query to
`leaflab-api`). A difference means the device's self-reported name has
drifted from an owner-authorized rename (FR4) or a prior config push (FR8)
that the device hasn't actually persisted to NVS. The processor then
composes the board's full desired sensor list — via the same
`leaflab/configcompose.ComposeDesiredSensors` function `PushDeviceConfig`
uses, guaranteeing composition parity — and publishes it as a corrective
`DeviceConfig`, entirely within its own process (no RPC to `leaflab-api`;
see `ARCHITECTURE.md`'s FR9 carve-out).

**NFR4 storm guards**, both backed by Postgres columns on `sensor`
(`corrective_push_attempts`, `corrective_push_outstanding_version`), never
in-memory (the processor is `replicas=1` and restart-prone):

- **Concurrent guard.** While a corrective push for a sensor is outstanding
  (issued, not yet acked), a subsequent manifest reporting the same stale
  name does not trigger a second one. Handling `config.ack` clears the
  outstanding marker (on both accept and reject) without touching the
  attempt counter.
- **Sequential/reconnect-storm guard.** After 3 consecutive
  acked-but-unconverged reconnect-triggered round trips for a sensor, the
  processor stops auto-issuing corrective pushes for it — logging WARNING
  on each of the first two failed attempts and ERROR on the third (giving
  up; log-only, no alert route). Only a fresh FR4 rename or an explicit FR8
  push for that sensor resets the counter.
