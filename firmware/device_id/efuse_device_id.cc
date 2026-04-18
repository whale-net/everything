// EfuseDeviceId — ESP32 eFuse base MAC as a unique device identifier.

#include "firmware/device_id/efuse_device_id.h"

#include <cstdio>
#include <cstring>

#include <esp_efuse.h>
#include <esp_mac.h>

#include "pw_log/log.h"

namespace firmware {

const char* EfuseDeviceId::Get() const {
    if (loaded_) return id_;

    uint8_t mac[6] = {};
    // esp_efuse_mac_get_default reads the factory-burned base MAC from eFuse
    // BLOCK0. Falls back to esp_read_mac(ESP_MAC_WIFI_STA) if eFuse read
    // fails (shouldn't happen on a real chip).
    esp_err_t err = esp_efuse_mac_get_default(mac);
    if (err != ESP_OK) {
        PW_LOG_WARN("EfuseDeviceId: eFuse read failed (err=%d), falling back to WiFi STA MAC", err);
        esp_read_mac(mac, ESP_MAC_WIFI_STA);
    }

    if (prefix_ != nullptr && prefix_[0] != '\0') {
        snprintf(id_, sizeof(id_), "%s-%02x%02x%02x%02x%02x%02x",
                 prefix_, mac[0], mac[1], mac[2], mac[3], mac[4], mac[5]);
    } else {
        snprintf(id_, sizeof(id_), "%02x%02x%02x%02x%02x%02x",
                 mac[0], mac[1], mac[2], mac[3], mac[4], mac[5]);
    }

    loaded_ = true;
    PW_LOG_INFO("Device ID: %s", id_);
    return id_;
}

}  // namespace firmware
