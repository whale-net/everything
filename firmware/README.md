# Firmware Application Layer

Board-agnostic C++ libraries for embedded sensor applications.
All libraries compile and test on the host machine — no hardware required.

For the build infrastructure (toolchains, board platforms, flashing) see [`tools/firmware/README.md`](../tools/firmware/README.md).

---

## Directory Layout

```
firmware/
  sensor/
    sensor.h            ISensor — virtual interface for any sensor
    mock_sensor.h       FakeSensor, RecordingSensor (host-side test doubles)
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
bazel test //firmware/mqtt:mqtt_writer_test
bazel test //firmware/network:network_manager_test
bazel test //firmware/timing:loop_timer_test
```

---

## ISensor Interface (`firmware/sensor/sensor.h`)

The central abstraction. Every physical sensor implements this interface.

```cpp
class ISensor {
 public:
  virtual pw::Status Init()          = 0;  // called once from setup()
  virtual float      Read()          = 0;  // non-blocking; never delay()
  virtual const char* name() const   = 0;  // MQTT sub-topic
  virtual uint8_t    address() const = 0;  // I2C address / channel
};
```

### Compile-time dependency injection

The board-specific `main.cpp` instantiates the real sensor types and passes them to `MQTTWriter`:

```cpp
// firmware/esp32_greenhouse/main.cpp
BME280Sensor temp(0x76);
SoilSensor   soil(0x48);
ISensor* sensors[] = { &temp, &soil };

RealPublisher publisher(&network_manager);
MQTTWriter writer(sensors, "home/greenhouse", &publisher);
```

No heap allocation.  No `new`.  The array is on the stack; the writer holds a `pw::span`.

---

## MQTTWriter (`firmware/mqtt/mqtt_writer.h`)

Iterates the sensor array, reads each sensor, formats the value with `pw::StringBuffer` (zero heap allocation), and calls `IPublisher::Publish()`.

```
topic:   "<prefix>/<sensor.name()>"   e.g.  "home/greenhouse/temperature"
payload: "%.2f"                        e.g.  "23.50"
```

Sensors that failed `Init()` are silently skipped on every `PublishAll()` call.

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

Platform hooks (`WiFiIsConnected`, `MQTTConnect`, `MQTTPublish`) are declared `extern` and provided by:
- `firmware/network/esp32_platform.cc` (on-device, wraps `WiFi.h` + `PubSubClient`)
- Inline stubs in `network_manager_test.cc` (host tests)

---

## LoopTimer (`firmware/timing/loop_timer.h`)

Replaces `delay()` for periodic work. Uses `pw::chrono::SystemClock` — the backend is automatically selected (FreeRTOS tick on ESP32, `std::chrono::steady_clock` on host).

```cpp
LoopTimer sensor_tick(
    pw::chrono::SystemClock::for_at_least(std::chrono::seconds(30)));

void loop() {
    net.Poll();

    if (sensor_tick.IsReady()) {
        writer.PublishAll();
        sensor_tick.Reset();
    }

    // These must run every loop() pass — never block them with delay():
    mqtt_client.loop();       // MQTT keep-alive ping
    esp_task_wdt_reset();     // hardware watchdog feed
}
```

**Why `delay(30000)` is wrong on ESP32:**
`delay()` calls `vTaskDelay()`, blocking the current FreeRTOS task for 30 seconds.  `PubSubClient::loop()` runs in *your* task — not the Wi-Fi driver task.  If it doesn't run every ~15 seconds, the broker drops the connection.  The hardware watchdog will also reset the chip if `loop()` doesn't return within its timeout window.

---

## Test Doubles

### FakeSensor

```cpp
testing::FakeSensor temp("temperature", 0x76, /*value=*/23.5f);
temp.set_value(99.9f);          // change reading mid-test
temp.set_init_status(pw::Status::Unavailable());  // simulate init failure
```

### RecordingSensor

```cpp
testing::RecordingSensor s("soil", 0x48, 42.0f);
writer.PublishAll();
EXPECT_EQ(s.read_call_count(), 1);
```

### FakePublisher

```cpp
testing::FakePublisher pub;
writer.PublishAll();
EXPECT_STREQ(pub.messages()[0].topic,   "home/greenhouse/temperature");
EXPECT_STREQ(pub.messages()[0].payload, "23.50");

pub.set_connected(false);   // simulate broker disconnect
```

---

## Open Items

| Item | Status | Notes |
|------|--------|-------|
| Secrets provisioning (Wi-Fi / MQTT credentials) | Not started | Python RPC script vs Bazel `action` target |
| Payload serialization | Done (pw::StringBuffer) | Migrate to `pw_protobuf` if binary size or parsing becomes an issue |
| WDT integration | Scaffolded | `esp_task_wdt_reset()` call site shown above; actual init not wired |
| `esp32_platform.cc` | Not started | Real `WiFi.h` + `PubSubClient` implementations of the extern hooks |
| DeviceIdentity (eFuse MAC) | Not started | Wrap in `IDeviceIdentity` facade; use as MQTT client ID |
