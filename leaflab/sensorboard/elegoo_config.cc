// Board config for the Elegoo ESP32 dev board.
// Sensors: BH1750 ambient light at I2C address 0x23.
// Credentials: read from NVS (provision once with :provision target).
//
// To add sensors: declare more static instances and add pointers to kSensors[].
// To add a board: create a new *_config.cc and esp32_firmware() target.

#include <Arduino.h>

#include "firmware/credentials/nvs_credentials.h"
#include "firmware/device_id/efuse_device_id.h"
#include "firmware/i2c/arduino_i2c_bus.h"
#include "firmware/i2c/i2c_bus.h"
#include "firmware/mqtt/firmware_publisher.h"
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
//   1. Reads credentials from NVS (wifi_ssid, wifi_pass, mqtt_host, mqtt_port).
//   2. Initialises WiFi hardware (WiFiInit → WiFi.begin()).
//   3. Constructs and returns the NetworkManager.
//
// Provision with:
//   bazel run //leaflab/sensorboard:provision -- /dev/ttyUSB0 \
//     wifi_ssid=MySSID wifi_pass=MyPass mqtt_host=192.168.1.42

static firmware::NVSCredentials    creds;
static firmware::EfuseDeviceId     device_id("leaflab");
static firmware::NetworkManager*   net       = nullptr;
static firmware::FirmwarePublisher* publisher = nullptr;

firmware::NetworkManager& GetNetwork() {
    if (net != nullptr) return *net;

    pw::Status s = creds.Load();
    if (!s.ok()) {
        // Credentials not found — device will still start but WiFi won't connect.
    }

    WiFiInit(creds.wifi_ssid(), creds.wifi_password());

    // LWT topic includes device_id — build into a static buffer.
    static char lwt_topic[64];
    snprintf(lwt_topic, sizeof(lwt_topic), "leaflab/%s/status", device_id.Get());

    static firmware::NetworkManager::Config cfg;
    cfg.ssid        = creds.wifi_ssid();
    cfg.password    = creds.wifi_password();
    cfg.mqtt_host   = creds.mqtt_host();
    cfg.mqtt_port   = creds.mqtt_port();
    cfg.device_id   = device_id.Get();
    cfg.mqtt_user   = nullptr;
    cfg.mqtt_pass   = nullptr;
    cfg.lwt_topic   = lwt_topic;
    cfg.lwt_payload = "offline";

    static firmware::NetworkManager local_net(cfg);
    net = &local_net;
    return *net;
}

firmware::FirmwarePublisher& GetPublisher() {
    if (publisher != nullptr) return *publisher;
    static firmware::FirmwarePublisher local_pub(device_id, GetSensors(), GetNetwork());
    publisher = &local_pub;
    return *publisher;
}
