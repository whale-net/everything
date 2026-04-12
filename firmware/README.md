# Firmware Application Layer

Board-agnostic C++ libraries for embedded sensor applications.
All libraries compile and test on the host machine — no hardware required.

For the build infrastructure (toolchains, board platforms, flashing) see [`tools/firmware/README.md`](../tools/firmware/README.md).
For a worked example of these libraries in use see [`leaflab/sensorboard/README.md`](../leaflab/sensorboard/README.md).

---

## Directory Layout

```
firmware/
  sensor/
    sensor.h            ISensor interface + SensorReading return type
    mock_sensor.h       FakeSensor, RecordingSensor (host-side test doubles)
    bh1750.h/.cc        BH1750 ambient light sensor (II2CBus-backed)
    thermistor.h/.cc    NTC thermistor via ADC voltage divider
    BUILD.bazel
  i2c/
    i2c_bus.h           II2CBus — board-agnostic I2C interface
    arduino_i2c_bus.h   Wire-backed implementation (device-only)
    fake_i2c_bus.h      Transaction-recording test double (host)
    BUILD.bazel
  mqtt/
    mqtt_writer.h       MQTTWriter — zero-allocation sensor aggregator
    mqtt_writer.cc
    mock_publisher.h    FakePublisher — captures published messages in tests
    BUILD.bazel
  network/
    network_manager.h   Non-blocking Wi-Fi + MQTT state machine
    network_manager.cc
    BUILD.bazel
  timing/
    loop_timer.h        pw_chrono-based non-blocking periodic timer
    BUILD.bazel
```

---

## Running Tests

All tests run on the host (no ESP32 needed):

```bash
bazel test //firmware/...

# Individual suites
bazel test //firmware/sensor:bh1750_test
bazel test //firmware/sensor:thermistor_sensor_test
bazel test //firmware/mqtt:mqtt_writer_test
bazel test //firmware/network:network_manager_test
bazel test //firmware/timing:loop_timer_test
```

---

## ISensor Interface (`firmware/sensor/sensor.h`)

The central abstraction. Every physical sensor implements this interface.

```cpp
struct SensorReading {
    float value;
    bool  valid;

    static SensorReading Ok(float v);   // valid reading
    static SensorReading Invalid();     // no reading available
};

class ISensor {
 public:
  virtual pw::Status   Init()          = 0;  // called once from setup()
  virtual SensorReading Read()         = 0;  // non-blocking; never delay()
  virtual const char*  name()  const   = 0;  // MQTT sub-topic / log identifier
  virtual uint8_t      address() const = 0;  // I2C address / channel
};
```

**`SensorReading` vs raw `float`:** Sensors that haven't completed their first measurement, or that encounter a hardware error, return `SensorReading::Invalid()` instead of a sentinel float (NaN, -1, etc.). Callers check `r.valid` before using `r.value` — no magic numbers required.

### Compile-time dependency injection

Board config files instantiate the real sensor types and expose them through two link-seam functions:

```cpp
// leaflab/sensorboard/elegoo_config.cc
static firmware::ArduinoI2CBus bus;
static firmware::BH1750Sensor  bh1750(bus, 0x23, "light", millis);
static firmware::ISensor* const kSensors[] = {&bh1750};

firmware::II2CBus&               GetBus()     { return bus; }
pw::span<firmware::ISensor* const> GetSensors() { return kSensors; }
```

No heap allocation. No `new`. The array is statically allocated; the span is a non-owning view.

---

## II2CBus Interface (`firmware/i2c/i2c_bus.h`)

Board-agnostic I2C bus abstraction used by all I2C sensor implementations.

```cpp
class II2CBus {
 public:
  virtual pw::Status Init(uint8_t sda_pin, uint8_t scl_pin) = 0;
  virtual pw::Status Write(uint8_t address, const uint8_t* data, size_t len) = 0;
  virtual pw::Status Read(uint8_t address, uint8_t* buf, size_t len) = 0;
  virtual pw::Status ReadRegister(uint8_t address, uint8_t reg,
                                  uint8_t* buf, size_t len) = 0;
  virtual pw::Status WriteRegister(uint8_t address, uint8_t reg,
                                   const uint8_t* data, size_t len) = 0;
};
```

Concrete implementations:

| Class | Target | Notes |
|-------|--------|-------|
| `ArduinoI2CBus` | ESP32 device only | Wraps `Wire.h`; `target_compatible_with` enforced in BUILD |
| `FakeI2CBus` | Host tests | Records all transactions; supports preset responses and error injection |

Multiple sensors share one `II2CBus` instance — I2C is a shared bus by design.

---

## BH1750Sensor (`firmware/sensor/bh1750.h`)

Ambient light sensor (lux). Protocol: write power-on command, trigger one-shot measurement, wait 180 ms, read 2 bytes.

```cpp
// On device — millis is Arduino's elapsed-time function
BH1750Sensor bh1750(bus, /*address=*/0x23, /*name=*/"light", millis);

// In tests — pass a stub so you control timing
static uint32_t fake_ms = 0;
BH1750Sensor bh1750(bus, 0x23, "light", []() -> uint32_t { return fake_ms; });
```

`Read()` is **non-blocking**: it returns the cached lux value while the hardware integrates, then reads the result and re-arms itself after `kMeasureTimeMs` (180 ms) elapses. Never calls `delay()`.

**Address:** `ADDR` pin to GND → `0x23`. `ADDR` to VCC → `0x5C`.

