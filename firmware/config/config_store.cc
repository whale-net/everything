// ConfigStore — NVS persistence for DeviceConfig proto.

#include "firmware/config/config_store.h"

#include <Preferences.h>

#include "pb_decode.h"
#include "pb_encode.h"
#include "pw_log/log.h"

namespace firmware {

pw::Status ConfigStore::Load(firmware_DeviceConfig* out) {
    Preferences prefs;
    if (!prefs.begin("config", /*readOnly=*/true)) {
        return pw::Status::NotFound();
    }

    size_t len = prefs.getBytesLength("device_cfg");
    if (len == 0) {
        prefs.end();
        return pw::Status::NotFound();
    }
    if (len > firmware_DeviceConfig_size) {
        prefs.end();
        PW_LOG_ERROR("ConfigStore: stored config (%zu bytes) exceeds max (%zu)",
                     len, static_cast<size_t>(firmware_DeviceConfig_size));
        return pw::Status::ResourceExhausted();
    }

    uint8_t buf[firmware_DeviceConfig_size];
    prefs.getBytes("device_cfg", buf, len);
    prefs.end();

    *out = firmware_DeviceConfig_init_zero;
    pb_istream_t stream = pb_istream_from_buffer(buf, len);
    if (!pb_decode(&stream, firmware_DeviceConfig_fields, out)) {
        PW_LOG_ERROR("ConfigStore: proto decode failed");
        return pw::Status::DataLoss();
    }

    version_ = out->version;
    PW_LOG_INFO("ConfigStore: loaded v%" PRIu64, version_);
    return pw::OkStatus();
}

pw::Status ConfigStore::Save(const firmware_DeviceConfig& cfg) {
    uint8_t buf[firmware_DeviceConfig_size];
    pb_ostream_t stream = pb_ostream_from_buffer(buf, sizeof(buf));
    if (!pb_encode(&stream, firmware_DeviceConfig_fields, &cfg)) {
        PW_LOG_ERROR("ConfigStore: proto encode failed");
        return pw::Status::Internal();
    }

    Preferences prefs;
    if (!prefs.begin("config", /*readOnly=*/false)) {
        PW_LOG_ERROR("ConfigStore: failed to open NVS namespace 'config'");
        return pw::Status::Internal();
    }
    prefs.putBytes("device_cfg", buf, stream.bytes_written);
    prefs.end();

    version_ = cfg.version;
    PW_LOG_INFO("ConfigStore: saved v%" PRIu64 " (%zu bytes)",
                version_, stream.bytes_written);
    return pw::OkStatus();
}

}  // namespace firmware
