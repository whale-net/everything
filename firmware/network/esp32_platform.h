#pragma once

#include <cstddef>
#include <cstdint>

// ESP32 platform functions for NetworkManager.
// Include this from board config files that initialize WiFi on ESP32.
// The implementations live in esp32_platform.cc and link WiFi.h + PubSubClient.

// Initialize WiFi: sets STA mode, configures auto-reconnect, and begins
// association.  Call once from setup() before NetworkManager::Connect().
// The SSID and password are stored internally and reused by WiFiConnect()
// on reconnection attempts.
void WiFiInit(const char* ssid, const char* password);

// Drive the PubSubClient keep-alive.  Call every loop() pass when the
// NetworkManager is kReady, or unconditionally — it is a no-op when
// disconnected.
void MQTTLoop();

// Subscribe to an MQTT topic. Returns true on success.
// Call after MQTTConnect succeeds (i.e. from within OnConnect handling).
bool MQTTSubscribe(const char* topic);

// Register a callback for incoming MQTT messages.
// PubSubClient's callback signature differs from ours, so an internal adapter
// bridges the types. Call before MQTTConnect.
void MQTTSetCallback(void (*cb)(const char*, const uint8_t*, size_t));
