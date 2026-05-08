// Board config for the Elegoo ESP32 dev board — fully dynamic sensor config.
//
// Hardware: ArduinoI2CBus on the native I2C pins, with optional TCA9548A mux.
// No sensors are compiled in. Push a DeviceConfig proto to
// leaflab/<device_id>/config to declare what chips are wired.
//
// Mux channels are allocated on demand — no compile-time topology needed.
//
// Examples:
//   Direct:  {chip_type: CHIP_TYPE_BH1750, i2c_address: 0x23, name: "light"}
//   Via mux: {chip_type: CHIP_TYPE_SHT3X, i2c_address: 0x44,
//             mux_path: [{mux_address: 0x70, mux_channel: 2}], name: "temp"}

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
#include "firmware/sensor/sensor.h"
#include "pw_span/span.h"

// ── I2C bus ───────────────────────────────────────────────────────────────────

static firmware::ArduinoI2CBus bus;

firmware::II2CBus& GetBus() { return bus; }

pw::span<firmware::ISensor* const> GetSensors() {
    return GetConfigApplier().sensors();
}

// ── Config (factory — all sensor instances created from DeviceConfig proto) ──

static firmware::ConfigApplier config_applier(&bus, millis);
static firmware::ConfigStore   config_store;

firmware::ConfigApplier& GetConfigApplier() { return config_applier; }
firmware::ConfigStore&   GetConfigStore()   { return config_store; }

// ── Network ───────────────────────────────────────────────────────────────────

static firmware::NVSCredentials     creds;
static firmware::EfuseDeviceId      device_id("leaflab");
static firmware::NetworkManager*    net       = nullptr;
static firmware::FirmwarePublisher* publisher = nullptr;

firmware::NetworkManager& GetNetwork() {
    if (net != nullptr) return *net;

    creds.Load();

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
        device_id, GetNetwork(), GetConfigApplier(), &GetConfigStore());
    publisher = &local_pub;
    return *publisher;
}
