# leaflab/sensorboard — Agent Instructions

## What this is

Single ESP32 firmware binary that supports any I2C sensor topology. No sensors are compiled in — push a `DeviceConfig` proto via MQTT to declare what chips are wired. Config is persisted in NVS and reloaded on boot.

## Build

```
bazel build //leaflab/sensorboard:sensorboard_elf --config=esp32
```

## Flash

```
# 1. Pause the serial daemon (it holds the port)
# 2. Flash
bazel run //leaflab/sensorboard:flash --config=esp32 -- /dev/ttyUSB0
# 3. Resume daemon
```

Via MCP tools (preferred in agent sessions):
```
mcp__serial-mcp__serial_pause   → pause
bazel run //leaflab/sensorboard:flash --config=esp32 -- /dev/ttyUSB0
mcp__serial-mcp__serial_resume  → resume
```

## Provision NVS credentials (one-time per device)

```
bazel run //leaflab/sensorboard:provision -- /dev/ttyUSB0 \
  wifi_ssid=MySSID wifi_pass=MyPass \
  mqtt_host=192.168.1.42 mqtt_user=rabbit mqtt_pass=password
```

Credentials survive firmware reflashes. Lost after `erase_flash`.

## Push a sensor config

```
bash leaflab/scripts/push-config.sh <device_id> <scenario>
bash leaflab/scripts/push-config.sh leaflab-ccdba79f5fac single-light
bash leaflab/scripts/push-config.sh leaflab-ccdba79f5fac light-mux-ch6
```

Scenarios are JSON files in `leaflab/scripts/scenarios/`. Each entry needs `chipType` set (e.g. `CHIP_TYPE_BH1750`) or the factory mode will not instantiate the sensor.

## Validate

1. Check serial for config apply and sensor init:
   ```
   mcp__serial-mcp__serial_tail
   mcp__serial-mcp__serial_grep pattern="config.*applied|ERR|factory apply"
   ```

2. Confirm readings in DB:
   ```sql
   SELECT s.name, sr.value, s.unit, sr.recorded_at
   FROM sensor_reading sr
   JOIN sensor s ON sr.sensor_id = s.sensor_id
   JOIN board b ON s.board_id = b.board_id
   WHERE b.device_id = '<device_id>'
   ORDER BY sr.recorded_at DESC LIMIT 10;
   ```
   Use `mcp__postgres-leaflab__execute_sql` for this.

3. Check device config ACK:
   ```
   mosquitto_sub -h localhost -p 1883 -u rabbit -P password \
     -t 'leaflab/<device_id>/config/ack' -v
   ```

## Reset lifecycle

| Trigger | Effect |
|---------|--------|
| Power cycle / normal boot | Load NVS config → apply → connect |
| MQTT config push (`leaflab/<device_id>/config`) | Queued in callback, applied from `loop()` (I2C-safe) |
| MQTT command `"reset"` (`leaflab/<device_id>/command`) | Publish offline → soft restart (config preserved) |
| MQTT command `"factory_reset"` | Publish offline → erase NVS config → restart |
| GPIO 0 (BOOT button, hold) | Soft restart (config preserved) |

Send a reset command:
```
mosquitto_pub -h localhost -p 1883 -u rabbit -P password \
  -t 'leaflab/<device_id>/command' -m 'reset'
```

## I2C hardware notes

- SDA = GPIO 21, SCL = GPIO 22
- TCA9548A mux default address: 0x70 (A0/A1/A2 all GND)
- BH1750 default address: 0x23 (ADDR pin low)
- If the I2C bus is stuck (no devices found on scanner): the slave device needs a full power cycle (not just MCU reset) to clear a mid-transaction state
- Sensor init is deferred to first Read() — a failed init at config-push time is retried automatically
- Config is applied in the MQTT callback; I2C init inside the callback can fail if WiFi just connected (transient ESP_ERR_INVALID_STATE). The lazy-init retry at poll time recovers from this.

## I2C scanner

```
bazel run //tools/firmware/esp32/i2c_scanner:flash --config=esp32 -- /dev/ttyUSB0
```

Use this to verify wiring before debugging firmware.

## Board pins (Elegoo ESP32)

| Role | GPIO |
|------|------|
| SDA  | 21   |
| SCL  | 22   |
| LED  | 2    |

## Device ID format

`leaflab-<mac>` where `<mac>` is the 6-byte eFuse base MAC with no separators.
Example: `leaflab-ccdba79f5fac` (MAC `cc:db:a7:9f:5f:ac`)
