# LeafLab MQTT Protocol

## Broker

Configured per-device via NVS provisioning:

```
bazel run //leaflab/sensorboard:provision -- /dev/ttyUSB0 \
  wifi_ssid=MySSID wifi_pass=MyPass mqtt_host=192.168.1.42 mqtt_port=1883
```

## Topic Structure

```
leaflab/<device_id>/status              plain string  "online" / "offline"
leaflab/<device_id>/manifest            proto         DeviceManifest  (retained)
leaflab/<device_id>/sensor/<name>       proto         SensorReading
```

`<device_id>` is derived from the ESP32 eFuse base MAC at boot, formatted as
`leaflab-<12 hex chars>` (e.g. `leaflab-a4cf12ab34cd`). It is stable across
firmware reflashes and unique per chip.

`<name>` is the sensor's logical name as set in the board config file
(e.g. `light`, `light_canopy`, `light_0x23`). Names must be unique per device.

## Protobuf Schema

Source: `firmware/proto/firmware.proto`

### `DeviceManifest` — published retained on connect

Describes all sensors on the board. Processors subscribe to
`leaflab/+/manifest` to discover new devices.

```proto
message DeviceManifest {
  string device_id = 1;
  repeated SensorDescriptor sensors = 2;
}

message SensorDescriptor {
  string     name = 1;
  SensorType type = 2;
  string     unit = 3;
}

enum SensorType {
  SENSOR_TYPE_UNKNOWN     = 0;
  SENSOR_TYPE_ILLUMINANCE = 1;   // lx
  SENSOR_TYPE_TEMPERATURE = 2;   // °C
  SENSOR_TYPE_HUMIDITY    = 3;   // %RH
}
```

### `SensorReading` — published each loop while connected

```proto
message SensorReading {
  float  value     = 1;
  uint32 uptime_ms = 2;
}
```

## LWT (Last Will and Testament)

Set at connect time: if the device disconnects unexpectedly the broker
publishes `"offline"` to `leaflab/<device_id>/status` automatically.
On clean connect the device publishes `"online"` to the same topic.

## Multi-tenancy

Devices share a single MQTT vhost. Isolation is by `device_id` — each device
has a unique topic namespace. A downstream processor subscribes to
`leaflab/+/manifest` and `leaflab/+/sensor/+` and handles HA registration.
Devices never register directly with Home Assistant.

## Processor Integration (future)

The processor service will:
1. Subscribe to `leaflab/+/manifest` — decode `DeviceManifest`, register HA entities via MQTT Discovery
2. Subscribe to `leaflab/+/sensor/+` — decode `SensorReading`, route to storage / HA state topics

HA MQTT Discovery config topic per sensor:
```
homeassistant/sensor/<device_id>_<sensor_name>/config
```
