// BH1750Sensor — non-blocking ambient light sensor over II2CBus.
// See bh1750.h for protocol details and usage.

#include "firmware/sensor/bh1750.h"

#include <cstring>

#include "pw_log/log.h"
#include "pw_status/status.h"

namespace firmware {

BH1750Sensor::BH1750Sensor(II2CBus& bus, uint8_t address, const char* name,
                           uint32_t (*clock_fn)())
    : bus_(bus), address_(address), clock_fn_(clock_fn) {
    strncpy(name_buf_, name, sizeof(name_buf_) - 1);
    name_buf_[sizeof(name_buf_) - 1] = '\0';
}

bool BH1750Sensor::SetName(const char* name) {
    strncpy(name_buf_, name, sizeof(name_buf_) - 1);
    name_buf_[sizeof(name_buf_) - 1] = '\0';
    return true;
}

pw::Status BH1750Sensor::Init() {
    uint8_t cmd = kCmdPowerOn;
    pw::Status s = bus_.Write(address_, &cmd, 1);
    if (!s.ok()) {
        PW_LOG_ERROR("BH1750 '%s': power-on failed", name_buf_);
        return s;
    }
    s = Trigger();
    if (s.ok()) init_ok_ = true;
    return s;
}

SensorReading BH1750Sensor::Read() {
    if (!init_ok_) return SensorReading::Invalid();
    uint32_t now = clock_fn_();
    if (now - trigger_ms_ >= kMeasureTimeMs) {
        uint8_t buf[2] = {};
        if (bus_.Read(address_, buf, 2).ok()) {
            last_lux_ = ((uint16_t(buf[0]) << 8) | buf[1]) / 1.2f;
            valid_ = true;
        } else {
            PW_LOG_WARN("BH1750 '%s': read failed", name_buf_);
        }
        Trigger();
    }
    return valid_ ? SensorReading::Ok(last_lux_) : SensorReading::Invalid();
}

pw::Status BH1750Sensor::Trigger() {
    trigger_ms_ = clock_fn_();
    uint8_t cmd = kCmdOneShot;
    pw::Status s = bus_.Write(address_, &cmd, 1);
    if (!s.ok()) {
        PW_LOG_ERROR("BH1750 '%s': trigger failed", name_buf_);
    }
    return s;
}

}  // namespace firmware
