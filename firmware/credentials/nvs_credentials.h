#pragma once

// NVSCredentials — reads WiFi credentials from ESP32 Non-Volatile Storage.
//
// NVS layout written by the provision script:
//   namespace: "creds"
//   key "wifi_ssid"  — network name
//   key "wifi_pass"  — network passphrase
//
// Provision the device first (one-time per board or per credentials change):
//   bazel run //leaflab/sensorboard:provision -- /dev/ttyUSB0 "SSID" "Password"
//
// Credentials survive firmware reflashes because they live in a separate
// NVS partition at 0x9000 — independent of the application binary at 0x10000.

#include "firmware/credentials/credentials.h"
#include "pw_status/status.h"

namespace firmware {

class NVSCredentials final : public ICredentials {
 public:
  // Reads "wifi_ssid" and "wifi_pass" from NVS namespace "creds".
  // Logs a clear error and returns NotFound() if the keys are missing —
  // which means the device has not been provisioned yet.
  pw::Status Load() override;

  const char* wifi_ssid()     const override { return ssid_; }
  const char* wifi_password() const override { return pass_; }

 private:
  char ssid_[64] = {};
  char pass_[64] = {};
};

}  // namespace firmware
