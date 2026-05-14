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
