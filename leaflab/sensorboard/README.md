# Sensorboard

ESP32 firmware that reads I2C sensors and logs readings via `pw_log`.
Currently reads a BH1750 ambient light sensor (lux) at I2C address `0x23`.

---

## Hardware

**Board:** Elegoo ESP32 (CP2102 USB-UART)

**BH1750 wiring:**

| BH1750 pin | ESP32 pin | Notes |
|------------|-----------|-------|
| VCC | 3.3V | — |
| GND | GND | — |
| SDA | D21 (GPIO 21) | Pull-up resistor required (4.7 kΩ to 3.3V) |
| SCL | D22 (GPIO 22) | Pull-up resistor required (4.7 kΩ to 3.3V) |
| ADDR | GND | Selects I2C address 0x23 (VCC → 0x5C) |

**Pin layout note:** On the ESP32 DevKit right-side header, the order is D23 → D22 → TX0 → RX0 → D21. TX0 is GPIO 1, not GPIO 21 — SDA goes to D21, which is two pins below D22 (past RX0).

If you're unsure about wiring, flash the I2C scanner first:
```bash
bazel run //tools/firmware/esp32/i2c_scanner:flash -- /dev/ttyUSB0
screen /dev/ttyUSB0 115200
# Expected: INF  Device found at 0x23
```

---

## WiFi and MQTT Setup

Credentials are stored in the ESP32's NVS partition, separate from the firmware binary. This means they survive firmware reflashes and are never baked into the `.bin`.

### Provision credentials (once per device)

```bash
bazel run //leaflab/sensorboard:provision -- /dev/ttyUSB0 \
  wifi_ssid=MySSID wifi_pass=MyPassword mqtt_host=192.168.1.42 mqtt_port=1883
```

Run this once. Re-run if credentials change. `mqtt_host` and `mqtt_port` are optional — omitting `mqtt_host` enables WiFi-only mode (no MQTT publishing).

### Alternative: compile-time credentials (boards without NVS)

Add to `.bazelrc.local` (gitignored — never commit credentials):

```
build --define=WIFI_SSID=MyNetwork
build --define=WIFI_PASSWORD=MyPassword
```

Then swap `NVSCredentials` for `DefineCredentials` in `elegoo_config.cc` and add `//firmware/credentials:define_credentials` to the `BUILD.bazel` deps.

---

## Build and Flash

```bash
# Build the flashable .bin
bazel build //leaflab/sensorboard:sensorboard_bin --config=esp32

# Flash to the board
bazel run //leaflab/sensorboard:flash -- /dev/ttyUSB0

# Monitor serial output (Ctrl-A K to exit)
screen /dev/ttyUSB0 115200
```

Expected output:
```
INF  Sensor ready: light @ 0x23
INF  light: 142.5
INF  light: 141.8
```

**WSL2:** If `/dev/ttyUSB0` doesn't appear, see the [USB passthrough setup](../../tools/firmware/README.md#wsl2-usb-setup-one-time) in `tools/firmware/README.md`.

**WSL2 flash failures:** If flashing fails with `device disconnected or multiple access on port?`, a previous esptool process may still be holding the port:
```bash
lsof /dev/ttyUSB0   # find the PID
kill <pid>
```

---

## Code Structure

```
leaflab/sensorboard/
  sensorboard_main.cc   Generic setup()/loop() — never changes
  elegoo_config.cc      Elegoo board: ArduinoI2CBus + BH1750 at 0x23
  BUILD.bazel
```

The main loop and board config are intentionally separated. `sensorboard_main.cc` calls two functions that the linked config file provides:

```cpp
firmware::II2CBus& GetBus();
pw::span<firmware::ISensor* const> GetSensors();
```

This means adding a new board or sensor configuration never touches the main loop.

---

## Adding a Sensor to the Elegoo Config

**1. Implement the sensor** in `firmware/sensor/` (or use an existing one):

```cpp
// firmware/sensor/my_sensor.h
class MySensor final : public firmware::ISensor {
 public:
  MySensor(firmware::II2CBus& bus, uint8_t address, const char* name);
  pw::Status Init() override;
  firmware::SensorReading Read() override;
  const char* name()    const override;
  uint8_t address()     const override;
  firmware_SensorType type() const override;
  const char* unit()    const override;
};
```

See [`firmware/README.md`](../../firmware/README.md#adding-a-new-sensor) for the full pattern.

**2. Add a static instance to `elegoo_config.cc`:**

```cpp
#include "firmware/sensor/my_sensor.h"

static firmware::ArduinoI2CBus bus;
static firmware::BH1750Sensor  bh1750(bus, 0x23, "light",  millis);
static firmware::MySensor      my_sensor(bus, 0x48, "my_reading", millis);

static firmware::ISensor* const kSensors[] = {&bh1750, &my_sensor};
```

**3. Add the dep to `BUILD.bazel`:**

```python
esp32_firmware(
    name = "sensorboard",
    srcs = ["elegoo_config.cc"],
    deps = [
        ":sensorboard_main",
        "//firmware/i2c:arduino_i2c_bus",
        "//firmware/sensor:bh1750",
        "//firmware/sensor:my_sensor",   # ← add this
        "@arduino_esp32//:Wire",
    ],
)
```

The sensor will appear in the log on the next flash.

---

## Adding a New Board Config

Create a new config file and a new `esp32_firmware()` target. `sensorboard_main.cc` is unchanged.

**1. Create `my_board_config.cc`:**

```cpp
#include <Arduino.h>
#include "firmware/i2c/arduino_i2c_bus.h"
#include "firmware/sensor/bh1750.h"
#include "pw_span/span.h"

static firmware::ArduinoI2CBus bus;
static firmware::BH1750Sensor  bh1750(bus, 0x23, "light", millis);
static firmware::ISensor* const kSensors[] = {&bh1750};

firmware::II2CBus& GetBus() { return bus; }
pw::span<firmware::ISensor* const> GetSensors() {
    return pw::span<firmware::ISensor* const>(kSensors);
}
```

**2. Add a target to `BUILD.bazel`:**

```python
esp32_firmware(
    name = "sensorboard_my_board",
    srcs = ["my_board_config.cc"],
    deps = [
        ":sensorboard_main",
        "//firmware/i2c:arduino_i2c_bus",
        "//firmware/sensor:bh1750",
        "@arduino_esp32//:Wire",
    ],
)
```

**3. Flash:**

```bash
bazel run //leaflab/sensorboard:flash_my_board -- /dev/ttyUSB0
```

---

## Running Unit Tests

The sensor implementations are tested on the host — no hardware needed:

```bash
bazel test //firmware/sensor:bh1750_test
bazel test //firmware/sensor:thermistor_sensor_test
bazel test //firmware/...
```

These tests use `FakeI2CBus` to verify I2C transaction sequences and lux conversion without an actual sensor. See [`firmware/README.md`](../../firmware/README.md) for the test double API.
