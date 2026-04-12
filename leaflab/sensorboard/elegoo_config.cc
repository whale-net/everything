// Board config for the Elegoo ESP32 dev board.
// Sensors: BH1750 ambient light at I2C address 0x23.
// Credentials: read from NVS (provision once with :provision target).
//
// To add sensors: declare more static instances and add pointers to kSensors[].
// To add a board: create a new *_config.cc and esp32_firmware() target.

#include <Arduino.h>

#include "firmware/credentials/nvs_credentials.h"
#include "firmware/i2c/arduino_i2c_bus.h"
#include "firmware/i2c/i2c_bus.h"
#include "firmware/network/esp32_platform.h"
#include "firmware/network/network_manager.h"
#include "firmware/sensor/bh1750.h"
#include "firmware/sensor/sensor.h"
#include "pw_span/span.h"

// ── I2C bus and sensors ──────────────────────────────────────────────────────

static firmware::ArduinoI2CBus bus;
static firmware::BH1750Sensor  bh1750(bus, 0x23, "light", millis);

static firmware::ISensor* const kSensors[] = {&bh1750};

firmware::II2CBus& GetBus() { return bus; }

pw::span<firmware::ISensor* const> GetSensors() {
    return pw::span<firmware::ISensor* const>(kSensors);
}

// ── Network ──────────────────────────────────────────────────────────────────
// GetNetwork() is called from setup() before Connect(). On first call it:
//   1. Reads WiFi credentials from NVS (via Preferences.h).
//   2. Initialises WiFi hardware (WiFiInit → WiFi.begin()).
//   3. Constructs and returns the NetworkManager.
//
// MQTT broker: change mqtt_host to your broker's hostname or IP.
//              Set mqtt_user/mqtt_pass if your broker requires auth.

static firmware::NVSCredentials creds;
static firmware::NetworkManager* net = nullptr;

firmware::NetworkManager& GetNetwork() {
    if (net != nullptr) return *net;

    pw::Status s = creds.Load();
    if (!s.ok()) {
        // Credentials not found — device will still start but WiFi won't connect.
        // Run: bazel run //leaflab/sensorboard:provision -- /dev/ttyUSB0 SSID PASS
    }

    WiFiInit(creds.wifi_ssid(), creds.wifi_password());

    static const firmware::NetworkManager::Config cfg = {
        .ssid      = creds.wifi_ssid(),
        .password  = creds.wifi_password(),
        .mqtt_host = "mqtt.local",  // ← change to your MQTT broker
        .mqtt_port = 1883,
        .device_id = "leaflab-sensorboard",
        .mqtt_user = nullptr,       // set if your broker requires auth
        .mqtt_pass = nullptr,
    };
    static firmware::NetworkManager local_net(cfg);
    net = &local_net;
    return *net;
}