---

## MQTTWriter (`firmware/mqtt/mqtt_writer.h`)

Iterates the sensor array, calls `Read()` on each, checks `reading.valid`, and publishes valid readings.

```
topic:   "<prefix>/<sensor.name()>"   e.g.  "leaflab/sensorboard/light"
payload: "%.2f"                        e.g.  "142.50"
```

Sensors that failed `Init()` or return `SensorReading::Invalid()` are skipped with a log warning.

### IPublisher

```cpp
class IPublisher {
 public:
  virtual pw::Status Publish(const char* topic, const char* payload) = 0;
};
```

- **Real** (`RealPublisher`, on-device): wraps `PubSubClient::publish()`
- **Fake** (`FakePublisher`, tests): captures messages in a `std::vector`

---

## NetworkManager (`firmware/network/network_manager.h`)

Non-blocking Wi-Fi + MQTT state machine. Call `Poll()` every `loop()` iteration — it returns in < 1 ms.

```
kIdle ──Connect()──► kConnecting ──both up──► kReady ──lost──► kBackoff
                           │                                        │
                           └──────────── 15 s timeout ─────────────┘
                                                │
                                   exponential delay expires
                                   (1 s → 2 s → 4 s … → 64 s cap)
                                                │
                                           kConnecting
```

```cpp
NetworkManager net(config);
net.Connect();

void loop() {
    net.Poll();
    if (net.state() == NetworkManager::State::kReady) {
        net.Publish(topic, payload);  // returns Unavailable() if not ready
    }
    esp_task_wdt_reset();
}
```

---

## LoopTimer (`firmware/timing/loop_timer.h`)

Replaces `delay()` for periodic work. Uses `pw::chrono::SystemClock` — backend is FreeRTOS tick on ESP32, `std::chrono::steady_clock` on host.

```cpp
LoopTimer sensor_tick(
    pw::chrono::SystemClock::for_at_least(std::chrono::seconds(30)));

void loop() {
    net.Poll();

    if (sensor_tick.IsReady()) {
        writer.PublishAll();
        sensor_tick.Reset();
    }

    mqtt_client.loop();       // MQTT keep-alive ping
    esp_task_wdt_reset();     // hardware watchdog feed
}
```

**Why `delay(30000)` is wrong:** `delay()` calls `vTaskDelay()`, blocking the FreeRTOS task for 30 seconds. `PubSubClient::loop()` runs in your task — if it doesn't run every ~15 seconds, the broker drops the connection. The hardware watchdog will also reset the chip if `loop()` doesn't return within its timeout window.

---

## Test Doubles

### FakeSensor

```cpp
testing::FakeSensor temp("temperature", 0x76, /*value=*/23.5f);
temp.set_value(99.9f);                              // change reading mid-test
temp.set_init_status(pw::Status::Unavailable());    // simulate init failure

// Read() always returns SensorReading::Ok(value)
SensorReading r = temp.Read();
EXPECT_TRUE(r.valid);
EXPECT_FLOAT_EQ(r.value, 23.5f);
```

### RecordingSensor

```cpp
testing::RecordingSensor s("soil", 0x48, 42.0f);
writer.PublishAll();
EXPECT_EQ(s.read_call_count(), 1);
```

### FakeI2CBus

```cpp
testing::FakeI2CBus bus;

// Preset read data for a sensor at address 0x23
bus.SetReadData(0x23, {0x1A, 0x00});

// Inject a single error — next transaction fails, subsequent ones succeed
bus.InjectError(pw::Status::Unavailable());

// Inspect all transactions after the fact
EXPECT_EQ(bus.write_count(), 2);
EXPECT_EQ(bus.transactions()[0].data[0], 0x01u);
```

### FakePublisher

```cpp
testing::FakePublisher pub;
writer.PublishAll();
EXPECT_STREQ(pub.messages()[0].topic,   "leaflab/sensorboard/light");
EXPECT_STREQ(pub.messages()[0].payload, "142.50");

pub.set_connected(false);   // simulate broker disconnect
```

---

## Adding a New Sensor

1. Create `firmware/sensor/my_sensor.h` and `.cc` implementing `ISensor`:
   - Constructor takes `II2CBus& bus`, `uint8_t address`, `const char* name`, and a clock function (`uint32_t (*clock_fn)()`) if the sensor has a measurement delay.
   - `Init()` → send power-on / config commands via `bus_.Write(...)`.
   - `Read()` → non-blocking; return `SensorReading::Ok(value)` or `SensorReading::Invalid()`.
2. Add `cc_library` and `cc_test` targets to `firmware/sensor/BUILD.bazel`.
3. Write tests in `my_sensor_test.cc` using `FakeI2CBus` — no hardware needed.
4. Add a static instance to the board's `*_config.cc` and include it in `kSensors[]`.

---

## Open Items

| Item | Status | Notes |
|------|--------|-------|
| Secrets provisioning (Wi-Fi / MQTT credentials) | Not started | Python RPC script vs Bazel `action` target |
| `esp32_platform.cc` | Not started | Real `WiFi.h` + `PubSubClient` implementations of the extern hooks |
| DeviceIdentity (eFuse MAC) | Not started | Wrap in `IDeviceIdentity` facade; use as MQTT client ID |
| TCA9548A multiplexer | Not started | Implement `II2CBus` that wraps another bus and selects a channel; sensor impls unchanged |
