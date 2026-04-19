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
        PW_LOG_ERROR("Provision: bazel run //leaflab/sensorboard:provision -- PORT wifi_ssid=... wifi_pass=... mqtt_host=...");
        return pw::Status::NotFound();
    }

    String ssid      = prefs.getString("wifi_ssid", "");
    String pass      = prefs.getString("wifi_pass", "");
    String mqtt_host = prefs.getString("mqtt_host", "");
    String mqtt_port = prefs.getString("mqtt_port", "");
    prefs.end();

    if (ssid.length() == 0) {
        PW_LOG_ERROR("NVS: 'wifi_ssid' key not found in namespace 'creds'");
        PW_LOG_ERROR("Provision: bazel run //leaflab/sensorboard:provision -- PORT wifi_ssid=SSID wifi_pass=PASS");
        return pw::Status::NotFound();
    }

    strncpy(ssid_,      ssid.c_str(),      sizeof(ssid_) - 1);
    strncpy(pass_,      pass.c_str(),      sizeof(pass_) - 1);
    strncpy(mqtt_host_, mqtt_host.c_str(), sizeof(mqtt_host_) - 1);
    if (mqtt_port.length() > 0) {
        mqtt_port_ = static_cast<uint16_t>(mqtt_port.toInt());
    }

    PW_LOG_INFO("NVS: loaded wifi_ssid='%s'", ssid_);
    if (mqtt_host_[0] != '\0') {
        PW_LOG_INFO("NVS: loaded mqtt_host='%s' port=%u", mqtt_host_, mqtt_port_);
    }
    return pw::OkStatus();
}

}  // namespace firmware
