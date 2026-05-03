// Board config for the Elegoo ESP32 dev board — dynamic MQTT config.
//
// Identical hardware to elegoo_config.cc: one BH1750 at 0x23 on the root I2C
// bus. Adds ConfigStore (NVS persistence) and ConfigApplier (runtime name
// overrides) so sensor names and enabled state can be changed via MQTT.

#include <Arduino.h>

#include "firmware/config/config_applier.h"
#include "firmware/config/config_store.h"
#include "firmware/credentials/nvs_credentials.h"
#include "firmware/device_id/efuse_device_id.h"
#include "firmware/i2c/arduino_i2c_bus.h"
#include "firmware/i2c/i2c_bus.h"
#include "firmware/mqtt/firmware_publisher.h"
#include "firmware/network/esp32_platform.h"
#include "firmware/network/network_manager.h"
#include "firmware/sensor/bh1750.h"
#include "firmware/sensor/catalog/chip_catalog.h"
#include "firmware/sensor/sensor.h"
#include "pw_span/span.h"

using namespace firmware::chip_addr;

// ── I2C bus and sensors ──────────────────────────────────────────────────────

static firmware::ArduinoI2CBus bus;
static firmware::BH1750Sensor  bh1750(bus, kBH1750Default, "light", millis);

static firmware::ISensor* const kSensors[] = {&bh1750};

firmware::II2CBus& GetBus() { return bus; }

pw::span<firmware::ISensor* const> GetSensors() {
    return pw::span<firmware::ISensor* const>(kSensors);
}

// ── Config (runtime overrides) ────────────────────────────────────────────────

static firmware::ConfigStore   config_store;
static firmware::ConfigApplier config_applier(GetSensors());

firmware::ConfigStore&   GetConfigStore()   { return config_store; }
firmware::ConfigApplier& GetConfigApplier() { return config_applier; }

// ── Network ───────────────────────────────────────────────────────────────────

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

    static char lwt_topic[64];
    snprintf(lwt_topic, sizeof(lwt_topic), "leaflab/%s/status", device_id.Get());

    static firmware::NetworkManager::Config cfg;
    cfg.ssid        = creds.wifi_ssid();
    cfg.password    = creds.wifi_password();
    cfg.mqtt_host   = creds.mqtt_host();
    cfg.mqtt_port   = creds.mqtt_port();
    cfg.device_id   = device_id.Get();
    cfg.mqtt_user   = creds.mqtt_user()[0] != '\0' ? creds.mqtt_user() : nullptr;
    cfg.mqtt_pass   = creds.mqtt_pass()[0] != '\0' ? creds.mqtt_pass() : nullptr;
    cfg.lwt_topic   = lwt_topic;
    cfg.lwt_payload = "offline";
    cfg.mqtt_tls    = creds.mqtt_tls();

    static firmware::NetworkManager local_net(cfg);
    net = &local_net;
    return *net;
}

firmware::FirmwarePublisher& GetPublisher() {
    if (publisher != nullptr) return *publisher;
    static firmware::FirmwarePublisher local_pub(
        device_id, GetSensors(), GetNetwork(),
        GetConfigStore(), GetConfigApplier());
    publisher = &local_pub;
    return *publisher;
}
