# LeafLab — Architecture

## System Overview

```
┌─────────────────────────────────────────────────────────┐
│                    LeafLab Device                       │
│                                                         │
│  ┌──────────────────────────────────────────────────┐   │
│  │              sensorboard firmware                │   │
│  │                                                  │   │
│  │  sensorboard_main.cc (generic loop)              │   │
│  │    ↓ calls GetBus() + GetSensors()               │   │
│  │  elegoo_config.cc (board-specific wiring)        │   │
│  │    ↓ provides ArduinoI2CBus + BH1750Sensor       │   │
│  └────────────────┬─────────────────────────────────┘   │
│                   │ II2CBus                              │
│  ┌────────────────▼────────────────────────────────┐    │
│  │          firmware/ libraries                    │    │
│  │  ISensor, SensorReading, II2CBus, MQTTWriter    │    │
│  └─────────────────────────────────────────────────┘    │
│                   │ Wire / I2C bus                       │
│  ┌────────────────▼────────────────────────────────┐    │
│  │          Physical Sensors                       │    │
│  │  BH1750 @ 0x23 (ambient light, lux)             │    │
│  └─────────────────────────────────────────────────┘    │
└───────────────────────┬─────────────────────────────────┘
                        │ MQTT / Wi-Fi
                  MQTT Broker
                        │
                  Cloud pipeline  [not yet implemented]
```

---

## Key Design Decisions

### Link-Seam Board Configuration

The main loop (`sensorboard_main.cc`) is a `cc_library` that calls two functions with no implementation:

```cpp
firmware::II2CBus& GetBus();
pw::span<firmware::ISensor* const> GetSensors();
```

These are implemented in a board-specific config file (`elegoo_config.cc`) that is linked into the `esp32_firmware()` target. Bazel resolves which config file to use at build time — no `#ifdef`, no runtime config, no YAML.

**To add a new board config:** create a new `*_config.cc` and a new `esp32_firmware()` target in `BUILD.bazel`. The main loop is unchanged.

### Sensor Registry

Sensors are declared as static instances in the config file:

```cpp
static firmware::ArduinoI2CBus bus;
static firmware::BH1750Sensor  bh1750(bus, 0x23, "light", millis);
static firmware::ISensor* const kSensors[] = {&bh1750};
```

The `kSensors` array is a compile-time constant. Adding a sensor is: declare an instance, add a pointer to `kSensors[]`, add the dep to `BUILD.bazel`. No registration macros, no dynamic lists.

### Non-Blocking Sensor Reads

Sensors with hardware measurement delays (e.g. BH1750 needs 180 ms) use a state machine driven by a clock function:

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

## Data Flow

```
setup():
  GetBus().Init(SDA, SCL)
  for each sensor: sensor.Init()
    → send power-on command via II2CBus::Write()
    → send trigger command (starts first measurement)

loop():
  for each sensor: sensor.Read()
    → if measurement window elapsed: retrieve result, re-arm
    → return SensorReading { value, valid }
  log valid readings via PW_LOG_INFO

[future] MQTTWriter.PublishAll():
  → format topic: "leaflab/sensorboard/<sensor.name()>"
  → format payload: "%.2f"
  → IPublisher::Publish()
```

---

## Future Directions

### Cloud Pipeline

The sensorboard publishes `DeviceManifest` (retained) and `SensorReading` protos over MQTT. A future processor service will subscribe to `leaflab/+/manifest` and `leaflab/+/sensor/+`, decode the protos, and handle Home Assistant MQTT Discovery and storage routing. See [`MQTT.md`](MQTT.md) for the topic structure.

### Multiple Sensor Types

Add sensor implementations to `firmware/sensor/` following the `ISensor` pattern. Each new sensor:
- Takes `II2CBus&` and a clock function in its constructor
- Is host-testable via `FakeI2CBus`
- Is added to a board config with one line

### TCA9548A I2C Multiplexer

Implement a `TCA9548ABus` that wraps another `II2CBus` and selects a channel before each operation. The sensor implementations are unchanged — they only see `II2CBus&`. Config files can swap in the mux bus without touching any sensor code.

### Multiple Board Configs

A board with 4 sensors on a TCA9548A and a board with 1 sensor on the native bus differ only in their `*_config.cc`. The same `sensorboard_main.cc` and the same sensor implementations are reused.
