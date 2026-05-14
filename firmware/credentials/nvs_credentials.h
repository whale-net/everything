#pragma once

#include <cstdint>

// NVSCredentials — reads credentials from ESP32 NVS via Preferences.h.
//
// NVS layout written by the provision script (namespace "creds"):
//   key "wifi_ssid"  — WiFi network name         (required)
//   key "wifi_pass"  — WiFi passphrase            (required)
//   key "mqtt_host"  — MQTT broker IP or hostname (optional; "" if absent)
//   key "mqtt_port"  — MQTT broker port as string (optional; 1883 if absent)
//   key "mqtt_user"  — MQTT username              (optional; "" if absent)
//   key "mqtt_pass"  — MQTT password              (optional; "" if absent)
//   key "mqtt_tls"   — "1" to enable TLS          (optional; false if absent)
//
// Provision the device:
//   bazel run //leaflab/sensorboard:provision -- /dev/ttyUSB0 \
//     wifi_ssid=MySSID wifi_pass=MyPass mqtt_host=192.168.1.42
//
// Credentials survive firmware reflashes because they live in a separate
// NVS partition at 0x9000 — independent of the application binary at 0x10000.

#include "firmware/credentials/credentials.h"
#include "pw_status/status.h"

namespace firmware {

class NVSCredentials final : public ICredentials {
 public:
  // Reads credentials from NVS namespace "creds".
  // Returns NotFound() if wifi_ssid is missing (device not provisioned).
  // mqtt_host / mqtt_port are optional — absent keys leave defaults.
  pw::Status Load() override;

  const char* wifi_ssid()     const override { return ssid_; }
  const char* wifi_password() const override { return pass_; }
  const char* mqtt_host()     const override { return mqtt_host_; }
  uint16_t    mqtt_port()     const override { return mqtt_port_; }
  const char* mqtt_user()     const override { return mqtt_user_; }
  const char* mqtt_pass()     const override { return mqtt_pass_; }
  bool        mqtt_tls()      const override { return mqtt_tls_; }

 private:
  char     ssid_[64]      = {};
  char     pass_[64]      = {};
  char     mqtt_host_[64] = {};
  uint16_t mqtt_port_     = 1883;
  char     mqtt_user_[64] = {};
  char     mqtt_pass_[64] = {};
  bool     mqtt_tls_      = false;
};

}  // namespace firmware
