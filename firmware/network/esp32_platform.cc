// ESP32 platform hook implementations for NetworkManager.
//
// WiFiClient and PubSubClient are owned here and hidden from application code.
// Applications depend on //firmware/network:esp32_platform and get the 6
// platform hooks (WiFiIsConnected, WiFiConnect, MQTTConnect, MQTTIsConnected,
// MQTTPublish, MQTTLoop) for free — no PubSubClient.h or WiFi.h needed.
//
// TLS: when MQTTConnect is called with tls=true, the WiFiClientSecure transport
// is used. setInsecure() skips certificate verification — sufficient for an
// embedded device that has no CA cert store. Add CA cert support later if needed.

#include "firmware/network/esp32_platform.h"

#include <PubSubClient.h>
#include <WiFi.h>
#include <WiFiClientSecure.h>
#include "pw_log/log.h"

static WiFiClient        plain_client;
static WiFiClientSecure  tls_client;
static PubSubClient      mqtt_plain(plain_client);
static PubSubClient      mqtt_tls_client(tls_client);
static PubSubClient*     g_mqtt = &mqtt_plain;

static const char* g_ssid     = nullptr;
static const char* g_password = nullptr;

void WiFiInit(const char* ssid, const char* password) {
    g_ssid     = ssid;
    g_password = password;
    WiFi.mode(WIFI_STA);
    WiFi.setAutoReconnect(true);
    WiFi.begin(ssid, password);
    PW_LOG_INFO("WiFi: connecting to '%s'...", ssid);
}

bool WiFiIsConnected() {
    static bool s_was_connected = false;
    bool connected = WiFi.status() == WL_CONNECTED;
    if (connected && !s_was_connected) {
        PW_LOG_INFO("WiFi: connected — IP %s", WiFi.localIP().toString().c_str());
    } else if (!connected && s_was_connected) {
        PW_LOG_WARN("WiFi: lost connection");
    }
    s_was_connected = connected;
    return connected;
}

// Re-triggers association if not connected.  Called by NetworkManager when
// transitioning to kConnecting.  setAutoReconnect(true) handles most cases,
// but an explicit begin() speeds up recovery after a long disconnect.
void WiFiConnect() {
    if (WiFi.status() != WL_CONNECTED && g_ssid) {
        WiFi.begin(g_ssid, g_password);
    }
}

bool MQTTConnect(const char* host, uint16_t port, const char* id,
                 const char* user, const char* pass,
                 const char* lwt_topic, const char* lwt_payload,
                 bool tls) {
    if (tls) {
        tls_client.setInsecure();  // no CA cert store on device; skip verification
        g_mqtt = &mqtt_tls_client;
        PW_LOG_INFO("MQTT: using TLS (insecure — no cert verification)");
    } else {
        g_mqtt = &mqtt_plain;
    }
    g_mqtt->setServer(host, port);
    if (lwt_topic != nullptr && lwt_topic[0] != '\0') {
        return g_mqtt->connect(id, user, pass,
                               lwt_topic, /*qos=*/0, /*retain=*/true,
                               lwt_payload);
    }
    return g_mqtt->connect(id, user, pass);
}

bool MQTTIsConnected() { return g_mqtt->connected(); }

bool MQTTPublish(const char* topic, const char* payload) {
    return g_mqtt->publish(topic, payload);
}

bool MQTTPublishBinary(const char* topic, const uint8_t* data, size_t len,
                        bool retained) {
    return g_mqtt->publish(topic, data, static_cast<unsigned int>(len),
                           retained);
}

void MQTTLoop() { g_mqtt->loop(); }

uint32_t PlatformNowMs() { return millis(); }
