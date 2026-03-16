// network_send demo — reads a thermistor and publishes over MQTT.
//
// Architecture:
//
//   ThermistorSensor (board::kAdc0)
//          ↓
//   MQTTWriter  ───  NetworkPublisher  ───  NetworkManager
//                                                ↓
//                                       WiFi + PubSubClient
//
// Published topic:  "home/sensors/thermistor"
// Published value:  temperature in °C, one decimal place ("22.5")
//
// Build:  bazel build //demo/network_send:network_send_bin --config=esp32
// Flash:  bazel run  //demo/network_send:flash -- /dev/ttyUSB0
//
// Platform hooks (WiFiIsConnected, WiFiConnect, MQTTConnect, MQTTIsConnected,
// MQTTPublish, MQTTLoop) are provided by //firmware/network:esp32_platform.
// Edit kNetConfig to match your environment before flashing.

#include <Arduino.h>
#include <chrono>
#include <cstdio>

#include <esp_efuse.h>
#include <esp_task_wdt.h>

#include "board_pins.h"
#include "firmware/adc/arduino_adc.h"
#include "firmware/mqtt/mqtt_writer.h"
#include "firmware/network/network_manager.h"
#include "firmware/network/network_publisher.h"
#include "firmware/sensor/thermistor.h"
#include "firmware/timing/loop_timer.h"
#include "pw_log/log.h"

// MQTTLoop() is implemented in esp32_platform.cc; declared here so loop() can
// call it without pulling in PubSubClient.h.
extern void MQTTLoop();

// ── Configuration — edit before flashing ─────────────────────────────────────

// device_id is filled dynamically in setup() from the eFuse MAC address.
// Everything else is a compile-time constant.
static firmware::NetworkManager::Config net_config = {
    .ssid       = "your_wifi_ssid",
    .password   = "your_wifi_password",
    .mqtt_host  = "192.168.1.100",
    .mqtt_port  = 1883,
    .device_id  = nullptr,  // filled in setup()
    .mqtt_user  = nullptr,
    .mqtt_pass  = nullptr,
};

// Buffer for the MAC-derived device ID ("esp32_aabbccdd").  Static lifetime so
// net_config.device_id stays valid for the duration of the program.
static char g_device_id[32];

// ── Application objects ───────────────────────────────────────────────────────

namespace {

firmware::NetworkManager   net(net_config);
firmware::NetworkPublisher publisher(net);
firmware::ArduinoAdc       adc;
firmware::ThermistorSensor thermistor(board::kAdc0, &adc);

firmware::ISensor* const kSensors[] = {&thermistor};
firmware::MQTTWriter writer(kSensors, "home/sensors", &publisher);

// Fires once per second; drives sensor reads and MQTT publishes.
firmware::LoopTimer sensor_tick(
    pw::chrono::SystemClock::for_at_least(std::chrono::seconds(1)));

}  // namespace

// ── Sketch ────────────────────────────────────────────────────────────────────

void setup() {
    Serial.begin(115200);
    // Enable persistent auto-reconnect so the ESP32 re-associates after a
    // link drop without waiting for the state machine to call WiFiConnect().
    WiFi.setAutoReconnect(true);
    WiFi.persistent(true);
    WiFi.begin(net_config.ssid, net_config.password);

    // Unique device ID derived from the last 4 bytes of the eFuse MAC address.
    uint8_t mac[6];
    esp_efuse_mac_get_default(mac);
    snprintf(g_device_id, sizeof(g_device_id),
             "esp32_%02x%02x%02x%02x", mac[2], mac[3], mac[4], mac[5]);
    net.set_device_id(g_device_id);

    int init_ok = writer.InitAll();
    PW_LOG_INFO("network_send: %d/%d sensors initialised",
                init_ok, static_cast<int>(sizeof(kSensors) / sizeof(kSensors[0])));

    net.Connect();
    PW_LOG_INFO("network_send: connecting to %s / %s (id: %s)",
                net_config.ssid, net_config.mqtt_host, g_device_id);
}

void loop() {
    net.Poll();
    MQTTLoop();            // MQTT keep-alive — must run every pass
    esp_task_wdt_reset();  // hardware watchdog feed — must run every pass

    if (net.state() == firmware::NetworkManager::State::kReady &&
        sensor_tick.IsReady()) {
        int published = writer.PublishAll();
        PW_LOG_DEBUG("network_send: published %d sensor(s)", published);
        sensor_tick.Reset();
    }
}
