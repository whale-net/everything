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
// Platform hooks (WiFiIsConnected, MQTTConnect, MQTTIsConnected, MQTTPublish)
// are implemented below using the ESP32 WiFi and PubSubClient libraries.
// Edit kNetConfig to match your environment before flashing.

#include <Arduino.h>
#include <PubSubClient.h>
#include <WiFi.h>

#include "board_pins.h"
#include "firmware/mqtt/mqtt_writer.h"
#include "firmware/network/network_manager.h"
#include "firmware/network/network_publisher.h"
#include "firmware/sensor/thermistor.h"
#include "pw_log/log.h"

// ── Configuration — edit before flashing ─────────────────────────────────────

namespace {

constexpr firmware::NetworkManager::Config kNetConfig = {
    .ssid = "your_wifi_ssid",
    .password = "your_wifi_password",
    .mqtt_host = "192.168.1.100",
    .mqtt_port = 1883,
    .device_id = "esp32_network_send",
    .mqtt_user = nullptr,
    .mqtt_pass = nullptr,
};

// ── Platform hook implementations ─────────────────────────────────────────────
// These satisfy the extern declarations in network_manager.cc.

WiFiClient wifi_client;
PubSubClient mqtt_client(wifi_client);

}  // namespace

bool WiFiIsConnected() {
    return WiFi.status() == WL_CONNECTED;
}

bool MQTTConnect(const char* host, uint16_t port, const char* client_id,
                 const char* user, const char* pass) {
    mqtt_client.setServer(host, port);
    return mqtt_client.connect(client_id, user, pass);
}

bool MQTTIsConnected() {
    return mqtt_client.connected();
}

bool MQTTPublish(const char* topic, const char* payload) {
    return mqtt_client.publish(topic, payload);
}

// ── Application objects ───────────────────────────────────────────────────────

namespace {

firmware::NetworkManager   net(kNetConfig);
firmware::NetworkPublisher publisher(net);
firmware::ThermistorSensor thermistor(board::kAdc0);

firmware::ISensor* const kSensors[] = {&thermistor};
firmware::MQTTWriter writer(kSensors, "home/sensors", &publisher);

}  // namespace

// ── Sketch ────────────────────────────────────────────────────────────────────

// Platform hook: called by NetworkManager when entering kConnecting.
// ESP32 handles re-association internally when setAutoReconnect(true);
// re-calling WiFi.begin() here is a no-op in that case.
void WiFiConnect() {
    WiFi.begin(kNetConfig.ssid, kNetConfig.password);
}

void setup() {
    Serial.begin(115200);
    // Enable persistent auto-reconnect so the ESP32 re-associates after a
    // link drop without waiting for the state machine to call WiFiConnect().
    WiFi.setAutoReconnect(true);
    WiFi.persistent(true);
    WiFi.begin(kNetConfig.ssid, kNetConfig.password);

    int init_ok = writer.InitAll();
    PW_LOG_INFO("network_send: %d/%d sensors initialised",
                init_ok, static_cast<int>(sizeof(kSensors) / sizeof(kSensors[0])));

    net.Connect();
    PW_LOG_INFO("network_send: connecting to %s / %s",
                kNetConfig.ssid, kNetConfig.mqtt_host);
}

void loop() {
    net.Poll();

    if (net.state() == firmware::NetworkManager::State::kReady) {
        mqtt_client.loop();  // keep-alive, non-blocking
        int published = writer.PublishAll();
        PW_LOG_DEBUG("network_send: published %d sensor(s)", published);
    }

    delay(1000);
}
