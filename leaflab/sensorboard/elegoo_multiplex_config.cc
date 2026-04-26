// Board config for the Elegoo ESP32 with a TCA9548A (HW-617) I2C multiplexer.
// Mux: address 0x70 (A0/A1/A2 tied low).
//
// Channel SD0: BH1750 ambient light at 0x23 ("light2").
// Channel SD5: SHT3x temperature + humidity at 0x44.
// Channel SD6: CCS811 eCO2 + TVOC at 0x5A.
//   CCS811 WAKE pin: tied to GND (always-on). WAKE is a power-saving feature
//   for battery devices; continuous operation on wall power is safe and intended.
// Channel SD7: BH1750 ambient light at 0x23 ("light").
//
// To add sensors: declare a TCA9548ABus for its channel, instantiate the
// sensor against it, and add the pointer to kSensors[].

#include <Arduino.h>

#include "firmware/credentials/nvs_credentials.h"
#include "firmware/device_id/efuse_device_id.h"
#include "firmware/i2c/arduino_i2c_bus.h"
#include "firmware/i2c/i2c_bus.h"
#include "firmware/i2c/tca9548a_bus.h"
#include "firmware/mqtt/firmware_publisher.h"
#include "firmware/network/esp32_platform.h"
#include "firmware/network/network_manager.h"
#include "firmware/sensor/bh1750.h"
#include "firmware/sensor/ccs811.h"
#include "firmware/sensor/sensor.h"
#include "firmware/sensor/sht3x.h"
#include "pw_span/span.h"

// ── I2C bus, multiplexer channels, and sensors ───────────────────────────────

static firmware::ArduinoI2CBus bus;
static firmware::TCA9548ABus   ch0(bus, 0x70, 0);  // HW-617 SD0
static firmware::TCA9548ABus   ch5(bus, 0x70, 5);  // HW-617 SD5
static firmware::TCA9548ABus   ch6(bus, 0x70, 6);  // HW-617 SD6
static firmware::TCA9548ABus   ch7(bus, 0x70, 7);  // HW-617 SD7

static firmware::BH1750Sensor     bh1750_2(ch0, 0x23, "max-light", millis);
static firmware::SHT3xDevice      sht3x_dev(ch5, 0x44, millis);
static firmware::SHT3xTemperature sht3x_temp(sht3x_dev, "board-temp");
static firmware::SHT3xHumidity    sht3x_humi(sht3x_dev, "board-humidity");
static firmware::CCS811Device     ccs811_dev(ch6, 0x5A, millis);
static firmware::CCS811eCO2       ccs811_eco2(ccs811_dev, "board-eco2");
static firmware::CCS811TVOC       ccs811_tvoc(ccs811_dev, "board-tvoc");
static firmware::BH1750Sensor     bh1750(ch7, 0x23, "board-light", millis);

static firmware::ISensor* const kSensors[] = {
    &bh1750_2,
    &sht3x_temp, &sht3x_humi,
    &ccs811_eco2, &ccs811_tvoc,
    &bh1750,
};

firmware::II2CBus& GetBus() { return bus; }

pw::span<firmware::ISensor* const> GetSensors() {
    return pw::span<firmware::ISensor* const>(kSensors);
}

// ── Network ──────────────────────────────────────────────────────────────────

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
