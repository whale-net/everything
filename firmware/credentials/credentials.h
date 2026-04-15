#pragma once

#include "pw_status/status.h"

namespace firmware {

// Board-agnostic credential store for Wi-Fi and MQTT configuration.
//
// Implementations:
//   NVSCredentials    — reads from ESP32 NVS (provisioned via provision.py)
//   DefineCredentials — reads from compile-time --define flags (.bazelrc.local)
//   FakeCredentials   — fixed values for host-side tests
//
// Call Load() once from setup() before accessing any credential accessors.
// mqtt_host() returns "" if not provisioned; caller should skip MQTT in that case.
class ICredentials {
 public:
  virtual ~ICredentials() = default;

  // Load credentials from their backing store.
  // Returns not-ok if credentials are missing or the backing store fails.
  virtual pw::Status Load() = 0;

  virtual const char* wifi_ssid()     const = 0;
  virtual const char* wifi_password() const = 0;
  virtual const char* mqtt_host()     const = 0;
  virtual uint16_t    mqtt_port()     const = 0;
};

// ── Test double ──────────────────────────────────────────────────────────────

namespace testing {

class FakeCredentials final : public ICredentials {
 public:
  FakeCredentials(const char* ssid, const char* password,
                  const char* mqtt_host = "", uint16_t mqtt_port = 1883)
      : ssid_(ssid), password_(password),
        mqtt_host_(mqtt_host), mqtt_port_(mqtt_port) {}

  pw::Status Load() override { return pw::OkStatus(); }
  const char* wifi_ssid()     const override { return ssid_; }
  const char* wifi_password() const override { return password_; }
  const char* mqtt_host()     const override { return mqtt_host_; }
  uint16_t    mqtt_port()     const override { return mqtt_port_; }

 private:
  const char* ssid_;
  const char* password_;
  const char* mqtt_host_;
  uint16_t    mqtt_port_;
};

}  // namespace testing
}  // namespace firmware
