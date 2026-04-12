#pragma once

#include "pw_status/status.h"

namespace firmware {

// Board-agnostic credential store for Wi-Fi configuration.
//
// Implementations:
//   NVSCredentials    — reads from ESP32 NVS (provisioned via provision.py)
//   DefineCredentials — reads from compile-time --define flags (.bazelrc.local)
//   FakeCredentials   — fixed values for host-side tests
//
// Call Load() once from setup() before accessing any credential accessors.
class ICredentials {
 public:
  virtual ~ICredentials() = default;

  // Load credentials from their backing store.
  // Returns not-ok if credentials are missing or the backing store fails.
  virtual pw::Status Load() = 0;

  virtual const char* wifi_ssid()     const = 0;
  virtual const char* wifi_password() const = 0;
};

// ── Test double ──────────────────────────────────────────────────────────────

namespace testing {

class FakeCredentials final : public ICredentials {
 public:
  FakeCredentials(const char* ssid, const char* password)
      : ssid_(ssid), password_(password) {}

  pw::Status Load() override { return pw::OkStatus(); }
  const char* wifi_ssid()     const override { return ssid_; }
  const char* wifi_password() const override { return password_; }

 private:
  const char* ssid_;
  const char* password_;
};

}  // namespace testing
}  // namespace firmware
