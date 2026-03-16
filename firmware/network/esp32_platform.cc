// ESP32 platform hook implementations for NetworkManager.
//
// WiFiClient and PubSubClient are owned here and hidden from application code.
// Applications depend on //firmware/network:esp32_platform and get the 6
// platform hooks (WiFiIsConnected, WiFiConnect, MQTTConnect, MQTTIsConnected,
// MQTTPublish, MQTTLoop) for free — no PubSubClient.h or WiFi.h needed.

#include <PubSubClient.h>
#include <WiFi.h>

static WiFiClient   wifi_client;
static PubSubClient mqtt_client(wifi_client);

bool WiFiIsConnected() { return WiFi.status() == WL_CONNECTED; }

// No-op: setAutoReconnect(true) handles re-association automatically.
void WiFiConnect() {}

bool MQTTConnect(const char* host, uint16_t port, const char* id,
                 const char* user, const char* pass) {
    mqtt_client.setServer(host, port);
    return mqtt_client.connect(id, user, pass);
}

bool MQTTIsConnected() { return mqtt_client.connected(); }

bool MQTTPublish(const char* topic, const char* payload) {
    return mqtt_client.publish(topic, payload);
}

// Drive the PubSubClient keep-alive.  Must be called every loop() pass.
void MQTTLoop() { mqtt_client.loop(); }
