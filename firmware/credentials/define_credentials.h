#pragma once

// DefineCredentials — WiFi credentials baked in at compile time via Bazel
// --define flags.
//
// Usage — add to .bazelrc.local (gitignored, never committed):
//   build --define=WIFI_SSID=MyNetwork
//   build --define=WIFI_PASSWORD=MyPassword
//
// The define_credentials BUILD target translates these into preprocessor
// macros via copts.  If either define is missing, WIFI_SSID / WIFI_PASSWORD
// default to empty string and Load() returns FailedPrecondition().
//
// When to use:
//   - Boards without NVS flash support
//   - Quick local testing when you don't want a separate provision step
//
// When NOT to use:
//   - Any board with NVS (e.g. ESP32) — credentials end up in the binary
//   - Shared or production boards — use NVSCredentials instead

#include "firmware/credentials/credentials.h"
#include "pw_log/log.h"
#include "pw_status/status.h"

// Defaults to empty string when --define is not passed (caught by Load()).
#ifndef WIFI_SSID
#define WIFI_SSID ""
#endif
#ifndef WIFI_PASSWORD
#define WIFI_PASSWORD ""
#endif
#ifndef MQTT_HOST
#define MQTT_HOST ""
#endif
#ifndef MQTT_PORT
#define MQTT_PORT 1883
#endif

namespace firmware {

class DefineCredentials final : public ICredentials {
 public:
  pw::Status Load() override {
      if (wifi_ssid()[0] == '\0') {
          PW_LOG_ERROR(
              "WIFI_SSID is empty. Add to .bazelrc.local:\n"
              "  build --define=WIFI_SSID=YourSSID\n"
              "  build --define=WIFI_PASSWORD=YourPass");
          return pw::Status::FailedPrecondition();
      }
      return pw::OkStatus();
  }

  const char* wifi_ssid()     const override { return WIFI_SSID; }
  const char* wifi_password() const override { return WIFI_PASSWORD; }
  const char* mqtt_host()     const override { return MQTT_HOST; }
  uint16_t    mqtt_port()     const override { return MQTT_PORT; }
  const char* mqtt_user()     const override { return ""; }
  const char* mqtt_pass()     const override { return ""; }
  bool        mqtt_tls()      const override { return false; }
};

}  // namespace firmware
