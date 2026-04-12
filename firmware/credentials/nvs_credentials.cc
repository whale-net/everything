// NVSCredentials — reads WiFi credentials from ESP32 NVS via Preferences.h.

#include "firmware/credentials/nvs_credentials.h"

#include <Preferences.h>
#include <cstring>

#include "pw_log/log.h"
#include "pw_status/status.h"

namespace firmware {

pw::Status NVSCredentials::Load() {
    Preferences prefs;
    if (!prefs.begin("creds", /*readOnly=*/true)) {
        PW_LOG_ERROR("NVS: failed to open namespace 'creds'");
        PW_LOG_ERROR("Provision: bazel run //leaflab/sensorboard:provision -- PORT SSID PASS");
        return pw::Status::NotFound();
    }

    String ssid = prefs.getString("wifi_ssid", "");
    String pass = prefs.getString("wifi_pass", "");
    prefs.end();

    if (ssid.length() == 0) {
        PW_LOG_ERROR("NVS: 'wifi_ssid' key not found in namespace 'creds'");
        PW_LOG_ERROR("Provision: bazel run //leaflab/sensorboard:provision -- PORT SSID PASS");
        return pw::Status::NotFound();
    }

    strncpy(ssid_, ssid.c_str(), sizeof(ssid_) - 1);
    strncpy(pass_, pass.c_str(), sizeof(pass_) - 1);
    PW_LOG_INFO("NVS: loaded wifi_ssid='%s'", ssid_);
    return pw::OkStatus();
}

}  // namespace firmware
